resource "onpager_team" "maintenance" {
  name = "Maintenance"
}

# One silence owns the complete team set, including multiteam declarations.
resource "onpager_silence" "maintenance" {
  scope      = "team"
  team_ids   = [onpager_team.maintenance.id]
  expression = "labels.environment == 'staging'"
  comment    = "Planned staging maintenance"
  starts_at  = "2027-01-10T10:00:00Z"
  ends_at    = "2027-01-10T11:00:00Z"
}
