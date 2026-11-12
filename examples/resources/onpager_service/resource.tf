resource "onpager_service" "example" {
  name    = "API"
  team_id = var.team_id
}

variable "team_id" {
  description = "Existing active owning team UUID."
  type        = string
}
