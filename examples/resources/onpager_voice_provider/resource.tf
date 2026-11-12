variable "zvonok_public_key" {
  type        = string
  sensitive   = true
  description = "Zvonok API v1 public key. It persists in Terraform state and saved plans."
}

resource "onpager_voice_provider" "example" {
  kind         = "zvonok"
  display_name = "Primary voice"
  enabled      = false

  configuration = {
    call_campaign_id         = "100001"
    verification_campaign_id = "100002"
    max_text_runes           = 500
  }

  secrets = {
    public_key = {
      action = "replace"
      value  = var.zvonok_public_key
    }
  }
}
