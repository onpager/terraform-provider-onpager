resource "onpager_team_membership" "example" {
  team_id = var.team_id
  user_id = var.user_id
  lead    = false
}

variable "team_id" {
  description = "Existing team UUID."
  type        = string
}

variable "user_id" {
  description = "Existing active user UUID; this resource does not create users."
  type        = string
}
