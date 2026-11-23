variable "owner" {
  description = "Owner tag [FILL]."
  type        = string
}

variable "staging_ou_id" {
  description = "Organisation unit ID of the staging accounts [FILL], ou-xxxx-xxxxxxxx."
  type        = string
}

variable "production_ou_id" {
  description = "Organisation unit ID of the production and DR accounts [FILL]."
  type        = string
}

variable "allowed_regions" {
  description = "R1: the two India regions."
  type        = list(string)
  default     = ["ap-south-1", "ap-south-2"]
}

variable "exempt_principal_arns" {
  description = "Principals exempt from the region deny (ARN patterns). Leave empty. Add only the audited break-glass role if a global-service call needs it [CONFIRM]."
  type        = list(string)
  default     = []
}
