# Compute: ECS on Fargate. Services api, worker and scheduler-core, plus two one-off task
# definitions: migrate (core database and key store) and scheduler-migrate (scheduler-core
# database). Tasks run in the private app subnets. Secrets are injected from Secrets Manager
# (the secrets are created EMPTY here; values are written by the migrate jobs or by operators,
# never by OpenTofu, K-SEC-05).
#
# Images (deploy/images/*.Dockerfile): ONE core image holds /app/api, /app/worker and
# /app/migrate (the same image per release); scheduler-core has its own image with
# ENTRYPOINT /app/scheduler-core. The task definitions set entryPoint explicitly.
# PRODUCTION IMAGES MUST RUN AS NON-ROOT (R4-5): the images use 65532:65532 and the task
# definitions force the same user, a read-only root filesystem, a writable /tmp volume, and
# drop all capabilities.
# NOTE: the Go start-up in staging uses the AWS KMS and S3 adapters (this module passes their
# settings) with the vendor fakes (adapter rule A10); production still refuses to start until
# the SMS, payment and video adapters exist.

variable "name" {
  description = "Name prefix, for example mugizh-staging."
  type        = string
}

variable "app_env" {
  description = "Value of APP_ENV: staging or production."
  type        = string
  validation {
    condition     = contains(["staging", "production"], var.app_env)
    error_message = "app_env must be staging or production."
  }
}

variable "app_subnet_ids" {
  type = list(string)
}

variable "app_security_group_id" {
  description = "Security group of the core tasks (api, worker, migrate)."
  type        = string
}

variable "scheduler_security_group_id" {
  description = "Security group of the scheduler-core tasks. It can reach only the core database port and HTTPS."
  type        = string
}

variable "kms_key_arn" {
  description = "Data key: log groups, secrets, ECR, and the application's wrapping of data keys."
  type        = string
}

variable "blobs_bucket_arn" {
  type = string
}

variable "worm_bucket_arn" {
  type = string
}

variable "blobs_bucket_name" {
  description = "MUGIZH_BLOB_BUCKET: the versioned data bucket."
  type        = string
}

variable "worm_bucket_name" {
  description = "MUGIZH_WORM_BUCKET: the Object Lock bucket for write-once evidence."
  type        = string
}

variable "allowed_regions" {
  description = "MUGIZH_ALLOWED_REGIONS: the residency regions the application accepts for AWS_REGION."
  type        = list(string)
  validation {
    condition     = length(var.allowed_regions) > 0
    error_message = "allowed_regions must not be empty."
  }
}

variable "api_target_group_arn" {
  type = string
}

variable "api_public_url" {
  description = "Public HTTPS base URL of the API, for example https://api.example.in [FILL]. scheduler-core calls back to it, through the ALB."
  type        = string
  validation {
    condition     = startswith(var.api_public_url, "https://") && !endswith(var.api_public_url, "/")
    error_message = "api_public_url must start with https:// and have no trailing slash."
  }
}

variable "secret_name_prefix" {
  description = "Prefix of the Secrets Manager secret names, for example mugizh/staging."
  type        = string
}

variable "secret_replica_region" {
  description = "Replicate the secrets to this region (production DR). Null for none."
  type        = string
  default     = null
}

variable "secret_replica_kms_key_arn" {
  description = "Key in the replica region (the data key replica)."
  type        = string
  default     = null
}

variable "ecr_replication_region" {
  description = "Replicate the ECR repositories to this region (production DR). The destination repositories must exist already (KMS-encrypted). Null for none."
  type        = string
  default     = null
}

variable "image_tag" {
  description = "Core image tag to deploy [FILL]. Repositories are immutable, so use a release tag or digest, never latest."
  type        = string
  validation {
    condition     = var.image_tag != "latest"
    error_message = "Do not deploy the latest tag."
  }
}

variable "scheduler_image_tag" {
  description = "scheduler-core image tag. Defaults to image_tag."
  type        = string
  default     = null
}

variable "db_modules" {
  description = "Core API modules, one login role and one MUGIZH_DB_URL_<MODULE> secret each. Keep equal to db.Modules in backend/internal/platform/db/db.go."
  type        = list(string)
  default = [
    "admin", "audit", "booking", "clinical", "consult", "directory", "identity",
    "notifications", "payments", "platform", "records", "reviews", "verification",
  ]
}

variable "otlp_endpoint" {
  description = "MUGIZH_OTLP_ENDPOINT: an India-hosted collector (R3) [FILL]. Empty leaves it unset."
  type        = string
  default     = ""
}

variable "desired_count" {
  type = object({
    api            = number
    worker         = number
    scheduler_core = number
  })
  default = { api = 2, worker = 1, scheduler_core = 1 }
}

variable "task_size" {
  description = "Fargate cpu units and memory MiB per task."
  type        = map(object({ cpu = number, memory = number }))
  default = {
    api               = { cpu = 512, memory = 1024 }
    worker            = { cpu = 512, memory = 1024 }
    scheduler-core    = { cpu = 256, memory = 512 }
    migrate           = { cpu = 256, memory = 512 }
    scheduler-migrate = { cpu = 256, memory = 512 }
  }
}

variable "cpu_architecture" {
  type    = string
  default = "X86_64"
}

variable "log_retention_days" {
  description = "K-SEC-01, RET-07."
  type        = number
  default     = 365
}

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

locals {
  api_port       = 8080
  scheduler_port = 8081
  repositories   = ["core", "scheduler-core"]
  user           = "65532:65532"

  # Secret keys are shared where one secret value feeds two variables (the callback HMAC
  # secret is the same value in core and in scheduler-core).
  core_secrets = merge(
    { for m in var.db_modules : "MUGIZH_DB_URL_${upper(m)}" => "mugizh_db_url_${m}" },
    {
      MUGIZH_KEYSTORE_URL              = "mugizh_keystore_url"
      MUGIZH_BLIND_INDEX_KEY           = "mugizh_blind_index_key"
      MUGIZH_SCHEDULER_CALLBACK_SECRET = "scheduler_callback_secret"
      MUGIZH_SCHEDULER_TOKEN           = "scheduler_service_token"
    },
  )

  core_env = merge(
    {
      APP_ENV              = var.app_env
      HTTP_ADDR            = ":${local.api_port}"
      TRUSTED_PROXIES      = "1" # one hop: the ALB (settings reject 0 or unset outside dev)
      MUGIZH_SCHEDULER_URL = "http://scheduler-core:${local.scheduler_port}"
      # Key management and storage (adapters awskms and s3store); the task role grants exactly this access.
      MUGIZH_KEY_PROVIDER    = "aws-kms"
      MUGIZH_KMS_KEY_ID      = var.kms_key_arn
      AWS_REGION             = data.aws_region.current.name
      MUGIZH_ALLOWED_REGIONS = join(",", var.allowed_regions)
      MUGIZH_BLOB_STORE      = "s3"
      MUGIZH_BLOB_BUCKET     = var.blobs_bucket_name
      MUGIZH_WORM_BUCKET     = var.worm_bucket_name
    },
    var.otlp_endpoint == "" ? {} : { MUGIZH_OTLP_ENDPOINT = var.otlp_endpoint },
  )

  # The country pack and vendor register paths are baked into the image (ENV in the Dockerfile).
  tasks = {
    api = {
      repo        = "core"
      entry       = ["/app/api"]
      command     = null
      secrets     = local.core_secrets
      env         = local.core_env
      data_access = true
      service     = true
      port        = local.api_port
      sg          = var.app_security_group_id
    }
    worker = {
      repo        = "core"
      entry       = ["/app/worker"]
      command     = null
      secrets     = merge(local.core_secrets, { MUGIZH_DB_URL_QUEUE = "mugizh_db_url_queue" })
      env         = merge(local.core_env, { HTTP_ADDR = ":8082" })
      data_access = true
      service     = true
      port        = null
      sg          = var.app_security_group_id
    }
    scheduler-core = {
      # scheduler-core holds no health data (K-ARC-02): no KMS, no S3, its own database.
      repo    = "scheduler-core"
      entry   = ["/app/scheduler-core"]
      command = null
      secrets = {
        SCHEDULER_DATABASE_URL         = "scheduler_database_url"
        SCHEDULER_CALLBACK_HMAC_SECRET = "scheduler_callback_secret"
        SCHEDULER_SERVICE_TOKEN_SHA256 = "scheduler_service_token_sha256"
      }
      env = {
        SCHEDULER_LISTEN_ADDR  = ":${local.scheduler_port}"
        SCHEDULER_CALLBACK_URL = "${var.api_public_url}/internal/v1/scheduler/occurrence-due"
      }
      data_access = false
      service     = true
      port        = local.scheduler_port
      sg          = var.scheduler_security_group_id
    }
    migrate = {
      # One-off: core migrations and the per-module login roles (as deploy/local/up.sh does).
      repo        = "core"
      entry       = ["/app/migrate"]
      command     = ["up"]
      secrets     = { MUGIZH_DB_URL_OWNER = "mugizh_db_url_owner", MUGIZH_KEYSTORE_OWNER_URL = "mugizh_keystore_owner_url" }
      env         = { APP_ENV = var.app_env }
      data_access = false
      service     = false
      port        = null
      sg          = var.app_security_group_id
    }
    scheduler-migrate = {
      # One-off: scheduler-core migrations (subcommand "migrate", with the migration owner's DSN).
      repo        = "scheduler-core"
      entry       = ["/app/scheduler-core"]
      command     = ["migrate"]
      secrets     = { SCHEDULER_MIGRATE_DATABASE_URL = "scheduler_migrate_database_url" }
      env         = {}
      data_access = false
      service     = false
      port        = null
      sg          = var.scheduler_security_group_id
    }
  }
  services = { for k, t in local.tasks : k => t if t.service }
  desired = {
    api            = var.desired_count.api
    worker         = var.desired_count.worker
    scheduler-core = var.desired_count.scheduler_core
  }
  secret_keys = toset(flatten([for t in local.tasks : values(t.secrets)]))
  image_tags = {
    core           = var.image_tag
    scheduler-core = coalesce(var.scheduler_image_tag, var.image_tag)
  }
}

# ---------- Secrets (empty shells) ----------
resource "aws_secretsmanager_secret" "app" {
  for_each                = local.secret_keys
  name                    = "${var.secret_name_prefix}/${each.key}"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = 30

  dynamic "replica" {
    for_each = var.secret_replica_region == null ? [] : [1]
    content {
      region     = var.secret_replica_region
      kms_key_id = var.secret_replica_kms_key_arn
    }
  }
}

# ---------- ECR ----------
resource "aws_ecr_repository" "this" {
  for_each             = toset(local.repositories)
  name                 = "${var.name}/${each.key}"
  image_tag_mutability = "IMMUTABLE"
  image_scanning_configuration {
    scan_on_push = true
  }
  encryption_configuration {
    encryption_type = "KMS"
    kms_key         = var.kms_key_arn
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each   = aws_ecr_repository.this
  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire untagged images after 14 days"
      selection    = { tagStatus = "untagged", countType = "sinceImagePushed", countUnit = "days", countNumber = 14 }
      action       = { type = "expire" }
    }]
  })
}

# DR: replicate images to the second India region. The destination repositories are created
# beforehand (KMS-encrypted, immutable) by the production environment.
resource "aws_ecr_replication_configuration" "dr" {
  count = var.ecr_replication_region == null ? 0 : 1
  replication_configuration {
    rule {
      destination {
        region      = var.ecr_replication_region
        registry_id = data.aws_caller_identity.current.account_id
      }
      repository_filter {
        filter      = "${var.name}/"
        filter_type = "PREFIX_MATCH"
      }
    }
  }
}

# ---------- Logs ----------
resource "aws_cloudwatch_log_group" "task" {
  for_each          = local.tasks
  name              = "/mugizh/${var.app_env}/${each.key}"
  retention_in_days = var.log_retention_days
  kms_key_id        = var.kms_key_arn
}

# ---------- Cluster ----------
resource "aws_ecs_cluster" "this" {
  name = var.name
  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_service_discovery_http_namespace" "this" {
  name        = "${var.name}.internal"
  description = "ECS Service Connect namespace (api and worker reach scheduler-core)"
}

# ---------- IAM ----------
data "aws_iam_policy_document" "tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_iam_role" "task" {
  for_each           = local.tasks
  name_prefix        = "${var.name}-${each.key}-task-"
  assume_role_policy = data.aws_iam_policy_document.tasks_assume.json
}

resource "aws_iam_role" "exec" {
  for_each           = local.tasks
  name_prefix        = "${var.name}-${each.key}-exec-"
  assume_role_policy = data.aws_iam_policy_document.tasks_assume.json
}

# Task roles: api and worker need the data key and blobs/worm. Nothing can delete from worm.
data "aws_iam_policy_document" "task" {
  for_each = { for k, t in local.tasks : k => t if t.data_access }

  # The application wraps its data keys with kms:Encrypt/Decrypt, always with the "mugizh:aad"
  # encryption context (internal/adapters/keys/awskms); a call without it is not allowed.
  statement {
    sid       = "DataKeyWrap"
    actions   = ["kms:Encrypt", "kms:Decrypt"]
    resources = [var.kms_key_arn]
    condition {
      test     = "Null"
      variable = "kms:EncryptionContextKeys"
      values   = ["false"]
    }
    condition {
      test     = "ForAllValues:StringEquals"
      variable = "kms:EncryptionContextKeys"
      values   = ["mugizh:aad"]
    }
  }
  # S3 default encryption (SSE-KMS) uses the caller's permission, only through S3.
  statement {
    sid       = "DataKeyViaS3"
    actions   = ["kms:Decrypt", "kms:GenerateDataKey"]
    resources = [var.kms_key_arn]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["s3.${data.aws_region.current.name}.amazonaws.com"]
    }
  }
  # Erasure and the prohibited-identifier purge delete from the blobs bucket (versioned; old
  # versions expire after 35 days, so crypto-shredding still covers them). Never from worm.
  statement {
    sid       = "BlobsReadWriteDelete"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${var.blobs_bucket_arn}/*"]
  }
  statement {
    sid       = "WormPutGetOnly"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${var.worm_bucket_arn}/*"]
  }
  statement {
    sid       = "List"
    actions   = ["s3:ListBucket"]
    resources = [var.blobs_bucket_arn, var.worm_bucket_arn]
  }
}

resource "aws_iam_role_policy" "task" {
  for_each = data.aws_iam_policy_document.task
  name     = "data-access"
  role     = aws_iam_role.task[each.key].id
  policy   = each.value.json
}

# Execution roles: pull their own image, write their own log group, read only their own secrets.
data "aws_iam_policy_document" "exec" {
  for_each = local.tasks

  # GetAuthorizationToken does not support resource-level permissions.
  #trivy:ignore:AVD-AWS-0057
  statement {
    sid       = "EcrToken"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
  statement {
    sid       = "PullOwnImage"
    actions   = ["ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"]
    resources = [aws_ecr_repository.this[each.value.repo].arn]
  }
  statement {
    sid       = "OwnLogs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.task[each.key].arn}:*"]
  }
  statement {
    sid       = "OwnSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [for k in distinct(values(each.value.secrets)) : aws_secretsmanager_secret.app[k].arn]
  }
  statement {
    sid       = "DecryptSecrets"
    actions   = ["kms:Decrypt"]
    resources = [var.kms_key_arn]
  }
}

resource "aws_iam_role_policy" "exec" {
  for_each = local.tasks
  name     = "execution"
  role     = aws_iam_role.exec[each.key].id
  policy   = data.aws_iam_policy_document.exec[each.key].json
}

# ---------- Task definitions ----------
resource "aws_ecs_task_definition" "this" {
  for_each                 = local.tasks
  family                   = "${var.name}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_size[each.key].cpu
  memory                   = var.task_size[each.key].memory
  task_role_arn            = aws_iam_role.task[each.key].arn
  execution_role_arn       = aws_iam_role.exec[each.key].arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  volume {
    name = "tmp"
  }

  container_definitions = jsonencode([
    merge(
      {
        name                   = each.key
        image                  = "${aws_ecr_repository.this[each.value.repo].repository_url}:${local.image_tags[each.value.repo]}"
        essential              = true
        user                   = local.user
        readonlyRootFilesystem = true
        privileged             = false
        entryPoint             = each.value.entry
        linuxParameters = {
          initProcessEnabled = true
          capabilities       = { drop = ["ALL"] }
        }
        mountPoints = [{ sourceVolume = "tmp", containerPath = "/tmp", readOnly = false }]
        environment = [for k, v in each.value.env : { name = k, value = v }]
        secrets     = [for k, v in each.value.secrets : { name = k, valueFrom = aws_secretsmanager_secret.app[v].arn }]
        logConfiguration = {
          logDriver = "awslogs"
          options = {
            "awslogs-group"         = aws_cloudwatch_log_group.task[each.key].name
            "awslogs-region"        = data.aws_region.current.name
            "awslogs-stream-prefix" = each.key
          }
        }
      },
      each.value.command == null ? {} : { command = each.value.command },
      each.value.port == null ? {} : {
        portMappings = [{ name = "http", containerPort = each.value.port, protocol = "tcp", appProtocol = "http" }]
      },
    )
  ])
}

# ---------- Services ----------
resource "aws_ecs_service" "this" {
  for_each                           = local.services
  name                               = each.key
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.this[each.key].arn
  desired_count                      = local.desired[each.key]
  launch_type                        = "FARGATE"
  enable_execute_command             = false
  propagate_tags                     = "SERVICE"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = each.key == "api" ? 60 : null

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.app_subnet_ids
    security_groups  = [each.value.sg]
    assign_public_ip = false
  }

  dynamic "load_balancer" {
    for_each = each.key == "api" ? [1] : []
    content {
      target_group_arn = var.api_target_group_arn
      container_name   = "api"
      container_port   = local.api_port
    }
  }

  service_connect_configuration {
    enabled   = true
    namespace = aws_service_discovery_http_namespace.this.arn
    dynamic "service" {
      for_each = each.key == "scheduler-core" ? [1] : []
      content {
        port_name      = "http"
        discovery_name = "scheduler-core"
        client_alias {
          port     = local.scheduler_port
          dns_name = "scheduler-core"
        }
      }
    }
  }

  # CI deploys new task definitions; do not fight it.
  lifecycle {
    ignore_changes = [task_definition]
  }
}

output "cluster_arn" { value = aws_ecs_cluster.this.arn }
output "cluster_name" { value = aws_ecs_cluster.this.name }
output "service_names" { value = { for k, s in aws_ecs_service.this : k => s.name } }
output "service_arns" { value = [for s in aws_ecs_service.this : s.id] }
output "migrate_task_families" {
  description = "One-off task definition families the deploy role may run."
  value       = [aws_ecs_task_definition.this["migrate"].family, aws_ecs_task_definition.this["scheduler-migrate"].family]
}
output "task_families" {
  description = "Families of all task definitions (the deploy role registers new revisions of these)."
  value       = [for t in aws_ecs_task_definition.this : t.family]
}
output "task_role_arns" { value = [for r in aws_iam_role.task : r.arn] }
output "exec_role_arns" { value = [for r in aws_iam_role.exec : r.arn] }
output "ecr_repository_arns" { value = [for r in aws_ecr_repository.this : r.arn] }
output "log_group_names" { value = { for k, g in aws_cloudwatch_log_group.task : k => g.name } }
output "log_group_arns" { value = { for k, g in aws_cloudwatch_log_group.task : k => g.arn } }
output "secret_names" {
  description = "Secrets Manager secrets created empty. Populate them out of band."
  value       = sort([for s in aws_secretsmanager_secret.app : s.name])
}
