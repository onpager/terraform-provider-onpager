# Import the existing tenant policy before applying when either set is nonempty.
data "onpager_rbac_role" "member" {
  key = "team_viewer"
}

data "onpager_rbac_role" "lead" {
  key = "team_administrator"
}

resource "onpager_team_role_defaults" "example" {
  members = [data.onpager_rbac_role.member.id]
  leads   = [data.onpager_rbac_role.lead.id]
}
