package provider_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// These fixtures own only new tfacc-* teams/accounts/custom roles. No keys or
// assignments are issued and no existing principal's access is changed.
func TestAccIAMFoundation(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var accountID, roleID string
	config := func(updated, active bool) string {
		owner := "onpager_team.iam_a.id"
		label, permissions := prefix+"-account", `["teams.read"]`
		if updated {
			owner = "onpager_team.iam_b.id"
			label += "-updated"
			permissions = `["teams.read", "services.read"]`
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "iam_a" { name = %q }
resource "onpager_team" "iam_b" { name = %q }
resource "onpager_service_account" "iam" {
 display_name = %q
 owner_team_id = %s
 active = %t
}
resource "onpager_rbac_role" "iam" {
 key = %q
 name = %q
 scope_type = "tenant"
 description = %q
 permission_keys = %s
}
`, prefix+"-iam-a", prefix+"-iam-b", label, owner, active, prefix+"-iam-role", label, label, permissions)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			account, err := c.GetServiceAccount(context.Background(), accountID)
			if err != nil {
				return err
			}
			if account.Active {
				return fmt.Errorf("destroy left the test account active")
			}
			if _, err = c.GetRBACRole(context.Background(), roleID); !client.IsNotFound(err) {
				return fmt.Errorf("destroy did not delete the test custom role: %v", err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false, true), Check: func(s *terraform.State) error {
				accountID = s.RootModule().Resources["onpager_service_account.iam"].Primary.ID
				roleID = s.RootModule().Resources["onpager_rbac_role.iam"].Primary.ID
				return nil
			}},
			{ResourceName: "onpager_service_account.iam", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_rbac_role.iam", ImportState: true, ImportStateVerify: true},
			{Config: config(true, true), Check: resource.TestCheckResourceAttr("onpager_service_account.iam", "active", "true")},
			{Config: config(true, true), PlanOnly: true},
			{PreConfig: func() {
				if err := c.DeactivateServiceAccount(t.Context(), accountID); err != nil {
					t.Fatal(err)
				}
				current, err := c.GetRBACRole(t.Context(), roleID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = c.UpdateRBACRole(t.Context(), roleID, current.Revision, client.RBACRoleUpdate{Name: prefix + "-drift", PermissionKeys: []string{}})
				if err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, true), Check: resource.TestCheckResourceAttr("onpager_service_account.iam", "active", "true")},
			{Config: config(true, false), Check: resource.TestCheckResourceAttr("onpager_service_account.iam", "active", "false")},
			{ResourceName: "onpager_service_account.iam", ImportState: true, ImportStateVerify: true},
			{Config: config(true, false), PlanOnly: true},
			{PreConfig: func() {
				current, err := c.GetRBACRole(t.Context(), roleID)
				if err != nil {
					t.Fatal(err)
				}
				if err = c.DeleteRBACRole(t.Context(), roleID, current.Revision); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, false), Check: func(s *terraform.State) error {
				newID := s.RootModule().Resources["onpager_rbac_role.iam"].Primary.ID
				if newID == roleID {
					return fmt.Errorf("externally deleted test role was not recreated")
				}
				roleID = newID
				return nil
			}},
			{Config: config(true, false), PlanOnly: true},
		},
	})
}
