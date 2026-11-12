resource "onpager_notification_template" "example" {
  type              = "alert_direct"
  delivery_provider = "telegram"
  language          = "en"
  format            = "telegram_html"
  body              = "Alert: {{.Alert.Title | escape}}"
}
