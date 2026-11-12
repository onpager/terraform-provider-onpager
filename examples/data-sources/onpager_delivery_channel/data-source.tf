variable "channel_id" {
  description = "Known delivery channel UUID, distinct from any catalog binding UUID."
  type        = string
}

data "onpager_delivery_channel" "example" {
  id = var.channel_id
}

output "channel_enabled" {
  value = data.onpager_delivery_channel.example.enabled
}
