# Network: public subnets (ALB), NAT subnets (NAT gateways only), optional firewall subnets,
# private app subnets (egress through NAT, or through the firewall then NAT), and private data
# subnets with NO internet route. AWS services are reached through VPC endpoints.

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

locals {
  idx = { for i, az in var.azs : az => i }
  # /20 blocks: public 0-1, nat 2-3, firewall 4-5, app 8-9, data 12-13.
  public_cidr   = { for az, i in local.idx : az => cidrsubnet(var.cidr, 4, i) }
  nat_cidr      = { for az, i in local.idx : az => cidrsubnet(var.cidr, 4, i + 2) }
  firewall_cidr = { for az, i in local.idx : az => cidrsubnet(var.cidr, 4, i + 4) }
  app_cidr      = { for az, i in local.idx : az => cidrsubnet(var.cidr, 4, i + 8) }
  data_cidr     = { for az, i in local.idx : az => cidrsubnet(var.cidr, 4, i + 12) }
  nat_azs       = var.nat_per_az ? var.azs : [var.azs[0]]
  nat_for_az    = { for az in var.azs : az => (var.nat_per_az ? az : var.azs[0]) }
  fw_endpoint = var.enable_egress_firewall ? {
    for s in aws_networkfirewall_firewall.egress[0].firewall_status[0].sync_states : s.availability_zone => s.attachment[0].endpoint_id
  } : {}
  interface_endpoints = ["kms", "secretsmanager", "logs", "ecr.api", "ecr.dkr"]
}

resource "aws_vpc" "this" {
  cidr_block           = var.cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.name }
}

# The default security group carries no rules, so nothing can use it by accident.
resource "aws_default_security_group" "default" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${var.name}-default-unused" }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = var.name }
}

resource "aws_subnet" "public" {
  for_each                = local.public_cidr
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = false
  tags                    = { Name = "${var.name}-public-${each.key}", tier = "public" }
}

resource "aws_subnet" "nat" {
  for_each                = local.nat_cidr
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = false
  tags                    = { Name = "${var.name}-nat-${each.key}", tier = "nat" }
}

resource "aws_subnet" "firewall" {
  for_each                = var.enable_egress_firewall ? local.firewall_cidr : {}
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = false
  tags                    = { Name = "${var.name}-firewall-${each.key}", tier = "firewall" }
}

resource "aws_subnet" "app" {
  for_each                = local.app_cidr
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = false
  tags                    = { Name = "${var.name}-app-${each.key}", tier = "app" }
}

resource "aws_subnet" "data" {
  for_each                = local.data_cidr
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = false
  tags                    = { Name = "${var.name}-data-${each.key}", tier = "data" }
}

# NAT gateways (egress only). Elastic IPs are the fixed source addresses vendors may allow-list.
resource "aws_eip" "nat" {
  for_each = toset(local.nat_azs)
  domain   = "vpc"
  tags     = { Name = "${var.name}-nat-${each.key}" }
}

resource "aws_nat_gateway" "this" {
  for_each      = toset(local.nat_azs)
  allocation_id = aws_eip.nat[each.key].id
  subnet_id     = aws_subnet.nat[each.key].id
  tags          = { Name = "${var.name}-${each.key}" }
  depends_on    = [aws_internet_gateway.this]
}

# Public route table: ALB subnets, default route to the internet gateway.
resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${var.name}-public" }
}

resource "aws_route" "public_internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this.id
}

resource "aws_route_table_association" "public" {
  for_each       = aws_subnet.public
  subnet_id      = each.value.id
  route_table_id = aws_route_table.public.id
}

# NAT subnets. With the firewall on, return traffic for the app subnets goes back through it.
resource "aws_route_table" "nat" {
  for_each = toset(var.azs)
  vpc_id   = aws_vpc.this.id
  tags     = { Name = "${var.name}-nat-${each.key}" }
}

resource "aws_route" "nat_internet" {
  for_each               = aws_route_table.nat
  route_table_id         = each.value.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this.id
}

resource "aws_route" "nat_return_via_firewall" {
  for_each               = var.enable_egress_firewall ? { for az in var.azs : az => az } : {}
  route_table_id         = aws_route_table.nat[each.key].id
  destination_cidr_block = local.app_cidr[each.key]
  vpc_endpoint_id        = local.fw_endpoint[each.key]
}

resource "aws_route_table_association" "nat" {
  for_each       = aws_subnet.nat
  subnet_id      = each.value.id
  route_table_id = aws_route_table.nat[each.key].id
}

# Firewall subnets: default route to the NAT gateway.
resource "aws_route_table" "firewall" {
  for_each = var.enable_egress_firewall ? toset(var.azs) : toset([])
  vpc_id   = aws_vpc.this.id
  tags     = { Name = "${var.name}-firewall-${each.key}" }
}

resource "aws_route" "firewall_nat" {
  for_each               = aws_route_table.firewall
  route_table_id         = each.value.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.this[local.nat_for_az[each.key]].id
}

resource "aws_route_table_association" "firewall" {
  for_each       = aws_subnet.firewall
  subnet_id      = each.value.id
  route_table_id = aws_route_table.firewall[each.key].id
}

# Private app route tables: default route to NAT, or to the firewall endpoint when enabled.
resource "aws_route_table" "app" {
  for_each = toset(var.azs)
  vpc_id   = aws_vpc.this.id
  tags     = { Name = "${var.name}-app-${each.key}" }
}

resource "aws_route" "app_default" {
  for_each               = aws_route_table.app
  route_table_id         = each.value.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = var.enable_egress_firewall ? null : aws_nat_gateway.this[local.nat_for_az[each.key]].id
  vpc_endpoint_id        = var.enable_egress_firewall ? local.fw_endpoint[each.key] : null
}

resource "aws_route_table_association" "app" {
  for_each       = aws_subnet.app
  subnet_id      = each.value.id
  route_table_id = aws_route_table.app[each.key].id
}

# Data route table: local routes plus the S3 gateway endpoint only. No internet route.
resource "aws_route_table" "data" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${var.name}-data" }
}

resource "aws_route_table_association" "data" {
  for_each       = aws_subnet.data
  subnet_id      = each.value.id
  route_table_id = aws_route_table.data.id
}

# Security groups. Core tasks and scheduler-core tasks have separate groups, so scheduler-core
# can reach only HTTPS (AWS endpoints, and the core API through the public ALB) and the core
# database port. It has no route to the key-store database (that group admits core tasks only).
resource "aws_security_group" "app" {
  name_prefix = "${var.name}-app-"
  description = "Core ECS tasks (api, worker, migrate)"
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${var.name}-app" }
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_security_group" "scheduler" {
  name_prefix = "${var.name}-scheduler-"
  description = "scheduler-core tasks (scheduler-core, scheduler-migrate)"
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${var.name}-scheduler" }
  lifecycle {
    create_before_destroy = true
  }
}

# Tasks need outbound HTTPS to AWS endpoints (inside the VPC) and to vendors (through NAT).
# The internet-bound part is limited by the egress firewall (TM-41) where enabled.
# scheduler-core also uses it to call the core API through the public ALB (callback).
#trivy:ignore:AVD-AWS-0104
resource "aws_vpc_security_group_egress_rule" "app_https_out" {
  security_group_id = aws_security_group.app.id
  description       = "HTTPS to AWS endpoints and allow-listed vendors"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

#trivy:ignore:AVD-AWS-0104
resource "aws_vpc_security_group_egress_rule" "scheduler_https_out" {
  security_group_id = aws_security_group.scheduler.id
  description       = "HTTPS to AWS endpoints and to the core API through the public ALB"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "app_postgres_out" {
  security_group_id = aws_security_group.app.id
  description       = "PostgreSQL to the VPC (data subnets)"
  ip_protocol       = "tcp"
  from_port         = 5432
  to_port           = 5432
  cidr_ipv4         = var.cidr
}

resource "aws_vpc_security_group_egress_rule" "scheduler_postgres_out" {
  security_group_id = aws_security_group.scheduler.id
  description       = "PostgreSQL to the VPC (data subnets); the key-store database does not admit this group"
  ip_protocol       = "tcp"
  from_port         = 5432
  to_port           = 5432
  cidr_ipv4         = var.cidr
}

# Core tasks call scheduler-core (Service Connect) on 8081.
resource "aws_vpc_security_group_egress_rule" "app_to_scheduler" {
  security_group_id            = aws_security_group.app.id
  description                  = "Core to scheduler-core"
  ip_protocol                  = "tcp"
  from_port                    = 8081
  to_port                      = 8081
  referenced_security_group_id = aws_security_group.scheduler.id
}

resource "aws_vpc_security_group_ingress_rule" "scheduler_from_app" {
  security_group_id            = aws_security_group.scheduler.id
  description                  = "From core tasks"
  ip_protocol                  = "tcp"
  from_port                    = 8081
  to_port                      = 8081
  referenced_security_group_id = aws_security_group.app.id
}

resource "aws_security_group" "endpoints" {
  name_prefix = "${var.name}-vpce-"
  description = "Interface VPC endpoints"
  vpc_id      = aws_vpc.this.id
  tags        = { Name = "${var.name}-vpce" }
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "endpoints_https" {
  for_each                     = { app = aws_security_group.app.id, scheduler = aws_security_group.scheduler.id }
  security_group_id            = aws_security_group.endpoints.id
  description                  = "HTTPS from the ${each.key} tier"
  ip_protocol                  = "tcp"
  from_port                    = 443
  to_port                      = 443
  referenced_security_group_id = each.value
}

# Endpoint policies (L6): only principals and buckets of this account. [VALIDATE] on first
# apply: ECR pulls also read layers from an AWS-owned bucket, which is allowed explicitly.
data "aws_iam_policy_document" "endpoint_s3" {
  statement {
    sid       = "ThisAccountBucketsOnly"
    actions   = ["s3:*"]
    resources = ["*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
  statement {
    sid       = "EcrLayerBucket"
    actions   = ["s3:GetObject"]
    resources = ["arn:aws:s3:::prod-${data.aws_region.current.name}-starport-layer-bucket/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
  }
}

data "aws_iam_policy_document" "endpoint_interface" {
  statement {
    sid       = "ThisAccountPrincipalsOnly"
    actions   = ["*"]
    resources = ["*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:PrincipalAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

# VPC endpoints: AWS API traffic stays on the private network.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${data.aws_region.current.name}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = concat([for rt in aws_route_table.app : rt.id], [aws_route_table.data.id])
  policy            = data.aws_iam_policy_document.endpoint_s3.json
  tags              = { Name = "${var.name}-s3" }
}

resource "aws_vpc_endpoint" "interface" {
  for_each            = toset(local.interface_endpoints)
  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${data.aws_region.current.name}.${each.key}"
  vpc_endpoint_type   = "Interface"
  subnet_ids          = [for s in aws_subnet.app : s.id]
  security_group_ids  = [aws_security_group.endpoints.id]
  private_dns_enabled = true
  policy              = data.aws_iam_policy_document.endpoint_interface.json
  tags                = { Name = "${var.name}-${each.key}" }
}

# Flow logs to CloudWatch, 365 days, encrypted (K-SEC-01).
resource "aws_cloudwatch_log_group" "flow" {
  name              = "/mugizh/${var.name}/vpc-flow"
  retention_in_days = var.flow_log_retention_days
  kms_key_id        = var.kms_key_arn
}

data "aws_iam_policy_document" "flow_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["vpc-flow-logs.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

data "aws_iam_policy_document" "flow" {
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"]
    resources = ["${aws_cloudwatch_log_group.flow.arn}:*"]
  }
}

resource "aws_iam_role" "flow" {
  name_prefix        = "${var.name}-flow-"
  assume_role_policy = data.aws_iam_policy_document.flow_assume.json
}

resource "aws_iam_role_policy" "flow" {
  name   = "write-flow-logs"
  role   = aws_iam_role.flow.id
  policy = data.aws_iam_policy_document.flow.json
}

resource "aws_flow_log" "this" {
  vpc_id          = aws_vpc.this.id
  traffic_type    = "ALL"
  log_destination = aws_cloudwatch_log_group.flow.arn
  iam_role_arn    = aws_iam_role.flow.arn
}

# Egress allow-list (TM-41): AWS Network Firewall, domain allow-list, drop everything else.
resource "aws_networkfirewall_rule_group" "egress_domains" {
  count    = var.enable_egress_firewall ? 1 : 0
  name     = "${var.name}-egress-domains"
  type     = "STATEFUL"
  capacity = 100
  rule_group {
    stateful_rule_options {
      rule_order = "STRICT_ORDER"
    }
    rules_source {
      rules_source_list {
        generated_rules_type = "ALLOWLIST"
        target_types         = ["HTTP_HOST", "TLS_SNI"]
        targets              = var.egress_allowed_domains
      }
    }
    rule_variables {
      ip_sets {
        key = "HOME_NET"
        ip_set {
          definition = [var.cidr]
        }
      }
    }
  }
  encryption_configuration {
    type   = "CUSTOMER_KMS"
    key_id = var.kms_key_arn
  }
  lifecycle {
    precondition {
      condition     = length(var.egress_allowed_domains) > 0
      error_message = "egress_allowed_domains must not be empty when the egress firewall is enabled."
    }
  }
}

resource "aws_networkfirewall_firewall_policy" "egress" {
  count = var.enable_egress_firewall ? 1 : 0
  name  = "${var.name}-egress"
  firewall_policy {
    stateless_default_actions          = ["aws:forward_to_sfe"]
    stateless_fragment_default_actions = ["aws:forward_to_sfe"]
    stateful_default_actions           = ["aws:drop_established", "aws:alert_established"]
    stateful_engine_options {
      rule_order = "STRICT_ORDER"
    }
    stateful_rule_group_reference {
      priority     = 10
      resource_arn = aws_networkfirewall_rule_group.egress_domains[0].arn
    }
  }
  encryption_configuration {
    type   = "CUSTOMER_KMS"
    key_id = var.kms_key_arn
  }
}

resource "aws_networkfirewall_firewall" "egress" {
  count               = var.enable_egress_firewall ? 1 : 0
  name                = "${var.name}-egress"
  vpc_id              = aws_vpc.this.id
  firewall_policy_arn = aws_networkfirewall_firewall_policy.egress[0].arn
  delete_protection   = true
  dynamic "subnet_mapping" {
    for_each = aws_subnet.firewall
    content {
      subnet_id = subnet_mapping.value.id
    }
  }
  encryption_configuration {
    type   = "CUSTOMER_KMS"
    key_id = var.kms_key_arn
  }
}

resource "aws_cloudwatch_log_group" "firewall" {
  count             = var.enable_egress_firewall ? 1 : 0
  name              = "/mugizh/${var.name}/network-firewall"
  retention_in_days = var.flow_log_retention_days
  kms_key_id        = var.kms_key_arn
}

resource "aws_networkfirewall_logging_configuration" "egress" {
  count        = var.enable_egress_firewall ? 1 : 0
  firewall_arn = aws_networkfirewall_firewall.egress[0].arn
  logging_configuration {
    log_destination_config {
      log_type             = "ALERT"
      log_destination_type = "CloudWatchLogs"
      log_destination      = { logGroup = aws_cloudwatch_log_group.firewall[0].name }
    }
  }
}
