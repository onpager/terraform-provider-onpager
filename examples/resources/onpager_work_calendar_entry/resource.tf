# Local clocks use the provider request timezone; source weekdays stay UTC.
resource "onpager_work_calendar_entry" "example" {
  team_id     = var.team_id
  calendar_id = var.calendar_id
  name        = "Maintenance"
  effect      = "non_working"
  rule_type   = "weekly"
  weekdays    = ["MO", "WE"]
  start_date  = "2027-01-01"
  end_date    = "2027-12-31"
  clock = {
    reference_date = "2024-01-01"
    windows = [
      { starts_at = "2024-01-01T09:00:00", ends_at = "2024-01-01T10:00:01" },
      { starts_at = "2024-01-03T09:00:00", ends_at = "2024-01-03T10:00:01" },
    ]
  }
}

variable "team_id" {
  description = "Existing owning team UUID."
  type        = string
}

variable "calendar_id" {
  description = "Existing work-calendar UUID in this team."
  type        = string
}
