variable "dns_primary_profile_name" {
  type        = string
  description = "The profile name to use for the DNS primary account"
  default     = "core-dns/terraform"
}

provider "aws" {
  # The AWS provider to use to make changes in the DNS primary account
  alias  = "primary"
  region = var.region

  profile = var.dns_primary_profile_name
}
