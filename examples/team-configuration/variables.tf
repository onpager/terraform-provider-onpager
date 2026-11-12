variable "iam_key_prefix" {
  type        = string
  description = "Unique immutable prefix for the custom role/group keys; 1–52 lowercase letters, digits, underscores or hyphens, starting with a letter or digit."
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9_-]{0,51}$", var.iam_key_prefix))
    error_message = "Use 1–52 lowercase letters, digits, underscores or hyphens, starting with a letter or digit."
  }
}

variable "notification_target" {
  type = object({
    delivery_channel_id = string
    target_id           = string
  })
  default     = null
  description = "Optional existing conversation-capable delivery channel/target; no target discovery is performed."
}

variable "oidc_group" {
  type = object({
    provider_slug = string
    issuer        = string
    client_id     = string
    external_key  = string
  })
  default     = null
  description = "Optional existing deployment OIDC identity/group mapping. Terraform saves the local mapping without contacting the identity provider."
}

variable "onpager_timezone" {
  type        = string
  default     = null
  description = "Optional IANA request timezone. Null keeps user profile or service-account UTC; explicit values override both."
}

variable "operator_user_id" {
  type        = string
  description = "An existing active user's UUID; this example does not create users."
}

variable "rotation_starts_at" {
  type        = string
  description = "Fixed whole-second RFC3339 start and effective boundary for the initial rotation, for example 2027-01-04T00:00:00Z."
}

variable "service_name" {
  type        = string
  description = "A service name not already reserved within the tenant."
}

variable "team_alert_template" {
  type = object({
    delivery_provider = string
    language          = string
    format            = string
    body              = string
  })
  default     = null
  description = "Optional team alert_direct template override for an explicit delivery provider and ru/en language. Omission leaves inheritance unchanged; applying does not send a test notification."
}

variable "team_name" {
  type        = string
  description = "A team name not already reserved by an active or inactive team."
}

variable "temporary_override" {
  type = object({
    starts_at = string
    ends_at   = string
  })
  default     = null
  description = "Optional one-off override interval for the operator, using explicit RFC3339 offsets and whole seconds."
}
