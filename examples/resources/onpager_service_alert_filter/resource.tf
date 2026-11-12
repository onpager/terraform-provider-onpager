resource "onpager_service_alert_filter" "example" {
  service_id = var.service_id
  cel_source = "false"
  active     = true
}

variable "service_id" {
  description = "Existing service UUID."
  type        = string
}
