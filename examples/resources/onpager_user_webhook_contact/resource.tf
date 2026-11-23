variable "team_id" {
  type        = string
  description = "UUID of the team that owns the notification webhook."
}

variable "user_id" {
  type        = string
  description = "UUID of a user with access to that team's webhook."
}

variable "recipient_login" {
  type        = string
  sensitive   = true
  description = "Recipient identifier understood by the notification endpoint."
}

variable "endpoint" {
  type        = string
  description = "HTTP endpoint for the notification receiver."
}

resource "onpager_team_outgoing_webhook" "personal" {
  team_id      = var.team_id
  purpose      = "notification"
  display_name = "Personal notification"
  enabled      = true

  request = {
    method        = "POST"
    endpoint      = var.endpoint
    body_format   = "json"
    body_template = "{\"login\":{{ json .Recipient.login }},\"notification\":{{ json .Notification }}}"
  }

  recipient_fields = [{
    key      = "login"
    label    = "Recipient login"
    kind     = "string"
    required = true
  }]
}

resource "terraform_data" "webhook_creation" {
  triggers_replace = [var.user_id, onpager_team_outgoing_webhook.personal.id]
}

resource "onpager_user_webhook_contact" "oncall" {
  user_id      = var.user_id
  webhook_id   = onpager_team_outgoing_webhook.personal.id
  display_name = "On-call webhook"
  values       = { login = var.recipient_login }
  request_id   = terraform_data.webhook_creation.id
}
