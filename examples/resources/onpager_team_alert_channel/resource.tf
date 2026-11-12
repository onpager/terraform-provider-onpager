resource "onpager_team_alert_channel" "example" {
  team_id             = var.team_id
  delivery_channel_id = var.delivery_channel_id
  target_id           = var.target_id
}

variable "team_id" {
  description = "Existing team UUID."
  type        = string
}

variable "delivery_channel_id" {
  description = "Existing delivery-channel UUID; not the catalog binding UUID."
  type        = string
}

variable "target_id" {
  description = "Existing compatible, authorized and enabled target UUID in that channel."
  type        = string
}
