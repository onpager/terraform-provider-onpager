variable "default_team_id" {
  type        = string
  description = "UUID of the active team receiving unmatched alerts."
}

resource "onpager_routing_settings" "example" {
  default_route = {
    target_kind = "team"
    target_id   = var.default_team_id
  }
}
