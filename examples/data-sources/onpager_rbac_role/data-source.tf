data "onpager_rbac_role" "example" {
  key = "configuration_reader"
}

output "role_id" {
  value = data.onpager_rbac_role.example.id
}
