# KMS keys (K-DP-03, R6).
#   data            : one multi-region key per environment. Wraps the patient, care and service
#                     data keys (encryption-design section 3), and encrypts S3, RDS (core), logs,
#                     secrets, ECR and SNS.
#   keystore_backup : a SEPARATE multi-region key for the key-store database and its backups
#                     (encryption-design section 4: backups under a separate key, 35 days max).
# Key policies enable IAM policies in this account (so task roles get exactly the actions their
# own policies list) and allow named AWS services only for this account and region.
# Set replica_of to build the DR copies (replica keys) in the second India region.

variable "name" {
  description = "Name prefix, for example mugizh-production."
  type        = string
}

variable "replica_of" {
  description = "When set, create replica keys of these primary multi-region keys (DR region) instead of new primaries."
  type = object({
    data_key_arn            = string
    keystore_backup_key_arn = string
  })
  default = null
}

variable "key_admin_arns" {
  description = "IAM role ARNs that may administer (not use) the keys. Break-glass or infra role [FILL]."
  type        = list(string)
  default     = []
}

variable "deletion_window_days" {
  description = "Waiting period before a key scheduled for deletion is destroyed."
  type        = number
  default     = 30
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  account = data.aws_caller_identity.current.account_id
  region  = data.aws_region.current.name
}

data "aws_iam_policy_document" "data" {
  statement {
    sid       = "EnableIAMPolicies"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${local.account}:root"]
    }
  }

  statement {
    sid       = "CloudWatchLogsInThisAccountAndRegion"
    actions   = ["kms:Encrypt*", "kms:Decrypt*", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:Describe*"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["logs.${local.region}.amazonaws.com"]
    }
    condition {
      test     = "ArnLike"
      variable = "kms:EncryptionContext:aws:logs:arn"
      values   = ["arn:aws:logs:${local.region}:${local.account}:*"]
    }
  }

  statement {
    sid       = "CloudWatchAlarmsToEncryptedSNS"
    actions   = ["kms:Decrypt", "kms:GenerateDataKey*"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["cloudwatch.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account]
    }
  }

  dynamic "statement" {
    for_each = length(var.key_admin_arns) > 0 ? [1] : []
    content {
      sid = "KeyAdministrationNoUse"
      actions = [
        "kms:Create*", "kms:Describe*", "kms:Enable*", "kms:List*", "kms:Put*", "kms:Update*",
        "kms:Revoke*", "kms:Disable*", "kms:Get*", "kms:ScheduleKeyDeletion", "kms:CancelKeyDeletion",
        "kms:ReplicateKey", "kms:TagResource", "kms:UntagResource",
      ]
      resources = ["*"]
      principals {
        type        = "AWS"
        identifiers = var.key_admin_arns
      }
    }
  }
}

data "aws_iam_policy_document" "keystore_backup" {
  statement {
    sid       = "EnableIAMPolicies"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${local.account}:root"]
    }
  }
}

resource "aws_kms_key" "data" {
  count                   = var.replica_of == null ? 1 : 0
  description             = "${var.name} data key (multi-region)"
  multi_region            = true
  enable_key_rotation     = true
  rotation_period_in_days = 365
  deletion_window_in_days = var.deletion_window_days
  policy                  = data.aws_iam_policy_document.data.json
}

resource "aws_kms_key" "keystore_backup" {
  count                   = var.replica_of == null ? 1 : 0
  description             = "${var.name} key-store database and backups (separate key, multi-region)"
  multi_region            = true
  enable_key_rotation     = true
  rotation_period_in_days = 365
  deletion_window_in_days = var.deletion_window_days
  policy                  = data.aws_iam_policy_document.keystore_backup.json
}

resource "aws_kms_replica_key" "data" {
  count                   = var.replica_of == null ? 0 : 1
  description             = "${var.name} data key (replica)"
  primary_key_arn         = var.replica_of.data_key_arn
  deletion_window_in_days = var.deletion_window_days
  policy                  = data.aws_iam_policy_document.data.json
}

resource "aws_kms_replica_key" "keystore_backup" {
  count                   = var.replica_of == null ? 0 : 1
  description             = "${var.name} key-store database and backups (replica)"
  primary_key_arn         = var.replica_of.keystore_backup_key_arn
  deletion_window_in_days = var.deletion_window_days
  policy                  = data.aws_iam_policy_document.keystore_backup.json
}

locals {
  data_key_arn            = var.replica_of == null ? aws_kms_key.data[0].arn : aws_kms_replica_key.data[0].arn
  data_key_id             = var.replica_of == null ? aws_kms_key.data[0].key_id : aws_kms_replica_key.data[0].key_id
  keystore_backup_key_arn = var.replica_of == null ? aws_kms_key.keystore_backup[0].arn : aws_kms_replica_key.keystore_backup[0].arn
  keystore_backup_key_id  = var.replica_of == null ? aws_kms_key.keystore_backup[0].key_id : aws_kms_replica_key.keystore_backup[0].key_id
}

resource "aws_kms_alias" "data" {
  name          = "alias/${var.name}-data"
  target_key_id = local.data_key_id
}

resource "aws_kms_alias" "keystore_backup" {
  name          = "alias/${var.name}-keystore-backup"
  target_key_id = local.keystore_backup_key_id
}

output "data_key_arn" { value = local.data_key_arn }
output "keystore_backup_key_arn" { value = local.keystore_backup_key_arn }
