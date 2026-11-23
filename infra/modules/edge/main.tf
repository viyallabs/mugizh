# Edge: ALB (HTTPS only, TLS 1.2+, HTTP redirects) + regional WAF. No CloudFront for the API
# (responses are no-store; R5). Static assets may get a CDN later.

variable "name" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "public_subnet_ids" {
  type = list(string)
}

variable "app_security_group_id" {
  description = "Task security group; the ALB may reach it on the container port only."
  type        = string
}

variable "certificate_arn" {
  description = "ACM certificate ARN (regional, issued in this region) [FILL]."
  type        = string
}

variable "logs_bucket_name" {
  description = "Access-log bucket (storage module, allow_alb_logs = true)."
  type        = string
}

variable "kms_key_arn" {
  description = "Data key for the WAF log group."
  type        = string
}

variable "container_port" {
  type    = number
  default = 8080
}

variable "health_check_path" {
  type    = string
  default = "/health/ready"
}

variable "rate_limit_per_5min" {
  description = "Requests per source IP per 5 minutes before WAF blocks."
  type        = number
  default     = 2000
}

variable "deletion_protection" {
  type    = bool
  default = true
}

variable "log_retention_days" {
  type    = number
  default = 365
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# The public API is reachable from the internet by design; the WAF and the application's own
# rate limits and authentication protect it. (Ingress rules are not flagged by Trivy 0.65.)
resource "aws_security_group" "alb" {
  name_prefix = "${var.name}-alb-"
  description = "Public ALB: HTTPS and HTTP (redirect only)"
  vpc_id      = var.vpc_id
  tags        = { Name = "${var.name}-alb" }
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "https" {
  security_group_id = aws_security_group.alb.id
  description       = "HTTPS from the internet"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_ingress_rule" "http_redirect" {
  security_group_id = aws_security_group.alb.id
  description       = "HTTP from the internet, answered with a redirect to HTTPS"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "to_app" {
  security_group_id            = aws_security_group.alb.id
  description                  = "To the API tasks"
  ip_protocol                  = "tcp"
  from_port                    = var.container_port
  to_port                      = var.container_port
  referenced_security_group_id = var.app_security_group_id
}

resource "aws_vpc_security_group_ingress_rule" "app_from_alb" {
  security_group_id            = var.app_security_group_id
  description                  = "From the ALB"
  ip_protocol                  = "tcp"
  from_port                    = var.container_port
  to_port                      = var.container_port
  referenced_security_group_id = aws_security_group.alb.id
}

# Public load balancer by design (API for patient and doctor apps).
#trivy:ignore:AVD-AWS-0053
resource "aws_lb" "this" {
  name                       = "${var.name}-api"
  load_balancer_type         = "application"
  internal                   = false
  subnets                    = var.public_subnet_ids
  security_groups            = [aws_security_group.alb.id]
  drop_invalid_header_fields = true
  enable_deletion_protection = var.deletion_protection
  idle_timeout               = 60
  access_logs {
    bucket  = var.logs_bucket_name
    prefix  = "alb"
    enabled = true
  }
}

resource "aws_lb_target_group" "api" {
  name_prefix = "api-"
  port        = var.container_port
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = var.vpc_id
  health_check {
    path                = var.health_check_path
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
  deregistration_delay = 30
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"
  default_action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

resource "aws_wafv2_web_acl_association" "this" {
  resource_arn = aws_lb.this.arn
  web_acl_arn  = aws_wafv2_web_acl.this.arn
}

# WAF logs: query strings and credentials are redacted (K-DP-04).
resource "aws_cloudwatch_log_group" "waf" {
  name              = "aws-waf-logs-${var.name}"
  retention_in_days = var.log_retention_days
  kms_key_id        = var.kms_key_arn
}

data "aws_iam_policy_document" "waf_logs" {
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.waf.arn}:*"]
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_cloudwatch_log_resource_policy" "waf" {
  policy_name     = "${var.name}-waf-logs"
  policy_document = data.aws_iam_policy_document.waf_logs.json
}

resource "aws_wafv2_web_acl_logging_configuration" "this" {
  resource_arn            = aws_wafv2_web_acl.this.arn
  log_destination_configs = [aws_cloudwatch_log_group.waf.arn]
  redacted_fields {
    query_string {}
  }
  redacted_fields {
    single_header {
      name = "authorization"
    }
  }
  redacted_fields {
    single_header {
      name = "cookie"
    }
  }
  depends_on = [aws_cloudwatch_log_resource_policy.waf]
}

output "alb_dns_name" { value = aws_lb.this.dns_name }
output "alb_zone_id" { value = aws_lb.this.zone_id }
output "alb_arn_suffix" { value = aws_lb.this.arn_suffix }
# Depends on the HTTPS listener, so the ECS service is not created before the target group
# is attached to a load balancer.
output "target_group_arn" {
  value      = aws_lb_target_group.api.arn
  depends_on = [aws_lb_listener.https]
}
output "target_group_arn_suffix" { value = aws_lb_target_group.api.arn_suffix }
