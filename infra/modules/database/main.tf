# RDS PostgreSQL 17, two separate instances:
#   core     : all module schemas and scheduler-core's own database (ADR-01 option b)
#   keystore : wrapped data keys only (encryption-design section 4), its own KMS key,
#              backups 35 days at most.
# PostGIS is an extension created by the migrations, not by OpenTofu.
# Login roles (one per module: mugizh_app_<module>, plus queue and keystore) are NOT created
# here. The migrate task creates them, as deploy/local/up.sh does locally, and stores the
# connection strings in Secrets Manager. Passwords never pass through OpenTofu state
# (master passwords are managed by RDS, K-SEC-05).

variable "name" {
  description = "Name prefix, for example mugizh-production."
  type        = string
}

variable "vpc_id" {
  type = string
}

variable "data_subnet_ids" {
  type = list(string)
}

variable "client_security_group_ids" {
  description = "Security groups allowed on 5432, per instance, keyed by a static label (keys must be known at plan time). The key store admits core tasks only (not scheduler-core)."
  type = object({
    core     = map(string)
    keystore = map(string)
  })
}

variable "final_snapshot_suffix" {
  description = "Suffix of the core database final snapshot name. The snapshot holds field-encrypted data; delete it by hand within 35 days of decommissioning (K-DP-07). The key-store instance takes NO final snapshot."
  type        = string
  default     = "final"
}

variable "kms_key_arn" {
  description = "Data key (core database, Performance Insights, master secret, log groups)."
  type        = string
}

variable "keystore_kms_key_arn" {
  description = "Separate key for the key-store database and its backups."
  type        = string
}

variable "multi_az" {
  type    = bool
  default = false
}

variable "instance_class" {
  type    = string
  default = "db.t4g.medium"
}

variable "allocated_storage_gb" {
  type    = number
  default = 50
}

variable "max_allocated_storage_gb" {
  type    = number
  default = 200
}

variable "engine_version" {
  description = "PostgreSQL major version. Must match dev and CI (dual-cloud rules)."
  type        = string
  default     = "17"
}

variable "backup_retention_days" {
  description = "Automated backups with point-in-time recovery (RET-17). RDS maximum is 35, which is also the key-store limit."
  type        = number
  default     = 35
  validation {
    condition     = var.backup_retention_days >= 1 && var.backup_retention_days <= 35
    error_message = "Backup retention must be 1 to 35 days (key-store backups never exceed 35)."
  }
}

variable "deletion_protection" {
  type    = bool
  default = true
}

variable "log_retention_days" {
  type    = number
  default = 365
}

variable "enable_backup_replication" {
  description = "Replicate automated backups to the DR region (production)."
  type        = bool
  default     = false
}

variable "dr_kms_key_arn" {
  description = "Replica of the data key in the DR region. Needed when enable_backup_replication is true."
  type        = string
  default     = null
}

variable "dr_keystore_kms_key_arn" {
  description = "Replica of the key-store backup key in the DR region. Needed when enable_backup_replication is true."
  type        = string
  default     = null
}

locals {
  instances = {
    core = {
      db_name = "mugizh"
      kms     = var.kms_key_arn
      dr_kms  = var.dr_kms_key_arn
    }
    keystore = {
      db_name = "mugizh_keystore"
      kms     = var.keystore_kms_key_arn
      dr_kms  = var.dr_keystore_kms_key_arn
    }
  }
}

resource "aws_db_subnet_group" "this" {
  name       = "${var.name}-data"
  subnet_ids = var.data_subnet_ids
}

resource "aws_security_group" "db" {
  for_each    = local.instances
  name_prefix = "${var.name}-${each.key}-db-"
  description = "${each.key} database: PostgreSQL from the app tier only"
  vpc_id      = var.vpc_id
  tags        = { Name = "${var.name}-${each.key}-db" }
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "db" {
  for_each = {
    for p in flatten([for db, sgs in var.client_security_group_ids : [for label, sg in sgs : { key = "${db}-${label}", db = db, sg = sg }]]) : p.key => p
  }
  security_group_id            = aws_security_group.db[each.value.db].id
  description                  = "PostgreSQL from the app tier"
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  referenced_security_group_id = each.value.sg
}

resource "aws_db_parameter_group" "this" {
  name_prefix = "${var.name}-pg17-"
  family      = "postgres17"
  description = "TLS only; connection logging; no statement logging (K-DP-04)"

  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
  parameter {
    name  = "log_connections"
    value = "1"
  }
  parameter {
    name  = "log_disconnections"
    value = "1"
  }
  parameter {
    name  = "log_statement"
    value = "none"
  }
  parameter {
    name  = "log_error_verbosity"
    value = "terse"
  }
  parameter {
    name  = "log_min_error_statement"
    value = "panic"
  }
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_cloudwatch_log_group" "db" {
  for_each          = { for p in setproduct(keys(local.instances), ["postgresql", "upgrade"]) : "${p[0]}-${p[1]}" => { db = p[0], log = p[1] } }
  name              = "/aws/rds/instance/${var.name}-${each.value.db}/${each.value.log}"
  retention_in_days = var.log_retention_days
  kms_key_id        = var.kms_key_arn
}

resource "aws_db_instance" "this" {
  for_each   = local.instances
  identifier = "${var.name}-${each.key}"

  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  db_name  = each.value.db_name
  username = "mugizh_owner"

  # RDS creates and rotates the master password in Secrets Manager. It is never in code or state.
  manage_master_user_password   = true
  master_user_secret_kms_key_id = each.value.kms

  allocated_storage     = var.allocated_storage_gb
  max_allocated_storage = var.max_allocated_storage_gb
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = each.value.kms

  multi_az               = var.multi_az
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db[each.key].id]
  publicly_accessible    = false
  parameter_group_name   = aws_db_parameter_group.this.name

  iam_database_authentication_enabled = false

  backup_retention_period  = var.backup_retention_days
  backup_window            = "20:00-21:00"
  maintenance_window       = "sun:21:30-sun:22:30"
  copy_tags_to_snapshot    = true
  delete_automated_backups = false
  deletion_protection      = var.deletion_protection
  # The key store must not leave a snapshot that outlives the 35-day window (K-DP-07, crypto-shredding).
  skip_final_snapshot       = each.key == "keystore"
  final_snapshot_identifier = each.key == "keystore" ? null : "${var.name}-${each.key}-${var.final_snapshot_suffix}"

  auto_minor_version_upgrade = true

  performance_insights_enabled          = true
  performance_insights_kms_key_id       = each.value.kms
  performance_insights_retention_period = 7

  enabled_cloudwatch_logs_exports = ["postgresql", "upgrade"]

  depends_on = [aws_cloudwatch_log_group.db]
}

# Cross-region automated-backup replication to the DR region (RPO 15 min with PITR).
# Created through the aws.dr provider; encrypted with the DR replica key of each database.
resource "aws_db_instance_automated_backups_replication" "dr" {
  for_each               = var.enable_backup_replication ? local.instances : {}
  provider               = aws.dr
  source_db_instance_arn = aws_db_instance.this[each.key].arn
  kms_key_id             = each.value.dr_kms
  retention_period       = var.backup_retention_days
}

output "endpoints" {
  description = "Host names (no credentials)."
  value       = { for k, i in aws_db_instance.this : k => i.address }
}

output "master_secret_arns" {
  description = "RDS-managed master secrets. Only the migrate job and break-glass may read them."
  value       = { for k, i in aws_db_instance.this : k => i.master_user_secret[0].secret_arn }
}

output "instance_identifiers" {
  value = { for k, i in aws_db_instance.this : k => i.identifier }
}
