variable "telegram_channel_id" {
  type        = string
  description = "UUID of an existing enabled, globally visible Telegram bot channel."
}

locals {
  telegram_contact_slot_id = "787e409c-5bbf-420f-bf42-98d0daea3263"
}

resource "onpager_user_notification_defaults" "example" {
  enabled = true
  contacts = [{
    id                   = local.telegram_contact_slot_id
    channel_id           = var.telegram_channel_id
    display_name         = "Telegram"
    require_verification = true
  }]

  policies = {
    default = {
      configured = true
      steps      = [{ kind = "notify", contact_id = local.telegram_contact_slot_id }]
    }
    important = {
      configured = true
      steps = [
        { kind = "notify", contact_id = local.telegram_contact_slot_id },
        { kind = "wait", wait_seconds = 60 },
        { kind = "notify", contact_id = local.telegram_contact_slot_id },
      ]
    }
  }
}

variable "initial_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "onpager_user" "example" {
  depends_on = [onpager_user_notification_defaults.example]

  login            = "example-user"
  display_name     = "Example user"
  initial_password = var.initial_password
}
