data "onpager_rbac_permissions" "example" {}

output "teams_read_metadata" {
  value = data.onpager_rbac_permissions.example.permissions["teams.read"]
}

output "teams_read_supports_service_accounts" {
  value = contains(
    data.onpager_rbac_permissions.example.permissions["teams.read"].actors,
    "service_account",
  )
}
