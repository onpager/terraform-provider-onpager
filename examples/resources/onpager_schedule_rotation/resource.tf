# Local timestamps use the provider request timezone. For example,
# timezone = "Europe/Moscow" resolves 09:00 to 06:00Z in the backend.
resource "onpager_schedule_rotation" "example" {
  schedule_id    = var.schedule_id
  effective_from = "2027-01-04T10:00:00"
  starts_at      = "2027-01-04T09:00:00"
  user_ids       = var.user_ids
  recurrence = {
    every = 1
    unit  = "weeks"
  }
}

variable "schedule_id" {
  description = "Existing enabled schedule UUID owned by an active team."
  type        = string
}

variable "user_ids" {
  description = "Ordered queue of distinct active team member UUIDs."
  type        = list(string)
}
