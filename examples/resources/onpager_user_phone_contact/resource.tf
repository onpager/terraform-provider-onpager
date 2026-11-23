variable "user_id" {
  type        = string
  description = "UUID of the user who will receive calls."
}

variable "voice_channel_id" {
  type        = string
  description = "UUID of an enabled Zvonok.com voice provider available to the user."
}

variable "phone" {
  type        = string
  sensitive   = true
  description = "Full E.164 telephone number."
}

variable "template_contact_id" {
  type        = string
  default     = null
  description = "UUID of the phone slot in the user's notification defaults, when completing a draft."
}

resource "terraform_data" "phone_creation" {
  triggers_replace = [var.user_id, var.voice_channel_id, var.template_contact_id]
}

resource "onpager_user_phone_contact" "oncall" {
  user_id             = var.user_id
  channel_id          = var.voice_channel_id
  display_name        = "On-call phone"
  phone               = var.phone
  verified            = true
  request_id          = terraform_data.phone_creation.id
  template_contact_id = var.template_contact_id
}
