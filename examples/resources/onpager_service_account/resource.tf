resource "onpager_service_account" "example" {
  display_name = "Configuration automation"
  active       = true
  # Omitted owner_team_id selects tenant-level management.
  # No roles or tokens are created by this resource.
}
