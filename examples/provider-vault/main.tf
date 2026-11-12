terraform {
  required_version = ">= 1.11.0, < 2.0.0"
  required_providers {
    onpager = {
      source = "onpager/onpager"
    }
    vault = {
      source = "hashicorp/vault"
    }
  }
}

# Configure Vault authentication independently, for example through VAULT_ADDR
# and VAULT_TOKEN. Use your deployment's supported authentication method.
provider "vault" {}

# Existing KV v2 secret at secret/data/onpager/terraform, with string keys
# endpoint and token. This example reads it without modifying Vault.
data "vault_kv_secret_v2" "onpager" {
  mount = "secret"
  name  = "onpager/terraform"
}

provider "onpager" {
  endpoint = data.vault_kv_secret_v2.onpager.data["endpoint"]
  token    = data.vault_kv_secret_v2.onpager.data["token"]
  timezone = var.onpager_timezone
}

variable "onpager_timezone" {
  description = "Optional IANA request timezone; for example Europe/Moscow or UTC."
  type        = string
  default     = null
}
