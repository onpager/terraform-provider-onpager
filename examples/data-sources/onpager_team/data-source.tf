data "onpager_team" "example" {
  name = "Operations"
}

output "team_id" {
  value = data.onpager_team.example.id
}
