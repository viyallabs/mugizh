variable "name" {
  description = "Name prefix, for example mugizh-staging."
  type        = string
}

variable "cidr" {
  description = "VPC CIDR. Must be a /16 (subnets are carved as /20)."
  type        = string
  default     = "10.0.0.0/16"
  validation {
    condition     = endswith(var.cidr, "/16")
    error_message = "cidr must be a /16."
  }
}

variable "azs" {
  description = "Exactly two availability zones in the region."
  type        = list(string)
  validation {
    condition     = length(var.azs) == 2
    error_message = "Exactly two AZs are required."
  }
}

variable "nat_per_az" {
  description = "One NAT gateway per AZ (production) or a single NAT gateway (staging, cheaper)."
  type        = bool
  default     = false
}

variable "kms_key_arn" {
  description = "Environment data key. Encrypts the flow-log and firewall log groups."
  type        = string
}

variable "flow_log_retention_days" {
  description = "VPC flow log retention (K-SEC-01, RET-07)."
  type        = number
  default     = 365
}

variable "enable_egress_firewall" {
  description = <<-EOT
    AWS Network Firewall domain allow-list for outbound traffic (TM-41). COST NOTE: about
    USD 0.395 per endpoint-hour (two AZs, roughly USD 570 a month) plus per-GB processing
    [VALIDATE current pricing]. Default on in production, off in staging.
  EOT
  type        = bool
  default     = false
}

variable "egress_allowed_domains" {
  description = "Domains the app tier may reach (vendor register). A leading dot matches subdomains, for example .example.com. Required when the firewall is enabled."
  type        = list(string)
  default     = []
  validation {
    condition     = alltrue([for d in var.egress_allowed_domains : !startswith(d, "http")])
    error_message = "Give domain names only, not URLs."
  }
}
