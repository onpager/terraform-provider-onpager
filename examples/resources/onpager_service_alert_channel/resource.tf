resource "onpager_service_alert_channel" "example" {
  service_id          = var.service_id
  delivery_channel_id = var.delivery_channel_id
  target_id           = var.target_id
}

variable "service_id" {
  description = "Existing service UUID."
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
