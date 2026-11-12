package provider_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// This suite owns every group/source it mutates. The existing human is only
// referenced by a new manual source; no profile, IdP, alert or delivery changes.
func TestAccGroups(t *testing.T) {
	c, prefix := routingAccSetup(t)
	user := os.Getenv("ONPAGER_ACC_USER_ID")
	if user == "" {
		t.Fatal("ONPAGER_ACC_USER_ID must identify an existing test human")
	}
	var group, mapping string
	// Best-effort API cleanup also runs after a failing Terraform test step. Never
	// enumerate or delete anything outside the exact identities captured here.
	t.Cleanup(func() {
		if group == "" {
			return
		}
		ctx := context.Background()
		if mapping != "" {
			v, e := c.GetOIDCGroupMapping(ctx, group, mapping)
			if e == nil {
				if e = c.DeleteOIDCGroupMapping(ctx, group, mapping, v.Revision); e != nil {
					t.Errorf("cleanup test mapping: %v", e)
				}
			} else if !client.IsNotFound(e) {
				t.Errorf("read test mapping for cleanup: %v", e)
			}
		}
		if e := c.RemoveGroupMembership(ctx, group, user); e != nil {
			t.Errorf("cleanup test manual source: %v", e)
		}
		if v, e := c.GetGroup(ctx, group); e == nil {
			if e = c.DeleteGroup(ctx, group, v.Revision); e != nil {
				t.Errorf("cleanup test group: %v", e)
			}
		} else if !client.IsNotFound(e) {
			t.Errorf("read test group for cleanup: %v", e)
		}
	})
	config := func(updated bool) string {
		name, description, external, oidcClient := prefix, "Terraform group fixture", prefix, "tfacc"
		if updated {
			name += "-updated"
			description = ""
			external = " " + prefix + "-Updated "
			oidcClient = "tfacc-updated"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_group" "test" {
 key = %q
 name = %q
 description = %q
}
resource "onpager_group_membership" "test" {
 group_id = onpager_group.test.id
 user_id = %q
}
resource "onpager_oidc_group_mapping" "test" {
 group_id = onpager_group.test.id
 provider_slug = "tfacc_unconfigured"
 issuer = "https://tfacc.invalid"
 client_id = %q
 external_key = %q
}
`, prefix, name, description, user, oidcClient, external)
	}
	capture := func(s *terraform.State) error {
		group = s.RootModule().Resources["onpager_group.test"].Primary.ID
		mapping = s.RootModule().Resources["onpager_oidc_group_mapping.test"].Primary.ID
		return nil
	}
	expectStatus := func(err error, status int) error {
		var e *client.APIError
		if !errors.As(err, &e) || e.StatusCode != status {
			return fmt.Errorf("expected API HTTP %d, got %v", status, err)
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(s *terraform.State) error {
			// A failed initial apply may have created resources before its check
			// callback ran. Capture only identities present in this test's state.
			if group == "" {
				if v := s.RootModule().Resources["onpager_group.test"]; v != nil && v.Primary != nil {
					group = v.Primary.ID
				}
			}
			if mapping == "" {
				if v := s.RootModule().Resources["onpager_oidc_group_mapping.test"]; v != nil && v.Primary != nil {
					mapping = v.Primary.ID
				}
			}
			if group == "" {
				return nil
			}
			if _, e := c.GetGroup(context.Background(), group); !client.IsNotFound(e) {
				return fmt.Errorf("destroy did not delete test group: %v", e)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_group.test", "description", "Terraform group fixture"))},
			{ResourceName: "onpager_group.test", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_group_membership.test", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_oidc_group_mapping.test", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(*terraform.State) (string, error) { return group + "/" + mapping, nil }},
			{Config: config(true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_group.test", "description", ""), resource.TestCheckResourceAttr("onpager_oidc_group_mapping.test", "external_key", " "+prefix+"-Updated "), func(*terraform.State) error {
				current, e := c.GetOIDCGroupMapping(t.Context(), group, mapping)
				if e != nil {
					return e
				}
				_, e = c.UpdateOIDCGroupMapping(t.Context(), group, mapping, current.Revision-1, client.OIDCGroupMappingInput{ProviderSlug: current.ProviderSlug, Issuer: current.Issuer, ClientID: current.ClientID, ExternalKey: current.ExternalKey})
				if e = expectStatus(e, 412); e != nil {
					return e
				}
				// Live group in-use must fail without unlinking sources.
				owner, e := c.GetGroup(t.Context(), group)
				if e != nil {
					return e
				}
				return expectStatus(c.DeleteGroup(t.Context(), group, owner.Revision), 409)
			})},
			{Config: config(true), PlanOnly: true},
			{PreConfig: func() {
				drift := "External drift"
				if _, e := c.UpdateGroup(t.Context(), group, client.GroupUpdate{Name: &drift, Description: &drift}); e != nil {
					t.Fatal(e)
				}
				current, e := c.GetOIDCGroupMapping(t.Context(), group, mapping)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = c.UpdateOIDCGroupMapping(t.Context(), group, mapping, current.Revision, client.OIDCGroupMappingInput{ProviderSlug: current.ProviderSlug, Issuer: current.Issuer, ClientID: "tfacc-drift", ExternalKey: prefix + "-drift"}); e != nil {
					t.Fatal(e)
				}
				if e = c.RemoveGroupMembership(t.Context(), group, user); e != nil {
					t.Fatal(e)
				}
			}, Config: config(true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_group.test", "name", prefix+"-updated"), resource.TestCheckResourceAttr("onpager_group.test", "description", ""), resource.TestCheckResourceAttr("onpager_oidc_group_mapping.test", "client_id", "tfacc-updated"))},
			{Config: config(true), PlanOnly: true},
		},
	})
}
