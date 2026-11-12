resource "onpager_oidc_group_mapping" "example" {
  group_id      = var.group_id
  provider_slug = "primary"
  issuer        = "https://identity.example.invalid"
  client_id     = "onpager"
  external_key  = "Operations"
}

variable "group_id" {
  description = "Existing custom group UUID; each resource manages one explicit local mapping."
  type        = string
}
