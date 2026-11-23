# OCI Vault and one master encryption key for the block volumes and the buckets (K-DP-03 at the storage layer).
# The application key provider in dev stays the local root key (synthetic data only): see infra/oci/README.md.
# Software-protected key (no HSM) because dev holds no personal data; staging and production on AWS use KMS.
resource "oci_kms_vault" "dev" {
  compartment_id = oci_identity_compartment.dev.id
  display_name   = "mugizh-dev"
  vault_type     = "DEFAULT"
  freeform_tags  = local.freeform_tags
  defined_tags   = local.defined_tags
}

resource "oci_kms_key" "storage" {
  compartment_id      = oci_identity_compartment.dev.id
  display_name        = "mugizh-dev-storage"
  management_endpoint = oci_kms_vault.dev.management_endpoint
  protection_mode     = "SOFTWARE"
  key_shape {
    algorithm = "AES"
    length    = 32
  }
  is_auto_rotation_enabled = true
  auto_key_rotation_details {
    rotation_interval_in_days = 365
  }
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}
