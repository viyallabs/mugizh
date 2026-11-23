variable "region" {
  description = "OCI region. India only (K-DP-01): Mumbai, or Hyderabad as the option."
  type        = string
  default     = "ap-mumbai-1"
  validation {
    condition     = contains(["ap-mumbai-1", "ap-hyderabad-1"], var.region)
    error_message = "region must be ap-mumbai-1 (Mumbai) or ap-hyderabad-1 (Hyderabad)."
  }
}

variable "tenancy_ocid" {
  description = "[FILL] Tenancy OCID. Used for the availability domain and Object Storage namespace lookups."
  type        = string
}

variable "parent_compartment_ocid" {
  description = "[FILL] Compartment that will hold the dev compartment (the tenancy OCID for the root). It also holds the tag namespace and the IAM policies, so the applying user needs rights there."
  type        = string
}

variable "owner" {
  description = "[FILL] Value of the owner tag (a team or role, not a personal e-mail address)."
  type        = string
}

variable "ssh_public_key" {
  description = "[FILL] SSH public key (not secret) for the VM default user. Used only through the OCI Bastion service."
  type        = string
}

variable "bastion_client_cidrs" {
  description = "[FILL] Public CIDRs allowed to open Bastion sessions (the office or a VPN). No default on purpose: 0.0.0.0/0 is rejected."
  type        = list(string)
  validation {
    condition     = length(var.bastion_client_cidrs) > 0 && !contains(var.bastion_client_cidrs, "0.0.0.0/0")
    error_message = "Give at least one CIDR and never 0.0.0.0/0."
  }
}

variable "vcn_cidr" {
  type    = string
  default = "10.40.0.0/16"
}

variable "private_subnet_cidr" {
  type    = string
  default = "10.40.1.0/24"
}

variable "vm_shape" {
  description = "Flex shape. [VALIDATE] availability in the region and the tenancy limits."
  type        = string
  default     = "VM.Standard.E4.Flex"
}

variable "vm_ocpus" {
  type    = number
  default = 2
}

variable "vm_memory_gbs" {
  type    = number
  default = 12
}

variable "boot_volume_gbs" {
  type    = number
  default = 50
}

variable "db_volume_gbs" {
  description = "Block volume that holds the PostgreSQL data directory."
  type        = number
  default     = 50
}

variable "ubuntu_version" {
  description = "Canonical Ubuntu image version, chosen by data source (latest build of this version)."
  type        = string
  default     = "24.04"
}

variable "docker_io_version" {
  description = "Exact apt version of docker.io (for example 27.5.1-0ubuntu3~24.04.2). Empty installs the archive current version; pin it after the first build [FILL]."
  type        = string
  default     = ""
}

variable "docker_compose_version" {
  description = "Exact apt version of docker-compose-v2. Empty installs the archive current version; pin it after the first build [FILL]."
  type        = string
  default     = ""
}

variable "postgis_image" {
  description = "Same image as deploy/local/docker-compose.yml."
  type        = string
  default     = "postgis/postgis:17-3.5"
}

variable "core_image" {
  description = "[FILL] Core image in the GitLab registry, without the tag (for example registry.gitlab.com/<group>/<project>/core)."
  type        = string
}

variable "core_image_tag" {
  description = "[FILL] Release tag of the core image. latest is rejected, as on AWS."
  type        = string
  validation {
    condition     = var.core_image_tag != "latest" && var.core_image_tag != ""
    error_message = "Use an immutable tag, not latest."
  }
}

variable "log_retention_days" {
  description = "Days that flow logs and audit logs are kept in the logs bucket (at least 365) [CONFIRM for dev]. The OCI Logging service keeps at most 180 days, so the bucket is the long-term copy."
  type        = number
  default     = 365
  validation {
    condition     = var.log_retention_days >= 365
    error_message = "Logs are kept at least 365 days."
  }
}

variable "ci_policy_enabled" {
  description = "Create the dev-only CI group and policy (ADR-10 fallback: a CI user with an API key in a protected GitLab variable). The user and its key are made by hand."
  type        = bool
  default     = true
}
