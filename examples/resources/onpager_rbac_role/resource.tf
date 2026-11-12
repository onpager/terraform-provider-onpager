resource "onpager_rbac_role" "example" {
  key             = "configuration_reader"
  name            = "Configuration reader"
  description     = "Read teams and services without changing configuration"
  scope_type      = "tenant"
  permission_keys = ["teams.read", "services.read"]
}
