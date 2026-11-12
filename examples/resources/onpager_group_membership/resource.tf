resource "onpager_group_membership" "example" {
  group_id = var.group_id
  user_id  = var.user_id
}

variable "group_id" {
  description = "Existing custom group UUID; All Users is derived and unsupported."
  type        = string
}

variable "user_id" {
  description = "Existing human user UUID; this source does not change the profile."
  type        = string
}
