# Local timestamps use the provider request timezone. Explicit Z/offset
# values can also be supplied and always identify absolute instants.
resource "onpager_schedule_override" "example" {
  schedule_id = var.schedule_id
  user_id     = var.user_id
  starts_at   = "2027-01-05T09:00:00"
  ends_at     = "2027-01-05T17:00:00"
}

variable "schedule_id" {
  description = "Existing enabled schedule UUID owned by an active team."
  type        = string
}

variable "user_id" {
  description = "Existing active member UUID of the schedule's team."
  type        = string
}
