terraform {
  required_version = ">= 1.10.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "5.100.0"
    }
  }

  # Applied first, from the management account, by hand (infra/README.md, bootstrap).
  backend "s3" {
    bucket       = "[FILL]-mugizh-tfstate-org"
    key          = "org/terraform.tfstate"
    region       = "ap-south-1"
    encrypt      = true
    kms_key_id   = "[FILL]"
    use_lockfile = true
  }
}

# Organizations is a global service; calls from the management account are not limited by SCPs.
provider "aws" {
  region = "ap-south-1"
  default_tags {
    tags = {
      project    = "mugizh"
      env        = "org"
      data_class = "none"
      owner      = var.owner
    }
  }
}
