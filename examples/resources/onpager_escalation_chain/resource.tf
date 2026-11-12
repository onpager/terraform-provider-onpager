resource "onpager_escalation_chain" "example" {
  team_id = var.team_id
  name    = "Primary escalation"
  steps = [
    { kind = "notify_user", target_user_id = var.user_id },
    { kind = "wait", wait_seconds = 60 },
    { kind = "notify_channel", channel_sources = ["service", "team"] },
    { kind = "repeat" },
  ]
}

variable "team_id" {
  description = "Existing owning team UUID."
  type        = string
}

variable "user_id" {
  description = "Existing user UUID to notify when this chain runs."
  type        = string
}
