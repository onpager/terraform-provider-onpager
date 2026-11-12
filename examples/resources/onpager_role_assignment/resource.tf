resource "onpager_role_assignment" "example" {
  role_id = var.role_id
  recipient = {
    kind = "service_account"
    id   = var.service_account_id
  }
  target_team_id = var.target_team_id
}

variable "role_id" {
  description = "Existing role UUID compatible with the target scope and service-account recipient."
  type        = string
}
variable "service_account_id" {
  description = "Existing service account UUID."
  type        = string
}
variable "target_team_id" {
  description = "Target team UUID for a team role, or null for a tenant role."
  type        = string
  default     = null
}
