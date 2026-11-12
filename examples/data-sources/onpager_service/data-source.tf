data "onpager_team" "owner" {
  name = "Operations"
}

data "onpager_service" "example" {
  name = "Public API"
  # Optional: fail if the service is owned by a different team.
  team_id = data.onpager_team.owner.id
}

output "service_id" {
  value = data.onpager_service.example.id
}
