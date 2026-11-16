resource "onpager_team_responder_policy" "default" {
  team_id    = var.team_id
  importance = "default"
  strategy   = "team_fanout"
}

resource "onpager_escalation_chain" "responders" {
  team_id = var.team_id
  name    = "Responder sequence"
  steps = [
    { kind = "notify_user", target_user_id = var.user_id, importance = "important" },
    { kind = "wait", wait_seconds = 60 },
  ]
}

resource "onpager_team_responder_policy" "example" {
  team_id             = var.team_id
  importance          = "important"
  strategy            = "team_chain"
  chain_id            = onpager_escalation_chain.responders.id
  publication_trigger = onpager_escalation_chain.responders.etag
}

variable "team_id" {
  description = "Existing active owning team UUID. Import any configured importance slot before applying."
  type        = string
}

variable "user_id" {
  description = "Existing active team member UUID referenced by the responder chain."
  type        = string
}
