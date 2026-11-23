# VCN flow logs on the private subnet, and a service connector that archives them and the compartment's
# audit events to the logs bucket for at least var.log_retention_days. The Logging service keeps only 180 days.
resource "oci_logging_log_group" "dev" {
  compartment_id = oci_identity_compartment.dev.id
  display_name   = "mugizh-dev"
  freeform_tags  = local.freeform_tags
  defined_tags   = local.defined_tags
}

resource "oci_logging_log" "flow" {
  display_name       = "mugizh-dev-private-subnet-flow"
  log_group_id       = oci_logging_log_group.dev.id
  log_type           = "SERVICE"
  is_enabled         = true
  retention_duration = 180
  configuration {
    compartment_id = oci_identity_compartment.dev.id
    source {
      category    = "all"
      resource    = oci_core_subnet.private.id
      service     = "flowlogs"
      source_type = "OCISERVICE"
    }
  }
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}

resource "oci_sch_service_connector" "log_archive" {
  compartment_id = oci_identity_compartment.dev.id
  display_name   = "mugizh-dev-log-archive"
  source {
    kind = "logging"
    log_sources {
      compartment_id = oci_identity_compartment.dev.id
      log_group_id   = oci_logging_log_group.dev.id
      log_id         = oci_logging_log.flow.id
    }
    log_sources {
      compartment_id = oci_identity_compartment.dev.id
      log_group_id   = "_Audit"
    }
  }
  target {
    kind                       = "objectStorage"
    bucket                     = oci_objectstorage_bucket.logs.name
    namespace                  = data.oci_objectstorage_namespace.this.namespace
    object_name_prefix         = "dev"
    batch_rollover_size_in_mbs = 10
    batch_rollover_time_in_ms  = 600000
  }
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
  depends_on    = [oci_identity_policy.log_archive]
}
