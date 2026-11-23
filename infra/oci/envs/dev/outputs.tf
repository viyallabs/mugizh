output "compartment_id" {
  value = oci_identity_compartment.dev.id
}

output "vm_private_ip" {
  value = oci_core_instance.dev.private_ip
}

output "vm_id" {
  value = oci_core_instance.dev.id
}

output "bastion_id" {
  description = "Open a managed SSH or port-forwarding session against this bastion (see infra/oci/README.md)."
  value       = oci_bastion_bastion.dev.id
}

output "blobs_bucket" {
  value = oci_objectstorage_bucket.blobs.name
}

output "s3_compatible_endpoint" {
  description = "For the future S3 adapter (path-style addressing, region = var.region). Credentials are an OCI customer secret key held outside code."
  value       = "https://${data.oci_objectstorage_namespace.this.namespace}.compat.objectstorage.${var.region}.oraclecloud.com"
}

output "storage_key_id" {
  value = oci_kms_key.storage.id
}

output "ci_group" {
  value = var.ci_policy_enabled ? oci_identity_group.ci[0].name : null
}
