# Passive definition: Terraform configures a rule without submitting alerts.
resource "onpager_inhibition_rule" "example" {
  name                   = "Database maintenance dependency"
  source_expression      = "labels['component'] == 'database'"
  target_expression      = "labels['component'] == 'application'"
  resolved_target_action = "release"
  enabled                = false

  matchers = [{
    source = "deployment/cluster"
    target = "deployment/cluster"
  }]
}
