# Object storage (K-DP-03, K-DP-07, TM-38, D-74). All buckets private, bucket-owner-enforced,
# versioned, TLS-only.
#   blobs : uploads and exports (SSE-KMS, non-current versions expire after 35 days)
#   worm  : Object Lock, COMPLIANCE mode (key tombstones, erasure journal, audit anchors)
#   logs  : access logs (ALB access logs only support SSE-S3, so this bucket uses AES256)

variable "name" {
  description = "Name prefix, for example mugizh-production."
  type        = string
}

variable "kms_key_arn" {
  description = "Environment data key (SSE-KMS for blobs and worm)."
  type        = string
}

variable "worm_retention_days" {
  description = "Default Object Lock retention in COMPLIANCE mode. Nobody, including root, can shorten or delete early. [CONFIRM] the period with the founder and lawyer before production."
  type        = number
  validation {
    condition     = var.worm_retention_days >= 1
    error_message = "worm_retention_days must be at least 1."
  }
}

variable "noncurrent_expiry_days" {
  description = "Non-current versions of blobs expire after this many days (RET-17)."
  type        = number
  default     = 35
}

variable "log_retention_days" {
  description = "Access logs expire after this many days (RET-07)."
  type        = number
  default     = 365
}

variable "allow_alb_logs" {
  description = "Allow the regional load balancer service to write ALB access logs here (primary region only)."
  type        = bool
  default     = false
}

variable "replication" {
  description = "When set, replicate blobs and worm to these DR buckets (same account, other India region)."
  type = object({
    blobs_bucket_arn = string
    worm_bucket_arn  = string
    kms_key_arn      = string
  })
  default = null
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  account = data.aws_caller_identity.current.account_id
  region  = data.aws_region.current.name
  keys    = toset(["blobs", "worm", "logs"])
}

resource "aws_s3_bucket" "this" {
  for_each            = local.keys
  bucket              = "${var.name}-${each.key}-${local.account}-${local.region}"
  object_lock_enabled = each.key == "worm"
  force_destroy       = false
}

resource "aws_s3_bucket_public_access_block" "this" {
  for_each                = aws_s3_bucket.this
  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "this" {
  for_each = aws_s3_bucket.this
  bucket   = each.value.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "this" {
  for_each = aws_s3_bucket.this
  bucket   = each.value.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "kms" {
  for_each = toset(["blobs", "worm"])
  bucket   = aws_s3_bucket.this[each.key].id
  rule {
    bucket_key_enabled = true
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.kms_key_arn
    }
  }
}

# ALB access logs cannot be delivered to an SSE-KMS bucket (AWS limitation).
# Accepted finding, see infra/README.md.
#trivy:ignore:AVD-AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "logs" {
  bucket = aws_s3_bucket.this["logs"].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_object_lock_configuration" "worm" {
  bucket = aws_s3_bucket.this["worm"].id
  rule {
    default_retention {
      mode = "COMPLIANCE"
      days = var.worm_retention_days
    }
  }
  depends_on = [aws_s3_bucket_versioning.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "blobs" {
  bucket = aws_s3_bucket.this["blobs"].id
  rule {
    id     = "expire-noncurrent"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = var.noncurrent_expiry_days
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
  depends_on = [aws_s3_bucket_versioning.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "logs" {
  bucket = aws_s3_bucket.this["logs"].id
  rule {
    id     = "expire-logs"
    status = "Enabled"
    filter {}
    expiration {
      days = var.log_retention_days
    }
    noncurrent_version_expiration {
      noncurrent_days = 35
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
  depends_on = [aws_s3_bucket_versioning.this]
}

# Server access logging of blobs and worm into the logs bucket.
resource "aws_s3_bucket_logging" "this" {
  for_each      = toset(["blobs", "worm"])
  bucket        = aws_s3_bucket.this[each.key].id
  target_bucket = aws_s3_bucket.this["logs"].id
  target_prefix = "s3/${each.key}/"
}

data "aws_elb_service_account" "main" {
  count = var.allow_alb_logs ? 1 : 0
}

data "aws_iam_policy_document" "bucket" {
  for_each = aws_s3_bucket.this

  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [each.value.arn, "${each.value.arn}/*"]
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

  dynamic "statement" {
    for_each = each.key == "logs" ? [1] : []
    content {
      sid       = "S3ServerAccessLogs"
      actions   = ["s3:PutObject"]
      resources = ["${each.value.arn}/s3/*"]
      principals {
        type        = "Service"
        identifiers = ["logging.s3.amazonaws.com"]
      }
      condition {
        test     = "StringEquals"
        variable = "aws:SourceAccount"
        values   = [local.account]
      }
    }
  }

  dynamic "statement" {
    for_each = each.key == "logs" && var.allow_alb_logs ? [1] : []
    content {
      sid       = "AlbAccessLogs"
      actions   = ["s3:PutObject"]
      resources = ["${each.value.arn}/alb/AWSLogs/${local.account}/*"]
      principals {
        type        = "AWS"
        identifiers = [data.aws_elb_service_account.main[0].arn]
      }
    }
  }
}

resource "aws_s3_bucket_policy" "this" {
  for_each = aws_s3_bucket.this
  bucket   = each.value.id
  policy   = data.aws_iam_policy_document.bucket[each.key].json
  depends_on = [
    aws_s3_bucket_public_access_block.this,
  ]
}

# Cross-region replication (production DR). Deletes of blobs are replicated; worm delete markers are not.
data "aws_iam_policy_document" "replication_assume" {
  count = var.replication == null ? 0 : 1
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["s3.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account]
    }
  }
}

data "aws_iam_policy_document" "replication" {
  count = var.replication == null ? 0 : 1
  statement {
    actions   = ["s3:GetReplicationConfiguration", "s3:ListBucket"]
    resources = [aws_s3_bucket.this["blobs"].arn, aws_s3_bucket.this["worm"].arn]
  }
  statement {
    actions = [
      "s3:GetObjectVersionForReplication", "s3:GetObjectVersionAcl", "s3:GetObjectVersionTagging",
      "s3:GetObjectRetention", "s3:GetObjectLegalHold",
    ]
    resources = ["${aws_s3_bucket.this["blobs"].arn}/*", "${aws_s3_bucket.this["worm"].arn}/*"]
  }
  statement {
    actions = ["s3:ReplicateObject", "s3:ReplicateDelete", "s3:ReplicateTags", "s3:ObjectOwnerOverrideToOwner"]
    resources = [
      "${var.replication.blobs_bucket_arn}/*",
      "${var.replication.worm_bucket_arn}/*",
    ]
  }
  statement {
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = [var.kms_key_arn]
  }
  statement {
    actions   = ["kms:Encrypt", "kms:GenerateDataKey", "kms:DescribeKey"]
    resources = [var.replication.kms_key_arn]
  }
}

resource "aws_iam_role" "replication" {
  count              = var.replication == null ? 0 : 1
  name_prefix        = "${var.name}-s3-repl-"
  assume_role_policy = data.aws_iam_policy_document.replication_assume[0].json
}

resource "aws_iam_role_policy" "replication" {
  count  = var.replication == null ? 0 : 1
  name   = "replicate"
  role   = aws_iam_role.replication[0].id
  policy = data.aws_iam_policy_document.replication[0].json
}

resource "aws_s3_bucket_replication_configuration" "this" {
  for_each = var.replication == null ? toset([]) : toset(["blobs", "worm"])
  bucket   = aws_s3_bucket.this[each.key].id
  role     = aws_iam_role.replication[0].arn
  rule {
    id       = "to-dr-${each.key}"
    status   = "Enabled"
    priority = 1
    filter {}
    delete_marker_replication {
      status = each.key == "blobs" ? "Enabled" : "Disabled"
    }
    source_selection_criteria {
      sse_kms_encrypted_objects {
        status = "Enabled"
      }
    }
    destination {
      bucket        = each.key == "blobs" ? var.replication.blobs_bucket_arn : var.replication.worm_bucket_arn
      storage_class = "STANDARD"
      encryption_configuration {
        replica_kms_key_id = var.replication.kms_key_arn
      }
    }
  }
  depends_on = [aws_s3_bucket_versioning.this, aws_iam_role_policy.replication]
}

# Outputs wait for the settings other resources rely on: replication and the ALB must not be
# created before versioning, Object Lock and the bucket policy exist.
output "blobs_bucket_arn" {
  value      = aws_s3_bucket.this["blobs"].arn
  depends_on = [aws_s3_bucket_versioning.this, aws_s3_bucket_public_access_block.this]
}
output "blobs_bucket_name" { value = aws_s3_bucket.this["blobs"].id }
output "worm_bucket_arn" {
  value      = aws_s3_bucket.this["worm"].arn
  depends_on = [aws_s3_bucket_versioning.this, aws_s3_bucket_object_lock_configuration.worm, aws_s3_bucket_public_access_block.this]
}
output "worm_bucket_name" { value = aws_s3_bucket.this["worm"].id }
output "logs_bucket_arn" { value = aws_s3_bucket.this["logs"].arn }
output "logs_bucket_name" {
  description = "Available only after the bucket policy that lets the load balancer write logs."
  value       = aws_s3_bucket.this["logs"].id
  depends_on  = [aws_s3_bucket_policy.this]
}
