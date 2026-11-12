resource "onpager_work_calendar" "example" {
  team_id = var.team_id
  name    = "Office hours"
  # With provider timezone = "Europe/Moscow", these local clocks become
  # 06:00Z through 14:00Z. Source weekday identities remain UTC.
  base_schedule = {
    reference_date = "2024-01-01"
    intervals = [
      { starts_at = "2024-01-01T09:00:00", ends_at = "2024-01-01T17:00:00" },
      { starts_at = "2024-01-02T09:00:00", ends_at = "2024-01-02T17:00:00" },
      { starts_at = "2024-01-03T09:00:00", ends_at = "2024-01-03T17:00:00" },
      { starts_at = "2024-01-04T09:00:00", ends_at = "2024-01-04T17:00:00" },
      { starts_at = "2024-01-05T09:00:00", ends_at = "2024-01-05T17:00:00" },
    ]
  }
  working_weekend_schedule = {
    reference_date = "2024-01-01"
    intervals      = []
  }
  inherit_production_calendar = false
  production_country_code     = "RU"
}

variable "team_id" {
  description = "Existing owning team UUID."
  type        = string
}
