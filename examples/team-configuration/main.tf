resource "onpager_integration" "monitoring" {
  name        = "${var.team_name} monitoring"
  source_kind = "alertmanager"
}

resource "onpager_team" "operations" {
  name = var.team_name
}

resource "onpager_service" "api" {
  name    = var.service_name
  team_id = onpager_team.operations.id
}

resource "onpager_team_membership" "operator" {
  team_id = onpager_team.operations.id
  user_id = var.operator_user_id
  lead    = true
}

# Account ownership and role definitions grant no access by themselves.
# These explicit assignments grant only the observer permissions below.
resource "onpager_service_account" "automation" {
  display_name  = "${var.team_name} automation"
  owner_team_id = onpager_team.operations.id
}

resource "onpager_rbac_role" "observer" {
  key             = "${var.iam_key_prefix}_observer"
  name            = "${var.team_name} observer"
  scope_type      = "team"
  permission_keys = ["teams.read", "services.read", "schedules.read"]
}

resource "onpager_group" "operators" {
  key         = "${var.iam_key_prefix}_operators"
  name        = "${var.team_name} operators"
  description = "Operations group"
}

resource "onpager_group_membership" "operator" {
  group_id = onpager_group.operators.id
  user_id  = var.operator_user_id
}

resource "onpager_oidc_group_mapping" "operators" {
  count         = var.oidc_group == null ? 0 : 1
  group_id      = onpager_group.operators.id
  provider_slug = var.oidc_group.provider_slug
  issuer        = var.oidc_group.issuer
  client_id     = var.oidc_group.client_id
  external_key  = var.oidc_group.external_key
}

resource "onpager_role_assignment" "automation_observer" {
  role_id        = onpager_rbac_role.observer.id
  target_team_id = onpager_team.operations.id
  recipient = {
    kind = "service_account"
    id   = onpager_service_account.automation.id
  }
}

resource "onpager_role_assignment" "operators_observer" {
  role_id        = onpager_rbac_role.observer.id
  target_team_id = onpager_team.operations.id
  recipient = {
    kind = "group"
    id   = onpager_group.operators.id
  }
}

resource "onpager_team_notification_template" "alert" {
  count             = var.team_alert_template == null ? 0 : 1
  team_id           = onpager_team.operations.id
  type              = "alert_direct"
  delivery_provider = var.team_alert_template.delivery_provider
  language          = var.team_alert_template.language
  format            = var.team_alert_template.format
  body              = var.team_alert_template.body
}

resource "onpager_team_alert_filter" "monitoring" {
  team_id    = onpager_team.operations.id
  cel_source = "integration_id == ${jsonencode(onpager_integration.monitoring.id)}"
}

resource "onpager_service_alert_filter" "monitoring" {
  service_id = onpager_service.api.id
  cel_source = "integration_id == ${jsonencode(onpager_integration.monitoring.id)}"
}

resource "onpager_work_calendar" "office" {
  team_id = onpager_team.operations.id
  name    = "Office hours"
  base_schedule = {
    reference_date = "2024-01-01"
    intervals = [
      { starts_at = "2024-01-01T09:00:00Z", ends_at = "2024-01-01T17:00:00Z" },
      { starts_at = "2024-01-02T09:00:00Z", ends_at = "2024-01-02T17:00:00Z" },
      { starts_at = "2024-01-03T09:00:00Z", ends_at = "2024-01-03T17:00:00Z" },
      { starts_at = "2024-01-04T09:00:00Z", ends_at = "2024-01-04T17:00:00Z" },
      { starts_at = "2024-01-05T09:00:00Z", ends_at = "2024-01-05T17:00:00Z" },
    ]
  }
}

resource "onpager_work_calendar_entry" "new_year" {
  team_id     = onpager_team.operations.id
  calendar_id = onpager_work_calendar.office.id
  name        = "New Year"
  effect      = "non_working"
  rule_type   = "yearly"
  month       = 1
  day         = 1
}

resource "onpager_schedule" "primary" {
  team_id          = onpager_team.operations.id
  name             = "Primary on-call"
  work_calendar_id = onpager_work_calendar.office.id
}

resource "onpager_schedule_rotation" "primary" {
  schedule_id    = onpager_schedule.primary.id
  effective_from = var.rotation_starts_at
  starts_at      = var.rotation_starts_at
  user_ids       = [onpager_team_membership.operator.user_id]
  recurrence = {
    every = 1
    unit  = "weeks"
  }
}

resource "onpager_schedule_override" "temporary" {
  count       = var.temporary_override == null ? 0 : 1
  schedule_id = onpager_schedule.primary.id
  user_id     = onpager_team_membership.operator.user_id
  starts_at   = var.temporary_override.starts_at
  ends_at     = var.temporary_override.ends_at
}

resource "onpager_team_alert_channel" "primary" {
  count               = var.notification_target == null ? 0 : 1
  team_id             = onpager_team.operations.id
  delivery_channel_id = var.notification_target.delivery_channel_id
  target_id           = var.notification_target.target_id
}

resource "onpager_service_alert_channel" "primary" {
  count               = var.notification_target == null ? 0 : 1
  service_id          = onpager_service.api.id
  delivery_channel_id = var.notification_target.delivery_channel_id
  target_id           = var.notification_target.target_id
}

resource "onpager_team_oncall_channel" "primary" {
  count               = var.notification_target == null ? 0 : 1
  team_id             = onpager_team.operations.id
  delivery_channel_id = var.notification_target.delivery_channel_id
  target_id           = var.notification_target.target_id
}

resource "onpager_schedule_handover" "primary" {
  count              = var.notification_target == null ? 0 : 1
  team_id            = onpager_team.operations.id
  schedule_id        = onpager_schedule.primary.id
  enabled            = true
  oncall_channel_ids = [onpager_team_oncall_channel.primary[0].id]
}

# Saves configuration only; it never starts an escalation run.
resource "onpager_escalation_chain" "primary" {
  name             = "Primary escalation"
  team_id          = onpager_team.operations.id
  work_calendar_id = onpager_work_calendar.office.id
  steps = concat([
    { kind = "notify_oncall", schedule_id = onpager_schedule.primary.id },
    { kind = "wait", wait_seconds = 60 },
    ], var.notification_target == null ? [] : [
    { kind = "notify_channel", channel_sources = ["service", "team"] }
  ], [{ kind = "repeat" }])

  depends_on = [onpager_schedule_rotation.primary, onpager_schedule_override.temporary, onpager_team_alert_channel.primary, onpager_service_alert_channel.primary]
}

resource "onpager_escalation_rule" "primary" {
  team_id    = onpager_team.operations.id
  name       = "Primary escalation"
  cel_source = "true"
  chain_id   = onpager_escalation_chain.primary.id
}
