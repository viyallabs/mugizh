# Policies live in the parent compartment and name the dev compartment, so they can be reviewed apart from it.

# Let the storage services use the key. Without these the encrypted volumes and buckets cannot be created.
resource "oci_identity_policy" "storage_use_key" {
  compartment_id = var.parent_compartment_ocid
  name           = "mugizh-dev-storage-use-key"
  description    = "Block Volume and Object Storage may use the dev storage key"
  statements = [
    "Allow service blockstorage to use keys in compartment id ${oci_identity_compartment.dev.id} where target.key.id = '${oci_kms_key.storage.id}'",
    "Allow service objectstorage-${var.region} to use keys in compartment id ${oci_identity_compartment.dev.id} where target.key.id = '${oci_kms_key.storage.id}'",
  ]
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}

# Service connector: flow logs and audit events into the long-retention logs bucket.
resource "oci_identity_policy" "log_archive" {
  compartment_id = var.parent_compartment_ocid
  name           = "mugizh-dev-log-archive"
  description    = "The service connector may read dev logs and write to the logs bucket"
  statements = [
    "Allow any-user to manage objects in compartment id ${oci_identity_compartment.dev.id} where all {request.principal.type = 'serviceconnector', target.bucket.name = '${oci_objectstorage_bucket.logs.name}'}",
    "Allow any-user to read log-content in compartment id ${oci_identity_compartment.dev.id} where request.principal.type = 'serviceconnector'",
  ]
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}

# Dev-only CI fallback (ADR-10): a group a CI user is added to by hand. No API key is created here (K-SEC-05).
# It can open Bastion sessions to the dev VM and read the dev compartment. It cannot touch other compartments.
resource "oci_identity_group" "ci" {
  count          = var.ci_policy_enabled ? 1 : 0
  compartment_id = var.tenancy_ocid
  name           = "mugizh-dev-ci"
  description    = "GitLab CI for the dev environment only. Synthetic data only."
  freeform_tags  = local.freeform_tags
  defined_tags   = local.defined_tags
}

resource "oci_identity_policy" "ci" {
  count          = var.ci_policy_enabled ? 1 : 0
  compartment_id = var.parent_compartment_ocid
  name           = "mugizh-dev-ci"
  description    = "Dev-only CI access: Bastion sessions and read access in the dev compartment [CONFIRM scope]"
  statements = [
    "Allow group ${oci_identity_group.ci[0].name} to manage bastion-session in compartment id ${oci_identity_compartment.dev.id}",
    "Allow group ${oci_identity_group.ci[0].name} to read bastion in compartment id ${oci_identity_compartment.dev.id}",
    "Allow group ${oci_identity_group.ci[0].name} to read instances in compartment id ${oci_identity_compartment.dev.id}",
    "Allow group ${oci_identity_group.ci[0].name} to read virtual-network-family in compartment id ${oci_identity_compartment.dev.id}",
  ]
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}
