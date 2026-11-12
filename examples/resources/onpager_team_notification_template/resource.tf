resource "onpager_team_notification_template" "example" {
  team_id           = "11111111-1111-4111-8111-111111111111"
  type              = "alert_direct"
  delivery_provider = "telegram"
  language          = "en"
  format            = "telegram_html"
  body              = "Alert: {{.Alert.Title | escape}}"
}
