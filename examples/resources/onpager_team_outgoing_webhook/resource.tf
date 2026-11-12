# Passive configuration example: no HTTP delivery is performed by Terraform.
resource "onpager_team_outgoing_webhook" "example" {
  team_id      = "11111111-1111-4111-8111-111111111111"
  purpose      = "notification"
  display_name = "Terraform passive webhook"
  enabled      = false

  request = {
    method          = "POST"
    endpoint        = "https://automation.example.invalid/onpager"
    body_format     = "json"
    body_template   = "{{ json .Notification }}"
    verify_ssl      = true
    timeout_seconds = 5
    headers = [{
      key   = "X-OnPager-Source"
      value = "terraform"
    }]
    auth = { kind = "none" }
  }
  recipient_fields = [{
    key         = "email"
    label       = "Email"
    kind        = "email"
    required    = true
    placeholder = "person@example.invalid"
  }]
}

# Credential example: declare secrets = { token = { action = "replace",
# value = var.webhook_token } } and request.auth = { kind = "bearer",
# token_secret = "token" }. Mark webhook_token sensitive; it persists in state.
