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

// Both definitions are disabled and use .invalid destinations. This suite never
// previews, invokes, delivers, discovers targets or changes shared tenant settings.
func TestAccOutgoingWebhooks(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var team, tenantHook, teamHook string
	cleanup := func(ctx context.Context, owner, id string) error {
		if id == "" {
			return nil
		}
		v, e := c.GetOutgoingWebhook(ctx, owner, id)
		if client.IsNotFound(e) {
			return nil
		}
		if e != nil {
			return e
		}
		_, e = c.DisableOutgoingWebhook(ctx, owner, id, v.ETag)
		return e
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if e := cleanup(ctx, "", tenantHook); e != nil {
			t.Errorf("cleanup tenant webhook: %v", e)
		}
		if e := cleanup(ctx, team, teamHook); e != nil {
			t.Errorf("cleanup team webhook: %v", e)
		}
		if team != "" {
			if e := c.DisableTeam(ctx, team); e != nil {
				t.Errorf("cleanup team: %v", e)
			}
		}
	})
	config := func(updated bool) string {
		name := prefix
		body := "{}"
		if updated {
			name += "-updated"
			body = `{"updated":true}`
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "webhook" { name = %q }
resource "onpager_outgoing_webhook" "tenant" {
 purpose = "escalation"
 display_name = %q
 enabled = false
 request = {
  endpoint = "http://terraform-passive.invalid/tenant"
  body_template = %q
  auth = { kind = "bearer", token_secret = "token" }
 }
 secrets = { token = { action = "replace", value = "tfacc-passive-dummy" } }
}
resource "onpager_team_outgoing_webhook" "team" {
 team_id = onpager_team.webhook.id
 purpose = "notification"
 display_name = %q
 enabled = false
 request = {
  endpoint = "http://terraform-passive.invalid/team"
  body_template = "{}"
 }
 recipient_fields = [{ key = "mail", label = "Email", kind = "email", required = false }]
}
`, prefix, name, body, name)
	}
	capture := func(s *terraform.State) error {
		team = s.RootModule().Resources["onpager_team.webhook"].Primary.ID
		tenantHook = s.RootModule().Resources["onpager_outgoing_webhook.tenant"].Primary.ID
		teamHook = s.RootModule().Resources["onpager_team_outgoing_webhook.team"].Primary.ID
		return nil
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(s *terraform.State) error {
		// Capture exact resources even when an apply failed before its check ran.
		for _, v := range []struct {
			name   string
			target *string
		}{{"onpager_team.webhook", &team}, {"onpager_outgoing_webhook.tenant", &tenantHook}, {"onpager_team_outgoing_webhook.team", &teamHook}} {
			if *v.target == "" {
				if row := s.RootModule().Resources[v.name]; row != nil && row.Primary != nil {
					*v.target = row.Primary.ID
				}
			}
		}
		for _, v := range []struct{ owner, id string }{{"", tenantHook}, {team, teamHook}} {
			if v.id == "" {
				continue
			}
			got, e := c.GetOutgoingWebhook(t.Context(), v.owner, v.id)
			if e != nil {
				return e
			}
			if got.Enabled {
				return fmt.Errorf("destroy retained enabled outgoing webhook")
			}
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_outgoing_webhook.tenant", "enabled", "false"), resource.TestCheckResourceAttr("onpager_team_outgoing_webhook.team", "enabled", "false"))},
		{ResourceName: "onpager_outgoing_webhook.tenant", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"request_id", "secrets"}},
		{ResourceName: "onpager_team_outgoing_webhook.team", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"request_id"}, ImportStateIdFunc: func(*terraform.State) (string, error) { return team + "/" + teamHook, nil }},
		{Config: config(true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_outgoing_webhook.tenant", "display_name", prefix+"-updated"), func(*terraform.State) error {
			v, e := c.GetOutgoingWebhook(t.Context(), "", tenantHook)
			if e != nil {
				return e
			}
			_, e = c.UpdateOutgoingWebhook(t.Context(), "", tenantHook, `"1"`, client.OutgoingWebhookInput{Purpose: v.Purpose, DisplayName: v.DisplayName, Enabled: false, Request: v.Request, RecipientFields: v.RecipientFields})
			var api *client.APIError
			if !errors.As(e, &api) || api.StatusCode != 412 {
				return fmt.Errorf("expected own stale CAS rejection, got %v", e)
			}
			return nil
		})},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			v, e := c.GetOutgoingWebhook(t.Context(), "", tenantHook)
			if e != nil {
				t.Fatal(e)
			}
			_, e = c.UpdateOutgoingWebhook(t.Context(), "", tenantHook, v.ETag, client.OutgoingWebhookInput{Purpose: v.Purpose, DisplayName: prefix + "-drift", Enabled: false, Request: v.Request, RecipientFields: v.RecipientFields})
			if e != nil {
				t.Fatal(e)
			}
		}, Config: config(true), Check: resource.TestCheckResourceAttr("onpager_outgoing_webhook.tenant", "display_name", prefix+"-updated")},
		{Config: config(true), PlanOnly: true},
	}})
}
