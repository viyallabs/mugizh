data "oci_identity_availability_domains" "this" {
  compartment_id = var.tenancy_ocid
}

data "oci_objectstorage_namespace" "this" {
  compartment_id = var.tenancy_ocid
}

locals {
  freeform_tags = {
    project    = "mugizh"
    env        = "dev"
    data_class = "synthetic"
    owner      = var.owner
  }
  defined_tags = {
    "${oci_identity_tag_namespace.mugizh.name}.${oci_identity_tag.env.name}"        = "dev"
    "${oci_identity_tag_namespace.mugizh.name}.${oci_identity_tag.data_class.name}" = "synthetic"
  }
  availability_domain = data.oci_identity_availability_domains.this.availability_domains[0].name
}

# One compartment for the whole dev environment. Nothing is created outside it except the tag
# namespace and the policies that name it.
resource "oci_identity_compartment" "dev" {
  compartment_id = var.parent_compartment_ocid
  name           = "mugizh-dev"
  description    = "Mugizh dev environment. Synthetic data only (CLAUDE.md section 5.3)."
  enable_delete  = false
  freeform_tags  = local.freeform_tags
}

# Tag namespace: env and data_class can only take the dev values, so a tag audit finds drift.
resource "oci_identity_tag_namespace" "mugizh" {
  compartment_id = var.parent_compartment_ocid
  name           = "mugizh-dev"
  description    = "Mugizh dev tags"
  freeform_tags  = local.freeform_tags
}

resource "oci_identity_tag" "env" {
  tag_namespace_id = oci_identity_tag_namespace.mugizh.id
  name             = "env"
  description      = "Environment"
  validator {
    validator_type = "ENUM"
    values         = ["dev"]
  }
}

resource "oci_identity_tag" "data_class" {
  tag_namespace_id = oci_identity_tag_namespace.mugizh.id
  name             = "data_class"
  description      = "Data class; dev is synthetic only"
  validator {
    validator_type = "ENUM"
    values         = ["synthetic"]
  }
}
