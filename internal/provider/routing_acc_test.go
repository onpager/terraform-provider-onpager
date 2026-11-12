package provider_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func routingAccSetup(t *testing.T) (*client.Client, string) {
	t.Helper()
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("live API acceptance is opt-in")
	}
	endpoint, token := os.Getenv("ONPAGER_ACC_ENDPOINT"), os.Getenv("ONPAGER_ACC_TOKEN")
	if endpoint == "" || token == "" {
		t.Fatal("ONPAGER_ACC_ENDPOINT and ONPAGER_ACC_TOKEN are required")
	}
	t.Setenv("ONPAGER_ENDPOINT", endpoint)
	t.Setenv("ONPAGER_TOKEN", token)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	c, e := client.New(endpoint, token)
	if e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 6)
	if _, e = rand.Read(b); e != nil {
		t.Fatal(e)
	}
	return c, fmt.Sprintf("tfacc-routing-%x", b)
}
func routingAccFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){"onpager": providerserver.NewProtocol6WithError(onpager.New("test")())}
}

const routingAccHeader = `
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {}
`

func TestAccRoutingDefinitions(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var team, serviceID, rule string
	config := func(updated, active bool) string {
		source := "false"
		if updated {
			source = "false || false"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "routing" {
 name = %q
 active = %t
}
resource "onpager_service" "routing" {
 name = %q
 team_id = onpager_team.routing.id
 active = %t
}
resource "onpager_escalation_chain" "routing" {
 name = %q
 team_id = onpager_team.routing.id
 steps = []
}
resource "onpager_team_alert_filter" "routing" {
 team_id = onpager_team.routing.id
 cel_source = %q
 active = %t
}
resource "onpager_service_alert_filter" "routing" {
 service_id = onpager_service.routing.id
 cel_source = %q
 active = %t
}
resource "onpager_escalation_rule" "routing" {
 team_id = onpager_team.routing.id
 name = %q
 cel_source = %q
 chain_id = onpager_escalation_chain.routing.id
 active = %t
}
`, prefix+"-team", active, prefix+"-service", active, prefix+"-chain", source, active, source, active, prefix+"-rule", source, active)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(s *terraform.State) error {
			for _, v := range s.RootModule().Resources {
				switch v.Type {
				case "onpager_team_alert_filter", "onpager_service_alert_filter":
					kind, owner := "team", v.Primary.Attributes["team_id"]
					if v.Type == "onpager_service_alert_filter" {
						kind, owner = "service", v.Primary.Attributes["service_id"]
					}
					got, e := c.GetAlertFilter(context.Background(), kind, owner)
					if e != nil {
						return e
					}
					if got.Active {
						return fmt.Errorf("destroy left %s active", v.Type)
					}
				case "onpager_escalation_rule":
					got, e := c.GetEscalationRule(context.Background(), v.Primary.Attributes["team_id"], v.Primary.ID)
					if e != nil {
						return e
					}
					if got.Active {
						return fmt.Errorf("destroy left rule active")
					}
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false, true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_alert_filter.routing", "active", "true"), func(s *terraform.State) error {
				team = s.RootModule().Resources["onpager_team.routing"].Primary.ID
				serviceID = s.RootModule().Resources["onpager_service.routing"].Primary.ID
				rule = s.RootModule().Resources["onpager_escalation_rule.routing"].Primary.ID
				return nil
			})},
			{ResourceName: "onpager_team_alert_filter.routing", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_service_alert_filter.routing", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_escalation_rule.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(*terraform.State) (string, error) { return team + "/" + rule, nil }},
			{Config: config(true, true)},
			{Config: config(true, true), PlanOnly: true},
			{PreConfig: func() {
				if _, e := c.PublishAlertFilter(t.Context(), "team", team, client.AlertFilterInput{CELSource: "false", Active: false}); e != nil {
					t.Fatal(e)
				}
				if _, e := c.PublishAlertFilter(t.Context(), "service", serviceID, client.AlertFilterInput{CELSource: "false", Active: false}); e != nil {
					t.Fatal(e)
				}
				current, e := c.GetEscalationRule(t.Context(), team, rule)
				if e != nil {
					t.Fatal(e)
				}
				current.Active = false
				current.CELSource = "false"
				if _, e = c.PublishEscalationRule(t.Context(), team, rule, current.EscalationRuleInput); e != nil {
					t.Fatal(e)
				}
			}, Config: config(true, true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_alert_filter.routing", "cel_source", "false || false"), resource.TestCheckResourceAttr("onpager_escalation_rule.routing", "active", "true"))},
			{Config: config(false, false)},
			{Config: config(false, false), PlanOnly: true},
			{ResourceName: "onpager_escalation_rule.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(*terraform.State) (string, error) { return team + "/" + rule, nil }},
			{ResourceName: "onpager_team_alert_filter.routing", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_service_alert_filter.routing", ImportState: true, ImportStateVerify: true},
		},
	})
}

// Existing fixtures are only read/referenced: this test owns and removes unique
// team/service bindings, never the delivery channel or targets themselves.
func TestAccRoutingCatalogBindings(t *testing.T) {
	c, prefix := routingAccSetup(t)
	channel, target, alternate := os.Getenv("ONPAGER_ACC_DELIVERY_CHANNEL_ID"), os.Getenv("ONPAGER_ACC_DELIVERY_TARGET_ID"), os.Getenv("ONPAGER_ACC_DELIVERY_TARGET_ALT_ID")
	if channel == "" || target == "" || alternate == "" {
		t.Skip("binding lifecycle needs explicit enabled, globally accessible conversation-capable local delivery fixtures: ONPAGER_ACC_DELIVERY_CHANNEL_ID, ONPAGER_ACC_DELIVERY_TARGET_ID and ONPAGER_ACC_DELIVERY_TARGET_ALT_ID; no target discovery is performed")
	}
	if target == alternate {
		t.Fatal("binding replacement coverage requires two distinct local target UUIDs")
	}
	var team, serviceID, teamBinding, serviceBinding, oncallBinding, initialOncall string
	config := func(updated, active bool) string {
		selected := target
		if updated {
			selected = alternate
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "routing" {
 name = %q
 active = %t
}
resource "onpager_service" "routing" {
 name = %q
 team_id = onpager_team.routing.id
 active = %t
}
resource "onpager_team_alert_channel" "routing" {
 team_id = onpager_team.routing.id
 delivery_channel_id = %q
 target_id = %q
}
resource "onpager_service_alert_channel" "routing" {
 service_id = onpager_service.routing.id
 delivery_channel_id = %q
 target_id = %q
}
resource "onpager_team_oncall_channel" "routing" {
 team_id = onpager_team.routing.id
 delivery_channel_id = %q
 target_id = %q
}
`, prefix+"-team", active, prefix+"-service", active, channel, selected, channel, selected, channel, selected)
	}
	capture := func(s *terraform.State) error {
		team = s.RootModule().Resources["onpager_team.routing"].Primary.ID
		serviceID = s.RootModule().Resources["onpager_service.routing"].Primary.ID
		teamBinding = s.RootModule().Resources["onpager_team_alert_channel.routing"].Primary.ID
		serviceBinding = s.RootModule().Resources["onpager_service_alert_channel.routing"].Primary.ID
		oncallBinding = s.RootModule().Resources["onpager_team_oncall_channel.routing"].Primary.ID
		return nil
	}
	importID := func(kind string) func(*terraform.State) (string, error) {
		return func(*terraform.State) (string, error) {
			switch kind {
			case "team":
				return team + "/" + teamBinding, nil
			case "service":
				return serviceID + "/" + serviceBinding, nil
			default:
				return team + "/" + oncallBinding, nil
			}
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(s *terraform.State) error {
			for _, v := range s.RootModule().Resources {
				kind, owner := "", ""
				switch v.Type {
				case "onpager_team_alert_channel":
					kind, owner = "team_alert", v.Primary.Attributes["team_id"]
				case "onpager_service_alert_channel":
					kind, owner = "service_alert", v.Primary.Attributes["service_id"]
				case "onpager_team_oncall_channel":
					kind, owner = "team_oncall", v.Primary.Attributes["team_id"]
				default:
					continue
				}
				if _, e := c.GetChannelBinding(context.Background(), kind, owner, v.Primary.ID); !client.IsNotFound(e) {
					return fmt.Errorf("binding destroy must remove only the binding: %v", e)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false, true), Check: func(s *terraform.State) error {
				if e := capture(s); e != nil {
					return e
				}
				initialOncall = oncallBinding
				return nil
			}},
			{ResourceName: "onpager_team_alert_channel.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID("team")},
			{ResourceName: "onpager_service_alert_channel.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID("service")},
			{ResourceName: "onpager_team_oncall_channel.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID("oncall")},
			{Config: config(true, true), Check: func(s *terraform.State) error {
				oldTeam, oldService := teamBinding, serviceBinding
				if e := capture(s); e != nil {
					return e
				}
				if teamBinding != oldTeam || serviceBinding != oldService {
					return fmt.Errorf("alert binding updates replaced UUID")
				}
				if oncallBinding == initialOncall {
					return fmt.Errorf("oncall target change did not replace binding")
				}
				if _, e := c.GetChannelBinding(t.Context(), "team_oncall", team, initialOncall); !client.IsNotFound(e) {
					return fmt.Errorf("replaced oncall binding retained: %v", e)
				}
				return nil
			}},
			{PreConfig: func() {
				if _, e := c.UpdateChannelBinding(t.Context(), "team_alert", team, teamBinding, client.ChannelBindingInput{ChannelID: channel, TargetID: target}); e != nil {
					t.Fatal(e)
				}
				if e := c.DeleteChannelBinding(t.Context(), "service_alert", serviceID, serviceBinding); e != nil {
					t.Fatal(e)
				}
			}, Config: config(true, true), Check: func(s *terraform.State) error {
				oldService := serviceBinding
				if e := capture(s); e != nil {
					return e
				}
				if serviceBinding == oldService {
					return fmt.Errorf("externally removed binding was not recreated")
				}
				return nil
			}},
			{Config: config(true, true), PlanOnly: true},
			{Config: config(true, false)},
			{ResourceName: "onpager_team_alert_channel.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID("team")},
			{ResourceName: "onpager_service_alert_channel.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID("service")},
			{ResourceName: "onpager_team_oncall_channel.routing", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID("oncall")},
			{Config: config(true, false), PlanOnly: true},
		},
	})
}

func TestAccRoutingCreateUnderInactiveOwner(t *testing.T) {
	c, prefix := routingAccSetup(t)
	team, e := c.CreateTeam(t.Context(), prefix+"-inactive")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := c.DisableTeam(context.Background(), team.ID); e != nil {
			t.Errorf("disable fixture team: %v", e)
		}
	})
	svc, e := c.CreateService(t.Context(), team.ID, prefix+"-inactive-service")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := c.DisableService(context.Background(), svc.ID); e != nil {
			t.Errorf("disable fixture service: %v", e)
		}
	})
	chain, e := c.CreateChain(t.Context(), team.ID, client.ChainInput{Name: prefix + "-passive", Steps: []client.ChainStep{}, WorkCalendarReference: "start_time"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		v, e := c.GetChain(context.Background(), team.ID, chain)
		if e == nil {
			e = c.ArchiveChain(context.Background(), team.ID, chain, v.ETag)
		}
		if e != nil {
			t.Errorf("archive fixture chain: %v", e)
		}
	})
	if e = c.DisableTeam(t.Context(), team.ID); e != nil {
		t.Fatal(e)
	}
	cfg := routingAccHeader + fmt.Sprintf(`
resource "onpager_team_alert_filter" "inactive" {
 team_id = %q
 cel_source = "false"
 active = false
}
resource "onpager_service_alert_filter" "inactive" {
 service_id = %q
 cel_source = "false"
 active = false
}
resource "onpager_escalation_rule" "inactive" {
 team_id = %q
 name = %q
 cel_source = "false"
 chain_id = %q
 active = false
}
`, team.ID, svc.ID, team.ID, prefix+"-rule", chain)
	var ruleID string
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		a, e := c.GetAlertFilter(context.Background(), "team", team.ID)
		if e != nil {
			return e
		}
		b, e := c.GetAlertFilter(context.Background(), "service", svc.ID)
		if e != nil {
			return e
		}
		r, e := c.GetEscalationRule(context.Background(), team.ID, ruleID)
		if e != nil {
			return e
		}
		if a.Active || b.Active || r.Active {
			return fmt.Errorf("inactive destroy activated retained configuration")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: cfg, Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_alert_filter.inactive", "active", "false"), resource.TestCheckResourceAttr("onpager_service_alert_filter.inactive", "active", "false"), resource.TestCheckResourceAttr("onpager_escalation_rule.inactive", "active", "false"), func(s *terraform.State) error {
			ruleID = s.RootModule().Resources["onpager_escalation_rule.inactive"].Primary.ID
			return nil
		})},
		{ResourceName: "onpager_team_alert_filter.inactive", ImportState: true, ImportStateVerify: true},
		{ResourceName: "onpager_service_alert_filter.inactive", ImportState: true, ImportStateVerify: true},
		{ResourceName: "onpager_escalation_rule.inactive", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(*terraform.State) (string, error) { return team.ID + "/" + ruleID, nil }},
		{Config: cfg, PlanOnly: true},
	}})
}
