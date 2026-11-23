terraform {
  required_version = ">= 1.10.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "5.100.0"
    }
  }

  # Remote state: create the bucket first (infra/README.md, bootstrap). Fill the placeholders.
  # use_lockfile is S3-native locking (OpenTofu 1.10+). No credentials here.
  backend "s3" {
    bucket       = "[FILL]-mugizh-tfstate-staging"
    key          = "staging/terraform.tfstate"
    region       = "ap-south-1"
    encrypt      = true
    kms_key_id   = "[FILL]"
    use_lockfile = true
  }
}

provider "aws" {
  region              = var.region
  allowed_account_ids = [var.account_id]
  default_tags {
    tags = {
      project    = "mugizh"
      env        = "staging"
      data_class = "synthetic"
      owner      = var.owner
    }
  }
}

# Second India region. Staging builds nothing here except the GuardDuty detector (no DR in staging).
provider "aws" {
  alias               = "dr"
  region              = "ap-south-2"
  allowed_account_ids = [var.account_id]
  default_tags {
    tags = {
      project    = "mugizh"
      env        = "staging"
      data_class = "synthetic"
      owner      = var.owner
    }
  }
}
