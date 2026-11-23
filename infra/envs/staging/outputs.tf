output "alb_dns_name" { value = module.edge.alb_dns_name }
output "deploy_role_arn" { value = module.ci_access.deploy_role_arn }
output "infra_role_arn" { value = module.ci_access.infra_role_arn }
output "db_endpoints" { value = module.database.endpoints }
output "nat_public_ips" { value = module.network.nat_public_ips }
output "secrets_to_populate" { value = module.compute.secret_names }
output "alerts_topic_arn" { value = module.observability.sns_topic_arn }

# Values for the GitLab CI variables of this environment (docs/ops/production-readiness.md section 4).
# None is a secret. Read with: tofu output -json deploy_ci_variables
output "deploy_ci_variables" {
  value = {
    DEPLOY_ROLE_ARN        = module.ci_access.deploy_role_arn
    DEPLOY_SUBNET_IDS      = join(",", module.network.app_subnet_ids)
    DEPLOY_APP_SG_ID       = module.network.app_security_group_id
    DEPLOY_SCHEDULER_SG_ID = module.network.scheduler_security_group_id
  }
}
