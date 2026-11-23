terraform {
  required_version = ">= 1.10.0"
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "7.22.0"
    }
  }

  # Remote state in OCI Object Storage through its S3 compatibility API. Create the bucket by hand first
  # (infra/oci/README.md, bootstrap) and replace the [FILL] values: backend blocks cannot use variables.
  # Credentials come from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (an OCI "customer secret key"), never from code.
  # Object Storage encrypts every object at rest; encrypt = true asks for it explicitly [VALIDATE the header is accepted].
  # use_lockfile (S3 conditional writes) is left off until OCI support is confirmed [VALIDATE]: one operator applies dev at a time.
  backend "s3" {
    bucket = "[FILL]-mugizh-tfstate-dev"
    key    = "dev/terraform.tfstate"
    region = "ap-mumbai-1"
    endpoints = {
      s3 = "https://[FILL-namespace].compat.objectstorage.ap-mumbai-1.oraclecloud.com"
    }
    use_path_style              = true
    encrypt                     = true
    skip_region_validation      = true
    skip_credentials_validation = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_s3_checksum            = true
  }
}

# Authentication: OCI config file profile or environment variables (TF_VAR / OCI_*). No key, fingerprint or
# token is ever written here (K-SEC-05).
provider "oci" {
  region = var.region
}
