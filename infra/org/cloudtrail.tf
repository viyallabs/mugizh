# Organisation CloudTrail (K-SEC-01, R3): all regions, management events (IAM and KMS included),
# log file validation on. Logs go to a dedicated bucket in ap-south-1 with Object Lock
# (COMPLIANCE, 365 days) and a customer-managed KMS key, so no account can erase the audit
# trail. Apply from the management account. [VALIDATE] retention against RET-07 / CERT-In.

variable "trail_retention_days" {
  description = "Object Lock retention of the trail bucket in COMPLIANCE mode (irreversible). 365 days matches K-SEC-01 as proposed [CONFIRM]."
  type        = number
  default     = 365
}

data "aws_caller_identity" "current" {}
data "aws_organizations_organization" "this" {}

locals {
  mgmt_account = data.aws_caller_identity.current.account_id
  org_id       = data.aws_organizations_organization.this.id
  trail_name   = "mugizh-org-trail"
  trail_arn    = "arn:aws:cloudtrail:ap-south-1:${local.mgmt_account}:trail/${local.trail_name}"
}

data "aws_iam_policy_document" "trail_key" {
  statement {
    sid       = "EnableIAMPolicies"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${local.mgmt_account}:root"]
    }
  }
  statement {
    sid       = "CloudTrailEncrypt"
    actions   = ["kms:GenerateDataKey*", "kms:DescribeKey"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceArn"
      values   = [local.trail_arn]
    }
    condition {
      test     = "StringLike"
      variable = "kms:EncryptionContext:aws:cloudtrail:arn"
      values   = ["arn:aws:cloudtrail:*:${local.mgmt_account}:trail/*"]
    }
  }
}

resource "aws_kms_key" "trail" {
  description             = "Organisation CloudTrail logs"
  enable_key_rotation     = true
  rotation_period_in_days = 365
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.trail_key.json
}

resource "aws_kms_alias" "trail" {
  name          = "alias/mugizh-org-trail"
  target_key_id = aws_kms_key.trail.key_id
}

resource "aws_s3_bucket" "trail" {
  bucket              = "mugizh-org-trail-${local.mgmt_account}-ap-south-1"
  object_lock_enabled = true
  force_destroy       = false
}

resource "aws_s3_bucket_public_access_block" "trail" {
  bucket                  = aws_s3_bucket.trail.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "trail" {
  bucket = aws_s3_bucket.trail.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "trail" {
  bucket = aws_s3_bucket.trail.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "trail" {
  bucket = aws_s3_bucket.trail.id
  rule {
    bucket_key_enabled = true
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.trail.arn
    }
  }
}

resource "aws_s3_bucket_object_lock_configuration" "trail" {
  bucket = aws_s3_bucket.trail.id
  rule {
    default_retention {
      mode = "COMPLIANCE"
      days = var.trail_retention_days
    }
  }
  depends_on = [aws_s3_bucket_versioning.trail]
}

resource "aws_s3_bucket_lifecycle_configuration" "trail" {
  bucket = aws_s3_bucket.trail.id
  rule {
    id     = "expire-after-retention"
    status = "Enabled"
    filter {}
    expiration {
      days = var.trail_retention_days + 30
    }
    noncurrent_version_expiration {
      noncurrent_days = var.trail_retention_days + 30
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
  depends_on = [aws_s3_bucket_versioning.trail]
}

data "aws_iam_policy_document" "trail_bucket" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.trail.arn, "${aws_s3_bucket.trail.arn}/*"]
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
  statement {
    sid       = "CloudTrailAclCheck"
    actions   = ["s3:GetBucketAcl"]
    resources = [aws_s3_bucket.trail.arn]
    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceArn"
      values   = [local.trail_arn]
    }
  }
  statement {
    sid     = "CloudTrailWrite"
    actions = ["s3:PutObject"]
    resources = [
      "${aws_s3_bucket.trail.arn}/AWSLogs/${local.mgmt_account}/*",
      "${aws_s3_bucket.trail.arn}/AWSLogs/${local.org_id}/*",
    ]
    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceArn"
      values   = [local.trail_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["bucket-owner-full-control"]
    }
  }
}

resource "aws_s3_bucket_policy" "trail" {
  bucket     = aws_s3_bucket.trail.id
  policy     = data.aws_iam_policy_document.trail_bucket.json
  depends_on = [aws_s3_bucket_public_access_block.trail]
}

resource "aws_cloudtrail" "org" {
  name                          = local.trail_name
  s3_bucket_name                = aws_s3_bucket.trail.id
  kms_key_id                    = aws_kms_key.trail.arn
  is_organization_trail         = true
  is_multi_region_trail         = true
  include_global_service_events = true
  enable_log_file_validation    = true

  # All management events, read and write. KMS and IAM calls are management events.
  event_selector {
    read_write_type           = "All"
    include_management_events = true
  }

  depends_on = [aws_s3_bucket_policy.trail]
}

output "trail_bucket" { value = aws_s3_bucket.trail.id }
