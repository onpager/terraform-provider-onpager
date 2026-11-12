package provider_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestAccRoleAssignment(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var id, first, firstRole string
	config := func(second, active, ownerActive bool) string {
		role := "onpager_rbac_role.first.id"
		if second {
			role = "onpager_rbac_role.second.id"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "assignment" {
 name=%q
 active=%t
}
resource "onpager_service_account" "assignment" {
 display_name=%q
 owner_team_id=onpager_team.assignment.id
 active=%t
}
resource "onpager_rbac_role" "first" {
 key=%q
 name=%q
 scope_type="team"
 permission_keys=["teams.read"]
}
resource "onpager_rbac_role" "second" {
 key=%q
 name=%q
 scope_type="team"
 permission_keys=["teams.read","schedules.read"]
}
resource "onpager_role_assignment" "main" {
 role_id=%s
 target_team_id=onpager_team.assignment.id
 recipient={kind="service_account",id=onpager_service_account.assignment.id}
}
`, prefix, ownerActive, prefix, active, prefix+"-first", prefix+"-first", prefix+"-second", prefix+"-second", role)
	}
	capture := func(s *terraform.State) error {
		id = s.RootModule().Resources["onpager_role_assignment.main"].Primary.ID
		firstRole = s.RootModule().Resources["onpager_rbac_role.first"].Primary.ID
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			if id == "" {
				return nil
			}
			if _, err := c.GetRoleAssignment(context.Background(), id); !client.IsNotFound(err) {
				return fmt.Errorf("assignment destroy did not revoke binding: %v", err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false, true, true), Check: capture},
			{ResourceName: "onpager_role_assignment.main", ImportState: true, ImportStateVerify: true},
			{Config: config(false, true, true), PlanOnly: true},
			{PreConfig: func() {
				first = id
				if err := c.DeleteRoleAssignment(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}, Config: config(false, true, true), Check: func(s *terraform.State) error {
				capture(s)
				if id == first {
					return fmt.Errorf("externally revoked assignment was not recreated")
				}
				return nil
			}},
			{PreConfig: func() { first = id }, Config: config(true, true, true), Check: func(s *terraform.State) error {
				capture(s)
				if id != first {
					return fmt.Errorf("atomic role change replaced binding UUID")
				}
				return nil
			}},
			{PreConfig: func() {
				// Simulate an authorized UI edit of this test-owned UUID and
				// prove a stale expected tuple cannot overwrite it.
				before, err := c.GetRoleAssignment(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				desired := before.RoleAssignmentInput
				desired.RoleID = firstRole
				changed, err := c.UpdateRoleAssignment(t.Context(), id, desired, before.RoleAssignmentInput)
				if err != nil || changed.ID != id {
					t.Fatalf("external same-UUID edit failed: %v", err)
				}
				_, err = c.UpdateRoleAssignment(t.Context(), id, before.RoleAssignmentInput, before.RoleAssignmentInput)
				var api *client.APIError
				if !errors.As(err, &api) || api.StatusCode != 412 {
					t.Fatalf("stale assignment update did not conflict: %v", err)
				}
				current, err := c.GetRoleAssignment(t.Context(), id)
				if err != nil || current.RoleID != firstRole {
					t.Fatalf("stale update changed the external edit: %v", err)
				}
			}, Config: config(true, true, true), Check: func(s *terraform.State) error {
				capture(s)
				if id != first {
					return fmt.Errorf("UI drift reconciliation replaced assignment UUID")
				}
				return nil
			}},
			{Config: config(true, true, true), PlanOnly: true},
			{Config: config(true, false, true)},
			{Config: config(true, false, false)},
			{ResourceName: "onpager_role_assignment.main", ImportState: true, ImportStateVerify: true},
			{Config: config(true, false, false), PlanOnly: true},
		},
	})
}
