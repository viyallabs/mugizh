locals {
  name = "mugizh-staging"
}

module "keys" {
  source         = "../../modules/keys"
  name           = local.name
  key_admin_arns = var.key_admin_arns
}

module "network" {
  source                 = "../../modules/network"
  name                   = local.name
  azs                    = var.azs
  nat_per_az             = false
  kms_key_arn            = module.keys.data_key_arn
  enable_egress_firewall = var.enable_egress_firewall
  egress_allowed_domains = var.egress_allowed_domains
}

module "storage" {
  source              = "../../modules/storage"
  name                = local.name
  kms_key_arn         = module.keys.data_key_arn
  worm_retention_days = var.worm_retention_days
  allow_alb_logs      = true
}

module "database" {
  source          = "../../modules/database"
  name            = local.name
  vpc_id          = module.network.vpc_id
  data_subnet_ids = module.network.data_subnet_ids
  client_security_group_ids = {
    core     = { app = module.network.app_security_group_id, scheduler = module.network.scheduler_security_group_id }
    keystore = { app = module.network.app_security_group_id }
  }
  kms_key_arn          = module.keys.data_key_arn
  keystore_kms_key_arn = module.keys.keystore_backup_key_arn
  multi_az             = false
  providers = {
    aws    = aws
    aws.dr = aws.dr # backup replication is off in staging
  }
}

module "edge" {
  source                 = "../../modules/edge"
  name                   = local.name
  vpc_id                 = module.network.vpc_id
  public_subnet_ids      = module.network.public_subnet_ids
  app_security_group_id  = module.network.app_security_group_id
  certificate_arn        = var.certificate_arn
  logs_bucket_name       = module.storage.logs_bucket_name
  kms_key_arn            = module.keys.data_key_arn
  internal_allowed_cidrs = [for ip in module.network.nat_public_ips : "${ip}/32"]
}

module "compute" {
  source                      = "../../modules/compute"
  name                        = local.name
  app_env                     = "staging"
  app_subnet_ids              = module.network.app_subnet_ids
  app_security_group_id       = module.network.app_security_group_id
  scheduler_security_group_id = module.network.scheduler_security_group_id
  api_public_url              = var.api_public_url
  kms_key_arn                 = module.keys.data_key_arn
  blobs_bucket_arn            = module.storage.blobs_bucket_arn
  worm_bucket_arn             = module.storage.worm_bucket_arn
  blobs_bucket_name           = module.storage.blobs_bucket_name
  worm_bucket_name            = module.storage.worm_bucket_name
  allowed_regions             = [var.region]
  api_target_group_arn        = module.edge.target_group_arn
  secret_name_prefix          = "mugizh/staging"
  image_tag                   = var.image_tag
  otlp_endpoint               = var.otlp_endpoint
}

module "ci_access" {
  source                    = "../../modules/ci_access"
  name                      = local.name
  project_path              = var.gitlab_project_path
  ref_type                  = "branch"
  ref_patterns              = ["main"]
  oidc_provider_thumbprints = var.oidc_provider_thumbprints
  cluster_arn               = module.compute.cluster_arn
  service_arns              = module.compute.service_arns
  migrate_task_families     = module.compute.migrate_task_families
  role_arns_to_pass         = concat(module.compute.task_role_arns, module.compute.exec_role_arns)
  ecr_repository_arns       = module.compute.ecr_repository_arns
  task_families             = module.compute.task_families
  alerts_topic_arn          = module.observability.sns_topic_arn
}

module "observability" {
  source                  = "../../modules/observability"
  name                    = local.name
  kms_key_arn             = module.keys.data_key_arn
  alert_protocol          = var.alert_protocol
  alert_endpoint          = var.alert_endpoint
  alb_arn_suffix          = module.edge.alb_arn_suffix
  target_group_arn_suffix = module.edge.target_group_arn_suffix
  api_log_group_name      = module.compute.log_group_names["api"]
  worker_log_group_name   = module.compute.log_group_names["worker"]
  db_instance_identifiers = module.database.instance_identifiers
}

# GuardDuty in both India regions (the account must have ap-south-2 enabled first).
module "guardduty" {
  source = "../../modules/guardduty"
}

module "guardduty_dr" {
  source    = "../../modules/guardduty"
  providers = { aws = aws.dr }
}
