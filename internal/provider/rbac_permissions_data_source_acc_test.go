package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Authenticated metadata requires no business grant and creates no IAM object.
func TestAccRBACPermissionsDataSource(t *testing.T) {
	routingAccSetup(t)
	config := routingAccHeader + `
data "onpager_rbac_permissions" "catalog" {}
output "team_target" { value = data.onpager_rbac_permissions.catalog.permissions["teams.read"].target }
output "machine_compatible" { value = contains(data.onpager_rbac_permissions.catalog.permissions["teams.read"].actors, "service_account") }
output "reserved_grantable" { value = data.onpager_rbac_permissions.catalog.permissions["bootstrap_passwords.reset"].custom_grantable }
`
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), Steps: []resource.TestStep{
		{Config: config, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("data.onpager_rbac_permissions.catalog", "id", "catalog"),
			resource.TestCheckOutput("team_target", "team"),
			resource.TestCheckOutput("machine_compatible", "true"),
			resource.TestCheckOutput("reserved_grantable", "false"),
		)},
		{Config: config, PlanOnly: true},
	}})
}
