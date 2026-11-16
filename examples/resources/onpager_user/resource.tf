variable "initial_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "onpager_user" "example" {
  display_name = "Example user"
  login        = "example-user"
  email        = "example-user@example.invalid"
  active       = true

  initial_password                 = var.initial_password
  initial_password_change_required = true
}
