# Observability (R3: everything stays in CloudWatch in India). Alarms go to one SNS topic.
# NTP: Fargate uses the Amazon Time Sync Service. CERT-In traceability to NIC or NPL
# is [VALIDATE] with AWS (see infra/README.md).

variable "name" {
  type = string
}

variable "kms_key_arn" {
  description = "Data key (SNS topic encryption). Its policy lets CloudWatch alarms publish."
  type        = string
}

variable "alert_protocol" {
  description = "SNS subscription protocol: email, https, or empty to skip the subscription."
  type        = string
  default     = ""
}

variable "alert_endpoint" {
  description = "Email address or pager webhook [FILL]. Empty skips the subscription."
  type        = string
  default     = ""
}

variable "alb_arn_suffix" {
  type = string
}

variable "target_group_arn_suffix" {
  type = string
}

variable "api_log_group_name" {
  type = string
}

variable "worker_log_group_name" {
  type = string
}

variable "db_instance_identifiers" {
  description = "Map of label to RDS instance identifier."
  type        = map(string)
}

variable "db_free_storage_bytes_min" {
  description = "Alarm when free storage falls below this many bytes."
  type        = number
  default     = 10737418240
}

variable "http_5xx_percent_max" {
  type    = number
  default = 2
}

resource "aws_sns_topic" "alerts" {
  name              = "${var.name}-alerts"
  kms_master_key_id = var.kms_key_arn
}

data "aws_caller_identity" "current" {}

data "aws_iam_policy_document" "alerts" {
  statement {
    sid       = "AllowAlarmsAndEvents"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.alerts.arn]
    principals {
      type        = "Service"
      identifiers = ["cloudwatch.amazonaws.com", "events.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }

  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["sns:Publish", "sns:Subscribe"]
    resources = [aws_sns_topic.alerts.arn]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_sns_topic_policy" "alerts" {
  arn    = aws_sns_topic.alerts.arn
  policy = data.aws_iam_policy_document.alerts.json
}

resource "aws_sns_topic_subscription" "alerts" {
  count     = var.alert_endpoint == "" ? 0 : 1
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = var.alert_protocol
  endpoint  = var.alert_endpoint
}

# 5xx rate at the load balancer.
resource "aws_cloudwatch_metric_alarm" "http_5xx" {
  alarm_name          = "${var.name}-alb-5xx-rate"
  alarm_description   = "Share of requests answered with 5xx (ELB or targets) is above the limit."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  threshold           = var.http_5xx_percent_max
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]

  metric_query {
    id          = "rate"
    expression  = "100 * (FILL(e5, 0) + FILL(t5, 0)) / MAX([FILL(req, 0), 1])"
    label       = "5xx percent"
    return_data = true
  }
  metric_query {
    id = "e5"
    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_ELB_5XX_Count"
      period      = 60
      stat        = "Sum"
      dimensions  = { LoadBalancer = var.alb_arn_suffix }
    }
  }
  metric_query {
    id = "t5"
    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_Target_5XX_Count"
      period      = 60
      stat        = "Sum"
      dimensions  = { LoadBalancer = var.alb_arn_suffix }
    }
  }
  metric_query {
    id = "req"
    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "RequestCount"
      period      = 60
      stat        = "Sum"
      dimensions  = { LoadBalancer = var.alb_arn_suffix }
    }
  }
}

resource "aws_cloudwatch_metric_alarm" "unhealthy_targets" {
  alarm_name          = "${var.name}-alb-unhealthy-targets"
  alarm_description   = "One or more API targets are failing health checks."
  namespace           = "AWS/ApplicationELB"
  metric_name         = "UnHealthyHostCount"
  dimensions          = { LoadBalancer = var.alb_arn_suffix, TargetGroup = var.target_group_arn_suffix }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 3
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "rds_cpu" {
  for_each            = var.db_instance_identifiers
  alarm_name          = "${var.name}-rds-${each.key}-cpu"
  alarm_description   = "RDS CPU above 80 percent for 15 minutes."
  namespace           = "AWS/RDS"
  metric_name         = "CPUUtilization"
  dimensions          = { DBInstanceIdentifier = each.value }
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 3
  comparison_operator = "GreaterThanThreshold"
  threshold           = 80
  treat_missing_data  = "missing"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "rds_storage" {
  for_each            = var.db_instance_identifiers
  alarm_name          = "${var.name}-rds-${each.key}-free-storage"
  alarm_description   = "RDS free storage is low."
  namespace           = "AWS/RDS"
  metric_name         = "FreeStorageSpace"
  dimensions          = { DBInstanceIdentifier = each.value }
  statistic           = "Minimum"
  period              = 300
  evaluation_periods  = 2
  comparison_operator = "LessThanThreshold"
  threshold           = var.db_free_storage_bytes_min
  treat_missing_data  = "missing"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}

# The application logs every operational alert as a JSON line with "msg":"ops.alert"
# (backend/internal/app/alerts.go). Any such line pages.
resource "aws_cloudwatch_log_metric_filter" "ops_alert" {
  for_each       = { api = var.api_log_group_name, worker = var.worker_log_group_name }
  name           = "${var.name}-${each.key}-ops-alert"
  log_group_name = each.value
  pattern        = "{ $.msg = \"ops.alert\" }"
  metric_transformation {
    name          = "OpsAlert-${each.key}"
    namespace     = "${var.name}/app"
    value         = "1"
    default_value = "0"
  }
}

resource "aws_cloudwatch_metric_alarm" "ops_alert" {
  for_each            = aws_cloudwatch_log_metric_filter.ops_alert
  alarm_name          = "${var.name}-${each.key}-ops-alert"
  alarm_description   = "The ${each.key} logged ops.alert. See the reason code in the log line."
  namespace           = "${var.name}/app"
  metric_name         = "OpsAlert-${each.key}"
  statistic           = "Sum"
  period              = 60
  evaluation_periods  = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}

output "sns_topic_arn" { value = aws_sns_topic.alerts.arn }
