# Passive configuration example: no HTTP delivery is performed by Terraform.
resource "onpager_outgoing_webhook" "example" {
  purpose      = "escalation"
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
}

# Credential example: declare secrets = { token = { action = "replace",
# value = var.webhook_token } } and request.auth = { kind = "bearer",
# token_secret = "token" }. Mark webhook_token sensitive; it persists in state.
