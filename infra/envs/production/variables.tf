variable "account_id" {
  description = "AWS account ID of the production account [FILL]."
  type        = string
}

variable "region" {
  description = "Primary India region."
  type        = string
  default     = "ap-south-1"
  validation {
    condition     = contains(["ap-south-1", "ap-south-2"], var.region)
    error_message = "Only the two India regions are allowed (R1)."
  }
}

variable "azs" {
  type    = list(string)
  default = ["ap-south-1a", "ap-south-1b"]
}

variable "owner" {
  description = "Owner tag [FILL]."
  type        = string
}

variable "certificate_arn" {
  description = "ACM certificate ARN for the API host name [FILL]."
  type        = string
}

variable "image_tag" {
  description = "Release image tag to run [FILL]."
  type        = string
}

variable "gitlab_project_path" {
  description = "GitLab project path, group/project [FILL]."
  type        = string
}

variable "oidc_provider_thumbprints" {
  description = "Root CA thumbprint(s) of the GitLab host [FILL]."
  type        = list(string)
}

variable "enable_egress_firewall" {
  description = "Network Firewall egress allow-list (TM-41). On in production. Cost: about USD 570 a month for two AZs plus per-GB charges [VALIDATE]."
  type        = bool
  default     = true
}

variable "egress_allowed_domains" {
  description = "Vendor domains the app tier may reach, from the vendor register."
  type        = list(string)
  default     = []
}

variable "worm_retention_days" {
  description = "Object Lock retention in COMPLIANCE mode, irreversible. [CONFIRM] with the founder and lawyer. No default on purpose."
  type        = number
}

variable "alert_protocol" {
  type    = string
  default = "email"
}

variable "alert_endpoint" {
  description = "Alert email or pager endpoint [FILL]."
  type        = string
  default     = ""
}

variable "otlp_endpoint" {
  description = "India-hosted OTLP collector (R3) [FILL]."
  type        = string
  default     = ""
}

variable "key_admin_arns" {
  type    = list(string)
  default = []
}

variable "dr_region" {
  description = "DR region."
  type        = string
  default     = "ap-south-2"
  validation {
    condition     = contains(["ap-south-1", "ap-south-2"], var.dr_region)
    error_message = "Only the two India regions are allowed (R1)."
  }
}

variable "dr_azs" {
  type    = list(string)
  default = ["ap-south-2a", "ap-south-2b"]
}
