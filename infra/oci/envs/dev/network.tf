data "oci_core_services" "all" {
  filter {
    name   = "name"
    values = ["All .* Services In Oracle Services Network"]
    regex  = true
  }
}

resource "oci_core_vcn" "dev" {
  compartment_id = oci_identity_compartment.dev.id
  display_name   = "mugizh-dev"
  cidr_blocks    = [var.vcn_cidr]
  dns_label      = "mugizhdev"
  freeform_tags  = local.freeform_tags
  defined_tags   = local.defined_tags
}

# No internet gateway and no public subnet: the Bastion service needs only a private endpoint, and the
# VM needs only outbound access. Nothing in this VCN has a public address.
resource "oci_core_nat_gateway" "dev" {
  compartment_id = oci_identity_compartment.dev.id
  vcn_id         = oci_core_vcn.dev.id
  display_name   = "mugizh-dev-nat"
  freeform_tags  = local.freeform_tags
  defined_tags   = local.defined_tags
}

resource "oci_core_service_gateway" "dev" {
  compartment_id = oci_identity_compartment.dev.id
  vcn_id         = oci_core_vcn.dev.id
  display_name   = "mugizh-dev-sgw"
  services {
    service_id = data.oci_core_services.all.services[0].id
  }
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}

resource "oci_core_route_table" "private" {
  compartment_id = oci_identity_compartment.dev.id
  vcn_id         = oci_core_vcn.dev.id
  display_name   = "mugizh-dev-private"
  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_nat_gateway.dev.id
    description       = "Egress through NAT"
  }
  route_rules {
    destination       = data.oci_core_services.all.services[0].cidr_block
    destination_type  = "SERVICE_CIDR_BLOCK"
    network_entity_id = oci_core_service_gateway.dev.id
    description       = "Object Storage and other Oracle services without leaving the Oracle network"
  }
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}

# The VCN default security list allows SSH from 0.0.0.0/0. Take it over and empty it so nothing can use it.
resource "oci_core_default_security_list" "locked" {
  manage_default_resource_id = oci_core_vcn.dev.default_security_list_id
  display_name               = "default-locked-no-rules"
  freeform_tags              = local.freeform_tags
  defined_tags               = local.defined_tags
}

# Egress only: HTTPS and HTTP (package mirrors, GitLab registry, container registry). DNS and NTP use
# the VCN resolver and the link-local time service, which need no rule. No ingress rules at all.
resource "oci_core_security_list" "private" {
  compartment_id = oci_identity_compartment.dev.id
  vcn_id         = oci_core_vcn.dev.id
  display_name   = "mugizh-dev-private-egress-only"
  egress_security_rules {
    destination = "0.0.0.0/0"
    protocol    = "6"
    description = "HTTPS out"
    tcp_options {
      min = 443
      max = 443
    }
  }
  egress_security_rules {
    destination = "0.0.0.0/0"
    protocol    = "6"
    description = "HTTP out (OS package mirrors)"
    tcp_options {
      min = 80
      max = 80
    }
  }
  freeform_tags = local.freeform_tags
  defined_tags  = local.defined_tags
}

resource "oci_core_subnet" "private" {
  compartment_id             = oci_identity_compartment.dev.id
  vcn_id                     = oci_core_vcn.dev.id
  display_name               = "mugizh-dev-private"
  cidr_block                 = var.private_subnet_cidr
  dns_label                  = "priv"
  prohibit_public_ip_on_vnic = true
  prohibit_internet_ingress  = true
  route_table_id             = oci_core_route_table.private.id
  security_list_ids          = [oci_core_security_list.private.id]
  freeform_tags              = local.freeform_tags
  defined_tags               = local.defined_tags
}

# SSH reaches the VM only from inside the subnet, where the Bastion service private endpoint lives.
resource "oci_core_network_security_group" "vm" {
  compartment_id = oci_identity_compartment.dev.id
  vcn_id         = oci_core_vcn.dev.id
  display_name   = "mugizh-dev-vm"
  freeform_tags  = local.freeform_tags
  defined_tags   = local.defined_tags
}

resource "oci_core_network_security_group_security_rule" "ssh_from_bastion" {
  network_security_group_id = oci_core_network_security_group.vm.id
  direction                 = "INGRESS"
  protocol                  = "6"
  source                    = var.private_subnet_cidr
  source_type               = "CIDR_BLOCK"
  description               = "SSH from the Bastion private endpoint only"
  tcp_options {
    destination_port_range {
      min = 22
      max = 22
    }
  }
}

resource "oci_bastion_bastion" "dev" {
  compartment_id               = oci_identity_compartment.dev.id
  bastion_type                 = "STANDARD"
  name                         = "mugizhdevbastion"
  target_subnet_id             = oci_core_subnet.private.id
  client_cidr_block_allow_list = var.bastion_client_cidrs
  max_session_ttl_in_seconds   = 10800
  freeform_tags                = local.freeform_tags
  defined_tags                 = local.defined_tags
}
