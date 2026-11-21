variable "user_id" {
  type        = string
  description = "OnPager UUID of the on-call user."
}

variable "pachca_bot_id" {
  type        = string
  description = "OnPager UUID of the Pachca bot available to the user."
}

variable "pachca_recipient_id" {
  type        = string
  sensitive   = true
  description = "Personal recipient ID known to this bot, for example the string 201 on a load-test mock."
}

resource "onpager_user_bot_contact" "oncall" {
  user_id      = var.user_id
  bot_id       = var.pachca_bot_id
  display_name = "Personal Pachca"
  recipient_id = var.pachca_recipient_id
  verified     = true
}
