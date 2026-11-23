# CI access (ADR-10, TM-47, R7): GitLab OIDC to AWS, short-lived credentials, no stored keys.
# One deploy role per environment (each environment is its own AWS account). The role is
# trusted only for this project and only for the refs in allowed_subjects:
#   staging    : the protected branch (project_path:<path>:ref_type:branch:ref:main)
#   production : protected tags only  (project_path:<path>:ref_type:tag:ref:v*)
# GitLab must mark that branch and those tags protected, and the job must request an id_token
# with the audience below. [VALIDATE] the sub format against the GitLab version in use.
# This role deploys the application. It cannot change infrastructure (tofu apply is a
# separate, manual, break-glass-approved step).

variable "name" {
  type = string
}

variable "gitlab_host" {
  description = "GitLab host name without scheme."
  type        = string
  default     = "gitlab.com"
}

variable "audience" {
  description = "Audience (aud) the CI job requests for its id_token."
  type        = string
  default     = "https://gitlab.com"
}

variable "project_path" {
  description = "GitLab project path, group/project [FILL]."
  type        = string
}

variable "ref_type" {
  description = "branch (staging) or tag (production)."
  type        = string
  validation {
    condition     = contains(["branch", "tag"], var.ref_type)
    error_message = "ref_type must be branch or tag."
  }
}

variable "ref_pattern" {
  description = "Allowed ref name or pattern: main for staging, v* for production release tags."
  type        = string
}

variable "create_oidc_provider" {
  description = "Create the account's GitLab OIDC provider (one per account)."
  type        = bool
  default     = true
}

variable "oidc_provider_thumbprints" {
  description = "Root CA thumbprint(s) of the GitLab host [FILL]. Public values, not secrets."
  type        = list(string)
}

variable "cluster_arn" {
  type = string
}

variable "service_arns" {
  type = list(string)
}

variable "migrate_task_family" {
  type = string
}

variable "role_arns_to_pass" {
  description = "Task and execution role ARNs the deploy role may pass to ECS."
  type        = list(string)
}

variable "ecr_repository_arns" {
  type = list(string)
}

variable "migrate_log_group_arn" {
  description = "Log group of the migrate task, readable by the deploy role to show results."
  type        = string
}

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

locals {
  account  = data.aws_caller_identity.current.account_id
  region   = data.aws_region.current.name
  provider = var.create_oidc_provider ? aws_iam_openid_connect_provider.gitlab[0].arn : "arn:aws:iam::${local.account}:oidc-provider/${var.gitlab_host}"
}

resource "aws_iam_openid_connect_provider" "gitlab" {
  count           = var.create_oidc_provider ? 1 : 0
  url             = "https://${var.gitlab_host}"
  client_id_list  = [var.audience]
  thumbprint_list = var.oidc_provider_thumbprints
}

data "aws_iam_policy_document" "trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [local.provider]
    }
    condition {
      test     = "StringEquals"
      variable = "${var.gitlab_host}:aud"
      values   = [var.audience]
    }
    condition {
      test     = "StringLike"
      variable = "${var.gitlab_host}:sub"
      values   = ["project_path:${var.project_path}:ref_type:${var.ref_type}:ref:${var.ref_pattern}"]
    }
  }
}

resource "aws_iam_role" "deploy" {
  name                 = "${var.name}-gitlab-deploy"
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "deploy" {
  # These actions do not support resource-level permissions.
  #trivy:ignore:AVD-AWS-0057
  statement {
    sid = "NoResourceLevelSupport"
    actions = [
      "ecr:GetAuthorizationToken",
      "ecs:RegisterTaskDefinition", "ecs:DeregisterTaskDefinition",
      "ecs:DescribeTaskDefinition",
    ]
    resources = ["*"]
  }
  statement {
    sid       = "EcrPush"
    actions   = ["ecr:BatchCheckLayerAvailability", "ecr:InitiateLayerUpload", "ecr:UploadLayerPart", "ecr:CompleteLayerUpload", "ecr:PutImage", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:DescribeImages"]
    resources = var.ecr_repository_arns
  }
  statement {
    sid       = "UpdateServices"
    actions   = ["ecs:UpdateService", "ecs:DescribeServices"]
    resources = var.service_arns
  }
  statement {
    sid       = "RunMigrateTask"
    actions   = ["ecs:RunTask"]
    resources = ["arn:aws:ecs:${local.region}:${local.account}:task-definition/${var.migrate_task_family}:*"]
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [var.cluster_arn]
    }
  }
  statement {
    sid       = "ObserveTasks"
    actions   = ["ecs:DescribeTasks", "ecs:StopTask"]
    resources = ["arn:aws:ecs:${local.region}:${local.account}:task/*/*"]
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [var.cluster_arn]
    }
  }
  statement {
    sid       = "PassTaskRoles"
    actions   = ["iam:PassRole"]
    resources = var.role_arns_to_pass
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
  statement {
    sid       = "ReadMigrateLogs"
    actions   = ["logs:GetLogEvents", "logs:DescribeLogStreams"]
    resources = ["${var.migrate_log_group_arn}:*"]
  }
}

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy"
  role   = aws_iam_role.deploy.id
  policy = data.aws_iam_policy_document.deploy.json
}

output "deploy_role_arn" { value = aws_iam_role.deploy.arn }
