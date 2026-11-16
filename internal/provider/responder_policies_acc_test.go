package provider_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Every publication belongs to a fresh test-owned team. No responder request,
// message or operational call is created; an existing user is only a member of
// that disposable team and their profile/credentials are never changed.
func TestAccTeamResponderPolicies(t *testing.T) {
	c, prefix := routingAccSetup(t)
	user := os.Getenv("ONPAGER_ACC_USER_ID")
	if user == "" {
		t.Fatal("an existing active ONPAGER_ACC_USER_ID is required")
	}
	var team string
	var firstRevision int64
	config := func(updated bool) string {
		wait, limit := 5, 3
		if updated {
			wait, limit = 10, 7
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "policies" { name = %q }
resource "onpager_team_membership" "subject" {
 team_id = onpager_team.policies.id
 user_id = %q
}
resource "onpager_escalation_chain" "responders" {
 team_id = onpager_team.policies.id
 name = %q
 steps = [
  { kind = "notify_user", target_user_id = onpager_team_membership.subject.user_id },
  { kind = "wait", wait_seconds = %d }
 ]
}
resource "onpager_team_responder_policy" "default" {
 team_id = onpager_team.policies.id
 importance = "default"
 strategy = "team_fanout"
}
resource "onpager_team_responder_policy" "important" {
 team_id = onpager_team.policies.id
 importance = "important"
 strategy = "team_chain"
 chain_id = onpager_escalation_chain.responders.id
 publication_trigger = onpager_escalation_chain.responders.etag
}
resource "onpager_team_responder_guard_policy" "limit" {
 team_id = onpager_team.policies.id
 max_open_requests_per_subject = %d
}
`, prefix+"-policies", user, prefix+"-responders", wait, limit)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		for _, importance := range []string{"default", "important"} {
			v, err := c.GetTeamResponderPolicy(context.Background(), team, importance)
			if err != nil {
				return err
			}
			if v.Configured {
				return fmt.Errorf("destroy left the test-owned responder slot configured")
			}
		}
		v, err := c.GetTeamResponderGuardPolicy(context.Background(), team)
		if err != nil {
			return err
		}
		if !v.IsDefault {
			return fmt.Errorf("destroy did not reset the test-owned guard override")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: func(s *terraform.State) error {
			team = s.RootModule().Resources["onpager_team.policies"].Primary.ID
			v, err := c.GetTeamResponderPolicy(t.Context(), team, "important")
			if err != nil {
				return err
			}
			firstRevision = v.Revision
			if !v.Configured || v.Strategy != "team_chain" || len(v.ChainSteps) != 2 {
				return fmt.Errorf("initial responder snapshot was not published")
			}
			return nil
		}},
		{ResourceName: "onpager_team_responder_policy.default", ImportState: true, ImportStateVerify: true},
		{ResourceName: "onpager_team_responder_policy.important", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"publication_trigger"}},
		{ResourceName: "onpager_team_responder_guard_policy.limit", ImportState: true, ImportStateVerify: true},
		{Config: config(false), PlanOnly: true},
		{Config: config(true), Check: func(*terraform.State) error {
			v, err := c.GetTeamResponderPolicy(t.Context(), team, "important")
			if err != nil {
				return err
			}
			if v.Revision <= firstRevision || len(v.ChainSteps) != 2 || v.ChainSteps[1].WaitSeconds != 10 {
				return fmt.Errorf("source chain edit did not republish the explicit policy snapshot")
			}
			return nil
		}},
		{PreConfig: func() {
			if _, err := c.PublishTeamResponderGuardPolicy(t.Context(), team, client.TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: 9}); err != nil {
				t.Fatal(err)
			}
		}, Config: config(true), Check: resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.limit", "max_open_requests_per_subject", "7")},
		{Config: config(true), PlanOnly: true},
	}})
}

// This singleton cannot be isolated by a fresh team. It stays explicitly gated
// and requires an initially empty disposable routing configuration.
func TestAccRoutingSettings(t *testing.T) {
	if os.Getenv("ONPAGER_ACC_ROUTING_SETTINGS_DISPOSABLE") != "1" {
		t.Skip("tenant routing mutation requires ONPAGER_ACC_ROUTING_SETTINGS_DISPOSABLE=1 and an initially empty disposable default route")
	}
	c, prefix := routingAccSetup(t)
	initial, err := c.GetRoutingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if initial.DefaultRoute != nil {
		t.Fatal("refusing to overwrite nonempty unowned tenant routing settings")
	}
	config := func(clear bool) string {
		route := `{target_kind="team",target_id=onpager_team.fallback.id}`
		if clear {
			route = "null"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "fallback" { name = %q }
resource "onpager_routing_settings" "test" { default_route = %s }
`, prefix+"-fallback", route)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		v, err := c.GetRoutingSettings(t.Context())
		if err != nil {
			return err
		}
		if v.DefaultRoute != nil {
			return fmt.Errorf("destroy left test-owned default routing configured")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false)},
		{ResourceName: "onpager_routing_settings.test", ImportState: true, ImportStateVerify: true},
		{Config: config(false), PlanOnly: true},
		{Config: config(true)},
		{Config: config(true), PlanOnly: true},
	}})
}
