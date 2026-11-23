output "alb_dns_name" { value = module.edge.alb_dns_name }
output "deploy_role_arn" { value = module.ci_access.deploy_role_arn }
output "db_endpoints" { value = module.database.endpoints }
output "nat_public_ips" { value = module.network.nat_public_ips }
output "secrets_to_populate" { value = module.compute.secret_names }
output "alerts_topic_arn" { value = module.observability.sns_topic_arn }
