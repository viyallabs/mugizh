data "oci_core_images" "ubuntu" {
  compartment_id           = oci_identity_compartment.dev.id
  operating_system         = "Canonical Ubuntu"
  operating_system_version = var.ubuntu_version
  shape                    = var.vm_shape
  sort_by                  = "TIMECREATED"
  sort_order               = "DESC"
}

locals {
  docker_packages = [
    var.docker_io_version == "" ? "docker.io" : "docker.io=${var.docker_io_version}",
    var.docker_compose_version == "" ? "docker-compose-v2" : "docker-compose-v2=${var.docker_compose_version}",
  ]
  db_device = "/dev/oracleoci/oraclevdb"
}

resource "oci_core_volume" "db" {
  compartment_id      = oci_identity_compartment.dev.id
  availability_domain = local.availability_domain
  display_name        = "mugizh-dev-db"
  size_in_gbs         = var.db_volume_gbs
  vpus_per_gb         = 10
  kms_key_id          = oci_kms_key.storage.id
  freeform_tags       = local.freeform_tags
  defined_tags        = local.defined_tags
  depends_on          = [oci_identity_policy.storage_use_key]
}

resource "oci_core_instance" "dev" {
  compartment_id                      = oci_identity_compartment.dev.id
  availability_domain                 = local.availability_domain
  display_name                        = "mugizh-dev"
  shape                               = var.vm_shape
  is_pv_encryption_in_transit_enabled = true
  preserve_boot_volume                = false
  shape_config {
    ocpus         = var.vm_ocpus
    memory_in_gbs = var.vm_memory_gbs
  }
  source_details {
    source_type             = "image"
    source_id               = data.oci_core_images.ubuntu.images[0].id
    boot_volume_size_in_gbs = var.boot_volume_gbs
    kms_key_id              = oci_kms_key.storage.id
  }
  create_vnic_details {
    subnet_id              = oci_core_subnet.private.id
    assign_public_ip       = false
    nsg_ids                = [oci_core_network_security_group.vm.id]
    skip_source_dest_check = false
  }
  instance_options {
    are_legacy_imds_endpoints_disabled = true
  }
  agent_config {
    are_all_plugins_disabled = false
    is_management_disabled   = false
    is_monitoring_disabled   = false
    plugins_config {
      name          = "Bastion"
      desired_state = "ENABLED"
    }
  }
  metadata = {
    ssh_authorized_keys = var.ssh_public_key
    user_data = base64encode(templatefile("${path.module}/cloud-init.yaml.tftpl", {
      packages      = local.docker_packages
      db_device     = local.db_device
      postgis_image = var.postgis_image
      core_image    = "${var.core_image}:${var.core_image_tag}"
    }))
  }
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
  lifecycle {
    # The image is picked by data source (latest build). Do not replace the VM when a newer build appears.
    ignore_changes = [source_details[0].source_id]
  }
  depends_on = [oci_identity_policy.storage_use_key]
}

# Paravirtualized attachment with encryption in transit on, as required for dev.
resource "oci_core_volume_attachment" "db" {
  attachment_type                     = "paravirtualized"
  instance_id                         = oci_core_instance.dev.id
  volume_id                           = oci_core_volume.db.id
  device                              = local.db_device
  is_pv_encryption_in_transit_enabled = true
}
