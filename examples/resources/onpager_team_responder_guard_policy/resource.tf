resource "onpager_team_responder_guard_policy" "example" {
  team_id                       = "11111111-1111-4111-8111-111111111111"
  max_open_requests_per_subject = 25
}
