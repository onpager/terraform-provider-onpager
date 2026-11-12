package provider_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Never run on shared seeded policy: the explicit opt-in and initial empty
// guard precede writes. Test-created empty roles supply no business authority.
func TestAccTeamRoleDefaults(t *testing.T) {
	if os.Getenv("ONPAGER_ACC_TEAM_ROLE_DEFAULTS_DISPOSABLE") != "1" {
		t.Skip("tenant-wide policy mutation requires ONPAGER_ACC_TEAM_ROLE_DEFAULTS_DISPOSABLE=1 and an initially empty disposable policy")
	}
	c, prefix := routingAccSetup(t)
	initial, err := c.GetTeamRoleDefaults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Members) != 0 || len(initial.Leads) != 0 {
		t.Fatal("refusing to overwrite nonempty unowned team role defaults; supply an explicitly disposable empty policy")
	}
	var memberRole, leadRole string
	ownedRevision := int64(0)
	// Cleanup may clear only an unchanged revision containing this test's own
	// role IDs. It never restores release defaults or rebases a foreign revision.
	t.Cleanup(func() {
		if ownedRevision == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		v, err := c.GetTeamRoleDefaults(ctx)
		if err != nil {
			t.Errorf("cleanup read team role defaults: %v", err)
			return
		}
		if len(v.Members) == 0 && len(v.Leads) == 0 {
			return
		}
		if v.Revision != ownedRevision || !defaultsAccOwnedRoles(v, memberRole, leadRole) {
			t.Error("cleanup refused a changed or foreign team role defaults policy")
			return
		}
		if _, err := c.ReplaceTeamRoleDefaults(ctx, ownedRevision, client.TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}}); err != nil {
			t.Errorf("cleanup clear own team role defaults: %v", err)
		}
	})
	config := func(updated bool) string {
		members, leads := "[onpager_rbac_role.member.id]", "[]"
		if updated {
			members, leads = "[]", "[onpager_rbac_role.lead.id]"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_rbac_role" "member" {
 key=%q
 name="Terraform acceptance member role"
 scope_type="team"
 permission_keys=[]
}
resource "onpager_rbac_role" "lead" {
 key=%q
 name="Terraform acceptance lead role"
 scope_type="team"
 permission_keys=[]
}
resource "onpager_team_role_defaults" "test" {
 members=%s
 leads=%s
}
`, prefix+"_member", prefix+"_lead", members, leads)
	}
	capture := func(s *terraform.State) error {
		if row := s.RootModule().Resources["onpager_rbac_role.member"]; row != nil && row.Primary != nil {
			memberRole = row.Primary.ID
		}
		if row := s.RootModule().Resources["onpager_rbac_role.lead"]; row != nil && row.Primary != nil {
			leadRole = row.Primary.ID
		}
		if row := s.RootModule().Resources["onpager_team_role_defaults.test"]; row != nil && row.Primary != nil && row.Primary.ID == "tenant" {
			revision, err := strconv.ParseInt(row.Primary.Attributes["revision"], 10, 64)
			if err != nil || revision <= 0 {
				return fmt.Errorf("policy did not save a usable own revision")
			}
			ownedRevision = revision
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			v, err := c.GetTeamRoleDefaults(t.Context())
			if err != nil {
				return err
			}
			if len(v.Members) != 0 || len(v.Leads) != 0 {
				return fmt.Errorf("destroy retained tenant-wide dynamic team role grants")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "id", "tenant"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "members.#", "1"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "leads.#", "0"))},
			{ResourceName: "onpager_team_role_defaults.test", ImportState: true, ImportStateId: "tenant", ImportStateVerify: true},
			{Config: config(false), PlanOnly: true},
			{Config: config(true), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "members.#", "0"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "leads.#", "1"), func(*terraform.State) error {
				_, err := c.ReplaceTeamRoleDefaults(t.Context(), initial.Revision, client.TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}})
				var api *client.APIError
				if !errors.As(err, &api) || api.StatusCode != 412 {
					return fmt.Errorf("expected stale team defaults CAS412, got %v", err)
				}
				return nil
			})},
			{Config: config(true), PlanOnly: true},
			{PreConfig: func() {
				v, err := c.GetTeamRoleDefaults(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if v.Revision != ownedRevision || !defaultsAccOwnedRoles(v, memberRole, leadRole) {
					t.Fatal("refusing drift write to a changed or foreign team role defaults policy")
				}
				v, err = c.ReplaceTeamRoleDefaults(t.Context(), ownedRevision, client.TeamRoleDefaultsInput{Members: []string{memberRole}, Leads: []string{leadRole}})
				if err != nil {
					t.Fatal(err)
				}
				ownedRevision = v.Revision
			}, Config: config(true), Check: capture},
			{Config: config(true), PlanOnly: true},
		},
	})
}

func defaultsAccOwnedRoles(v client.TeamRoleDefaults, memberRole, leadRole string) bool {
	for _, ids := range [][]string{v.Members, v.Leads} {
		for _, id := range ids {
			if id == "" || (id != memberRole && id != leadRole) {
				return false
			}
		}
	}
	return true
}
