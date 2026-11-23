# Compute: ECS on Fargate. Services api, worker, scheduler-core, and a one-off migrate task
# definition. Tasks run in the private app subnets. Secrets are injected from Secrets Manager
# (the secrets are created EMPTY here; values are written by the migrate job or by operators,
# never by OpenTofu, K-SEC-05).
#
# PRODUCTION IMAGES MUST RUN AS NON-ROOT (R4-5). The task definitions also force user
# 10001:10001, a read-only root filesystem, a writable /tmp volume, and drop all capabilities.
# NOTE: the Go start-up currently refuses APP_ENV=staging|production until the cloud KMS and
# S3 adapters exist (S-093/S-098), so these tasks will not become healthy before then.

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
  type = string
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

variable "api_target_group_arn" {
  type = string
}

variable "secret_name_prefix" {
  description = "Prefix of the Secrets Manager secret names, for example mugizh/staging."
  type        = string
}

variable "image_tag" {
  description = "Image tag to deploy [FILL]. Repositories are immutable, so use a release tag or digest, never latest."
  type        = string
  validation {
    condition     = var.image_tag != "latest"
    error_message = "Do not deploy the latest tag."
  }
}

variable "db_modules" {
  description = "Core API modules, one login role and one MUGIZH_DB_URL_<MODULE> secret each. Keep equal to db.Modules in backend/internal/platform/db/db.go."
  type        = list(string)
  default = [
    "admin", "audit", "booking", "clinical", "consult", "directory", "identity",
    "notifications", "payments", "platform", "records", "reviews", "verification",
  ]
}

variable "pack_dir" {
  description = "MUGIZH_PACK_DIR inside the image."
  type        = string
  default     = "/app/config/country-packs/IN"
}

variable "vendor_register" {
  description = "MUGIZH_VENDOR_REGISTER inside the image."
  type        = string
  default     = "/app/config/vendor-register.yaml"
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
  description = "Fargate cpu units and memory MiB per service."
  type        = map(object({ cpu = number, memory = number }))
  default = {
    api            = { cpu = 512, memory = 1024 }
    worker         = { cpu = 512, memory = 1024 }
    scheduler-core = { cpu = 256, memory = 512 }
    migrate        = { cpu = 256, memory = 512 }
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
  repositories   = ["api", "worker", "scheduler-core", "migrate"]

  db_url_vars = [for m in var.db_modules : "MUGIZH_DB_URL_${upper(m)}"]
  shared_vars = ["MUGIZH_KEYSTORE_URL", "MUGIZH_BLIND_INDEX_KEY", "MUGIZH_SCHEDULER_CALLBACK_SECRET", "MUGIZH_SCHEDULER_TOKEN"]
  all_vars = toset(concat(
    local.db_url_vars, local.shared_vars,
    ["MUGIZH_DB_URL_QUEUE", "MUGIZH_SCHEDULER_DB_URL", "MUGIZH_DB_URL_OWNER", "MUGIZH_KEYSTORE_OWNER_URL"],
  ))

  common_env = merge(
    {
      APP_ENV                = var.app_env
      HTTP_ADDR              = ":${local.api_port}"
      TRUSTED_PROXIES        = "1" # one hop: the ALB (settings reject 0 or unset outside dev)
      MUGIZH_PACK_DIR        = var.pack_dir
      MUGIZH_SCHEDULER_URL   = "http://scheduler-core:${local.scheduler_port}"
      MUGIZH_VENDOR_REGISTER = var.vendor_register
    },
    var.otlp_endpoint == "" ? {} : { MUGIZH_OTLP_ENDPOINT = var.otlp_endpoint },
  )

  tasks = {
    api = {
      secrets     = concat(local.db_url_vars, local.shared_vars)
      data_access = true
      service     = true
      port        = local.api_port
      env         = local.common_env
      command     = null
    }
    worker = {
      secrets     = concat(local.db_url_vars, local.shared_vars, ["MUGIZH_DB_URL_QUEUE"])
      data_access = true
      service     = true
      port        = null
      env         = merge(local.common_env, { HTTP_ADDR = ":8082" })
      command     = null
    }
    scheduler-core = {
      # scheduler-core holds no health data (K-ARC-02): no KMS, no S3, its own database.
      secrets     = ["MUGIZH_SCHEDULER_DB_URL", "MUGIZH_SCHEDULER_TOKEN", "MUGIZH_SCHEDULER_CALLBACK_SECRET"]
      data_access = false
      service     = true
      port        = local.scheduler_port
      env         = { APP_ENV = var.app_env, HTTP_ADDR = ":${local.scheduler_port}", TRUSTED_PROXIES = "1" }
      command     = null
    }
    migrate = {
      # One-off. Runs migrations and creates the per-module login roles (as deploy/local/up.sh does).
      secrets     = ["MUGIZH_DB_URL_OWNER", "MUGIZH_KEYSTORE_OWNER_URL"]
      data_access = false
      service     = false
      port        = null
      env         = { APP_ENV = var.app_env }
      command     = ["up"]
    }
  }
  services = { for k, t in local.tasks : k => t if t.service }
  desired = {
    api            = var.desired_count.api
    worker         = var.desired_count.worker
    scheduler-core = var.desired_count.scheduler_core
  }
}

# ---------- Secrets (empty shells) ----------
resource "aws_secretsmanager_secret" "app" {
  for_each                = local.all_vars
  name                    = "${var.secret_name_prefix}/${lower(each.key)}"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = 30
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

  statement {
    sid       = "DataKey"
    actions   = ["kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey", "kms:DescribeKey"]
    resources = [var.kms_key_arn]
  }
  statement {
    sid       = "BlobsReadWrite"
    actions   = ["s3:GetObject", "s3:GetObjectVersion", "s3:PutObject"]
    resources = ["${var.blobs_bucket_arn}/*"]
  }
  statement {
    sid       = "WormPutGetOnly"
    actions   = ["s3:GetObject", "s3:GetObjectVersion", "s3:PutObject"]
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
    resources = [aws_ecr_repository.this[each.key].arn]
  }
  statement {
    sid       = "OwnLogs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.task[each.key].arn}:*"]
  }
  statement {
    sid       = "OwnSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [for v in each.value.secrets : aws_secretsmanager_secret.app[v].arn]
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
        image                  = "${aws_ecr_repository.this[each.key].repository_url}:${var.image_tag}"
        essential              = true
        user                   = "10001:10001"
        readonlyRootFilesystem = true
        privileged             = false
        command                = each.value.command
        linuxParameters = {
          initProcessEnabled = true
          capabilities       = { drop = ["ALL"] }
        }
        mountPoints = [{ sourceVolume = "tmp", containerPath = "/tmp", readOnly = false }]
        environment = [for k, v in each.value.env : { name = k, value = v }]
        secrets     = [for v in each.value.secrets : { name = v, valueFrom = aws_secretsmanager_secret.app[v].arn }]
        logConfiguration = {
          logDriver = "awslogs"
          options = {
            "awslogs-group"         = aws_cloudwatch_log_group.task[each.key].name
            "awslogs-region"        = data.aws_region.current.name
            "awslogs-stream-prefix" = each.key
          }
        }
      },
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
    security_groups  = [var.app_security_group_id]
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
output "migrate_task_family" { value = aws_ecs_task_definition.this["migrate"].family }
output "task_role_arns" { value = [for r in aws_iam_role.task : r.arn] }
output "exec_role_arns" { value = [for r in aws_iam_role.exec : r.arn] }
output "ecr_repository_arns" { value = [for r in aws_ecr_repository.this : r.arn] }
output "log_group_names" { value = { for k, g in aws_cloudwatch_log_group.task : k => g.name } }
output "log_group_arns" { value = { for k, g in aws_cloudwatch_log_group.task : k => g.arn } }
output "secret_names" {
  description = "Secrets Manager secrets created empty. Populate them out of band."
  value       = sort([for s in aws_secretsmanager_secret.app : s.name])
}
