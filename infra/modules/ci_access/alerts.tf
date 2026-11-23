# Alerts on the CI roles and on task definitions (H2, M1). EventBridge rules on the CloudTrail management
# events of this account and region (the default bus receives them without a trail; CI uses the regional
# STS endpoint, so the assumption event is in this region). Every alert goes to the alerts topic.
# The message carries only the event name, the caller ARN and the time (no request bodies, K-DP-04).
# [VALIDATE] the patterns against real events on the first deploy (docs/ops/production-readiness.md).

locals {
  alert_prefix = "${var.name}-ci"
  ecr_prefix   = "${local.account}.dkr.ecr.${local.region}.amazonaws.com/${var.name}/"
  alert_input = {
    paths    = { event = "$.detail.eventName", who = "$.detail.userIdentity.arn", at = "$.time" }
    template = "\"CI security alert (${var.name}): <event> by <who> at <at>\""
  }
  alert_rules = {
    # H2: any assumption of the infrastructure-apply role. Expected only when someone ran tofu-plan or tofu-apply.
    infra-role-assumed = {
      description = "The infrastructure-apply role was assumed (expected only during a manual tofu job)."
      pattern = {
        source        = ["aws.sts"]
        "detail-type" = ["AWS API Call via CloudTrail"]
        detail = {
          eventSource       = ["sts.amazonaws.com"]
          eventName         = ["AssumeRoleWithWebIdentity"]
          requestParameters = { roleArn = [aws_iam_role.infra.arn] }
        }
      }
    }
    # M1 (a): a task definition with any container image outside this environment's ECR repositories.
    taskdef-foreign-image = {
      description = "A task definition was registered with an image that is not from this environment's ECR repositories."
      pattern = {
        source        = ["aws.ecs"]
        "detail-type" = ["AWS API Call via CloudTrail"]
        detail = {
          eventSource       = ["ecs.amazonaws.com"]
          eventName         = ["RegisterTaskDefinition"]
          requestParameters = { containerDefinitions = { image = [{ "anything-but" = { prefix = local.ecr_prefix } }] } }
        }
      }
    }
    # M1 (a, fallback): RunTask has no image in its request, so alert on every task-definition registration
    # or task run by any principal other than the two CI roles.
    ecs-by-other-principal = {
      description = "RegisterTaskDefinition or RunTask by a principal other than the deploy and infrastructure roles."
      pattern = {
        source        = ["aws.ecs"]
        "detail-type" = ["AWS API Call via CloudTrail"]
        detail = {
          eventSource = ["ecs.amazonaws.com"]
          eventName   = ["RegisterTaskDefinition", "RunTask"]
          userIdentity = {
            sessionContext = { sessionIssuer = { arn = [{ "anything-but" = [aws_iam_role.deploy.arn, aws_iam_role.infra.arn] }] } }
          }
        }
      }
    }
  }
}

resource "aws_cloudwatch_event_rule" "alert" {
  for_each      = local.alert_rules
  name          = "${local.alert_prefix}-${each.key}"
  description   = each.value.description
  event_pattern = jsonencode(each.value.pattern)
}

resource "aws_cloudwatch_event_target" "alert" {
  for_each = local.alert_rules
  rule     = aws_cloudwatch_event_rule.alert[each.key].name
  arn      = var.alerts_topic_arn
  input_transformer {
    input_paths    = local.alert_input.paths
    input_template = local.alert_input.template
  }
}
