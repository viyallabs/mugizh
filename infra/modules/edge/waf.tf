# Regional WAF: /internal/* only from the NAT addresses, a rate-based rule, and the AWS managed
# common and known-bad-inputs sets. The common set is split into three scoped rules so that
# a few routes can accept large bodies or clinical free text, while every other route keeps
# every rule blocking (M3). [VALIDATE] this tuning against the real API routes and test traffic.

variable "internal_allowed_cidrs" {
  description = "CIDRs allowed to call /internal/* (the NAT public addresses, as /32). Others are blocked by the WAF."
  type        = list(string)
}

resource "aws_wafv2_ip_set" "internal_callers" {
  name               = "${var.name}-internal-callers"
  scope              = "REGIONAL"
  ip_address_version = "IPV4"
  addresses          = var.internal_allowed_cidrs
}

# Document and evidence uploads: large bodies are legitimate (SizeRestrictions_BODY counted).
resource "aws_wafv2_regex_pattern_set" "uploads" {
  name  = "${var.name}-upload-routes"
  scope = "REGIONAL"
  regular_expression {
    regex_string = "^/v1/me/documents$"
  }
  regular_expression {
    regex_string = "^/v1/me/verification/evidence$"
  }
  regular_expression {
    regex_string = "^/v1/clinics/[^/]+/verification/evidence$"
  }
}

# Clinical routes carry free text (CrossSiteScripting_BODY counted; output encoding in the clients).
resource "aws_wafv2_regex_pattern_set" "clinical" {
  name  = "${var.name}-clinical-routes"
  scope = "REGIONAL"
  regular_expression {
    regex_string = "^/v1/consults"
  }
  regular_expression {
    regex_string = "^/v1/appointments/[^/]+/notes"
  }
  regular_expression {
    regex_string = "^/v1/prescriptions"
  }
}

resource "aws_wafv2_web_acl" "this" {
  name  = "${var.name}-api"
  scope = "REGIONAL"
  default_action {
    allow {}
  }

  # The scheduler callback is also HMAC-signed and timestamped; this keeps the route off the internet.
  rule {
    name     = "internal-only-from-nat"
    priority = 0
    action {
      block {}
    }
    statement {
      and_statement {
        statement {
          byte_match_statement {
            search_string         = "/internal/"
            positional_constraint = "STARTS_WITH"
            field_to_match {
              uri_path {}
            }
            text_transformation {
              priority = 0
              type     = "NORMALIZE_PATH"
            }
            text_transformation {
              priority = 1
              type     = "LOWERCASE"
            }
          }
        }
        statement {
          not_statement {
            statement {
              ip_set_reference_statement {
                arn = aws_wafv2_ip_set.internal_callers.arn
              }
            }
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "internal-only-from-nat"
      sampled_requests_enabled   = false
    }
  }

  rule {
    name     = "rate-limit-per-ip"
    priority = 1
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit              = var.rate_limit_per_5min
        aggregate_key_type = "IP"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "rate-limit-per-ip"
      sampled_requests_enabled   = false
    }
  }

  # Common set, every other route: all rules blocking.
  rule {
    name     = "aws-common-default"
    priority = 10
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesCommonRuleSet"
        scope_down_statement {
          not_statement {
            statement {
              or_statement {
                statement {
                  regex_pattern_set_reference_statement {
                    arn = aws_wafv2_regex_pattern_set.uploads.arn
                    field_to_match {
                      uri_path {}
                    }
                    text_transformation {
                      priority = 0
                      type     = "NONE"
                    }
                  }
                }
                statement {
                  regex_pattern_set_reference_statement {
                    arn = aws_wafv2_regex_pattern_set.clinical.arn
                    field_to_match {
                      uri_path {}
                    }
                    text_transformation {
                      priority = 0
                      type     = "NONE"
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "aws-common-default"
      sampled_requests_enabled   = false
    }
  }

  # Upload routes: SizeRestrictions_BODY is counted, the rest of the set blocks.
  rule {
    name     = "aws-common-uploads"
    priority = 11
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesCommonRuleSet"
        rule_action_override {
          name = "SizeRestrictions_BODY"
          action_to_use {
            count {}
          }
        }
        scope_down_statement {
          regex_pattern_set_reference_statement {
            arn = aws_wafv2_regex_pattern_set.uploads.arn
            field_to_match {
              uri_path {}
            }
            text_transformation {
              priority = 0
              type     = "NONE"
            }
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "aws-common-uploads"
      sampled_requests_enabled   = false
    }
  }

  # Clinical routes: CrossSiteScripting_BODY is counted, the rest of the set blocks.
  rule {
    name     = "aws-common-clinical"
    priority = 12
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesCommonRuleSet"
        rule_action_override {
          name = "CrossSiteScripting_BODY"
          action_to_use {
            count {}
          }
        }
        scope_down_statement {
          regex_pattern_set_reference_statement {
            arn = aws_wafv2_regex_pattern_set.clinical.arn
            field_to_match {
              uri_path {}
            }
            text_transformation {
              priority = 0
              type     = "NONE"
            }
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "aws-common-clinical"
      sampled_requests_enabled   = false
    }
  }

  rule {
    name     = "aws-known-bad-inputs"
    priority = 20
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "aws-known-bad-inputs"
      sampled_requests_enabled   = false
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${var.name}-api"
    sampled_requests_enabled   = false
  }
}
