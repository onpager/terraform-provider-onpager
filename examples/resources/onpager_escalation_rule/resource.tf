resource "onpager_escalation_rule" "example" {
  team_id    = var.team_id
  name       = "Route production"
  cel_source = "false"
  chain_id   = var.chain_id
  active     = true
}

variable "team_id" {
  description = "Existing owning team UUID."
  type        = string
}

variable "chain_id" {
  description = "Existing active escalation chain UUID valid for this team."
  type        = string
}
