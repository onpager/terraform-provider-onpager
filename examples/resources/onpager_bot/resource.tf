variable "bot_token" {
  type      = string
  sensitive = true
}

resource "onpager_bot" "example" {
  messenger      = "telegram"
  display_name   = "Alert bot"
  enrollment_url = "https://t.me/example_bot"
  enabled        = false

  configuration = {
    mode = "polling"
  }

  secrets = {
    token = {
      action = "replace"
      value  = var.bot_token
    }
  }

  # Telegram discovers addressing_scope_id after an enabled runtime starts.
  # The bot UUID is also its delivery-channel UUID. Catalog bindings reference
  # onpager_bot.example.id through their delivery_channel_id attribute.
}
