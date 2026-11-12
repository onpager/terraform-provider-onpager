data "onpager_group" "example" {
  key = "operations"
}

output "group_id" {
  value = data.onpager_group.example.id
}
