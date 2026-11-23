# Blob bucket: private, versioned, encrypted with the dev key. The application writes blobs to a local
# directory in dev for now; the future S3 adapter uses the S3-compatible endpoint in the outputs.
resource "oci_objectstorage_bucket" "blobs" {
  compartment_id        = oci_identity_compartment.dev.id
  namespace             = data.oci_objectstorage_namespace.this.namespace
  name                  = "mugizh-dev-blobs"
  access_type           = "NoPublicAccess"
  storage_tier          = "Standard"
  versioning            = "Enabled"
  kms_key_id            = oci_kms_key.storage.id
  object_events_enabled = false
  freeform_tags         = local.freeform_tags
  defined_tags          = local.defined_tags
  depends_on            = [oci_identity_policy.storage_use_key]
}

# Long-term copy of flow logs and audit events. The provider has no retention-rule resource, so the 365-day
# retention rule is created by hand (infra/oci/README.md, bootstrap). Versioning stays off for this bucket [VALIDATE].
resource "oci_objectstorage_bucket" "logs" {
  compartment_id        = oci_identity_compartment.dev.id
  namespace             = data.oci_objectstorage_namespace.this.namespace
  name                  = "mugizh-dev-logs"
  access_type           = "NoPublicAccess"
  storage_tier          = "Standard"
  versioning            = "Disabled"
  kms_key_id            = oci_kms_key.storage.id
  object_events_enabled = false
  freeform_tags         = local.freeform_tags
  defined_tags          = local.defined_tags
  depends_on            = [oci_identity_policy.storage_use_key]
}
