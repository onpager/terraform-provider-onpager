terraform {
  required_version = ">= 1.11.0, < 2.0.0"

  required_providers {
    onpager = {
      source = "onpager/onpager"
    }
  }
}

# Supply values through variables or use the ONPAGER_* environment fallback.
provider "onpager" {
  endpoint = var.onpager_endpoint
  token    = var.onpager_token
  timezone = var.onpager_timezone
}

variable "onpager_endpoint" {
  description = "Control base URL. Null uses ONPAGER_ENDPOINT."
  type        = string
  default     = null
}

variable "onpager_token" {
  description = "Control bearer token. Null uses ONPAGER_TOKEN."
  type        = string
  sensitive   = true
  default     = null
}

variable "onpager_timezone" {
  description = "Optional IANA request timezone. Null sends no override."
  type        = string
  default     = null
}
