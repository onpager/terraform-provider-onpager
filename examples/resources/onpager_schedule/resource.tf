resource "onpager_schedule" "example" {
  team_id = var.team_id
  name    = "Operations"
  enabled = true
}

variable "team_id" {
  description = "Existing active owning team UUID."
  type        = string
}
