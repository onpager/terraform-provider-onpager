resource "onpager_team_alert_filter" "example" {
  team_id    = var.team_id
  cel_source = "false"
  active     = true
}

variable "team_id" {
  description = "Existing team UUID."
  type        = string
}
