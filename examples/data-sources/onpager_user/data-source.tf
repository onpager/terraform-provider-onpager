data "onpager_user" "example" {
  # Selects a local credential, never an OIDC subject or display name.
  local_login = "operator"
}

output "user_id" {
  value = data.onpager_user.example.id
}
