# R1: an organisation-level policy that denies every action outside the two India regions.
# Global services have no region (or live in us-east-1), so they are exempt through NotAction.
# This guards against a console or CLI mistake creating a resource abroad.
# [VALIDATE] the exemption list against the services actually used before attaching to production.

data "aws_iam_policy_document" "region_deny" {
  statement {
    sid    = "DenyOutsideIndiaRegions"
    effect = "Deny"
    not_actions = [
      "iam:*",
      "organizations:*",
      "route53:*",
      "route53domains:*",
      "cloudfront:*",
      "waf:*",
      "sts:*",
      "support:*",
      "trustedadvisor:*",
      "budgets:*",
      "ce:*",
      "account:*",
      "health:*",
    ]
    resources = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestedRegion"
      values   = var.allowed_regions
    }
    dynamic "condition" {
      for_each = length(var.exempt_principal_arns) > 0 ? [1] : []
      content {
        test     = "ArnNotLike"
        variable = "aws:PrincipalARN"
        values   = var.exempt_principal_arns
      }
    }
  }
}

resource "aws_organizations_policy" "region_deny" {
  name        = "mugizh-deny-outside-india"
  description = "R1: deny all actions outside ap-south-1 and ap-south-2, except global services."
  type        = "SERVICE_CONTROL_POLICY"
  content     = data.aws_iam_policy_document.region_deny.json
}

resource "aws_organizations_policy_attachment" "staging" {
  policy_id = aws_organizations_policy.region_deny.id
  target_id = var.staging_ou_id
}

resource "aws_organizations_policy_attachment" "production" {
  policy_id = aws_organizations_policy.region_deny.id
  target_id = var.production_ou_id
}

output "region_deny_policy_id" { value = aws_organizations_policy.region_deny.id }
