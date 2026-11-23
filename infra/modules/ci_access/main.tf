# CI access (ADR-10, TM-47, R7): GitLab OIDC to AWS, short-lived credentials, no stored keys.
# One deploy role per environment (each environment is its own AWS account). The role is
# trusted only for this project and only for the refs in allowed_subjects:
#   staging    : the protected branch (project_path:<path>:ref_type:branch:ref:main)
#   production : protected release tags only, v0* to v9* (project_path:<path>:ref_type:tag:ref:v1*).
#                IAM StringLike has no character classes, so v[0-9]* is written as ten patterns
# GitLab must mark that branch and those tags protected, and the job must request an id_token
# with the audience below. [VALIDATE] the sub format against the GitLab version in use.
# This role deploys the application (tools/ci/deploy.sh, S-094) and holds exactly the calls that script
# makes. It cannot change infrastructure (tofu apply is a separate manual job with its own role,
# not built here).

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

variable "ref_patterns" {
  description = "Allowed ref names or patterns (IAM StringLike supports only * and ?): [\"main\"] for staging, v0* to v9* for production release tags."
  type        = list(string)
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

variable "migrate_task_families" {
  description = "Families of the one-off migrate task definitions the role may run."
  type        = list(string)
}

variable "role_arns_to_pass" {
  description = "Task and execution role ARNs the deploy role may pass to ECS."
  type        = list(string)
}

variable "ecr_repository_arns" {
  type = list(string)
}

variable "alerts_topic_arn" {
  description = "SNS alerts topic (modules/observability) that receives the CI-role and task-definition alerts."
  type        = string
}

variable "task_families" {
  description = "Families of all task definitions the deploy script registers new revisions of (api, worker, scheduler-core, migrate, scheduler-migrate)."
  type        = list(string)
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
      values   = [for p in var.ref_patterns : "project_path:${var.project_path}:ref_type:${var.ref_type}:ref:${p}"]
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
      "ecs:RegisterTaskDefinition",
      "ecs:DescribeTaskDefinition",
    ]
    resources = ["*"]
  }
  # The script copies the tags of the current revision to the new one (describe with TAGS, register with tags).
  statement {
    sid       = "TagTaskDefinitions"
    actions   = ["ecs:TagResource", "ecs:ListTagsForResource"]
    resources = [for f in var.task_families : "arn:aws:ecs:${local.region}:${local.account}:task-definition/${f}:*"]
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
    resources = [for f in var.migrate_task_families : "arn:aws:ecs:${local.region}:${local.account}:task-definition/${f}:*"]
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [var.cluster_arn]
    }
  }
  statement {
    sid       = "ObserveTasks"
    actions   = ["ecs:DescribeTasks"]
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
}

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy"
  role   = aws_iam_role.deploy.id
  policy = data.aws_iam_policy_document.deploy.json
}

output "deploy_role_arn" { value = aws_iam_role.deploy.arn }
