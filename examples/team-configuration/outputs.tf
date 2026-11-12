output "team_id" {
  value = onpager_team.operations.id
}

output "service_id" {
  value = onpager_service.api.id
}

output "automation_service_account_id" {
  value = onpager_service_account.automation.id
}

output "observer_role_id" {
  value = onpager_rbac_role.observer.id
}

output "escalation_chain_id" {
  value = onpager_escalation_chain.primary.id
}

output "integration_webhook_url" {
  value = onpager_integration.monitoring.webhook_url
}

output "integration_token" {
  value     = onpager_integration.monitoring.token
  sensitive = true
}
