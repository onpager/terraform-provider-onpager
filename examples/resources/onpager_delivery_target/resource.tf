variable "pachca_bot_id" {
  type        = string
  description = "UUID of an enabled Pachca bot in OnPager."
}

variable "pachca_account_id" {
  type        = string
  description = "The bot's addressing_scope_id in Pachca."
}

variable "team_id" {
  type        = string
  description = "UUID of the team receiving the alerts."
}

resource "onpager_delivery_target" "ops_alerts" {
  channel_id = var.pachca_bot_id
  snapshot_json = jsonencode({
    BotID       = var.pachca_bot_id
    Scope       = { Kind = "bot_account", ID = var.pachca_account_id }
    Kind        = "conversation"
    ChannelName = "ops-alerts"
  })
}

resource "onpager_team_alert_channel" "ops_alerts" {
  team_id             = var.team_id
  delivery_channel_id = onpager_delivery_target.ops_alerts.channel_id
  target_id           = onpager_delivery_target.ops_alerts.id
}

resource "onpager_escalation_chain" "team_channels" {
  team_id = onpager_team_alert_channel.ops_alerts.team_id
  name    = "Notify team channels"
  steps   = [{ kind = "notify_channel", channel_sources = ["team"] }]
}

resource "onpager_escalation_rule" "warning" {
  team_id    = var.team_id
  name       = "Warning alerts"
  cel_source = "severity == \"warning\""
  chain_id   = onpager_escalation_chain.team_channels.id
}
