package provider_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func TestAccLifecycleResources(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("live API acceptance is opt-in")
	}
	endpoint, token, user := os.Getenv("ONPAGER_ACC_ENDPOINT"), os.Getenv("ONPAGER_ACC_TOKEN"), os.Getenv("ONPAGER_ACC_USER_ID")
	if endpoint == "" || token == "" || user == "" {
		t.Fatal("ONPAGER_ACC_ENDPOINT, ONPAGER_ACC_TOKEN and ONPAGER_ACC_USER_ID are required")
	}
	t.Setenv("ONPAGER_ENDPOINT", endpoint)
	t.Setenv("ONPAGER_TOKEN", token)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	c, err := client.New(endpoint, token)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("tfacc-lifecycle-%x", b)
	config := func(updated, active bool) string {
		wait, importance, name := 60, "", prefix+"-chain"
		if updated {
			wait, importance, name = 120, `importance = "important"`, prefix+"-updated"
		}
		return fmt.Sprintf(`
terraform {
 required_providers { onpager = {source = "onpager/onpager"} }
}
provider "onpager" {}
resource "onpager_team" "main" {
 name = %q
 active = %t
}
resource "onpager_service" "main" {
 name = %q
 team_id = onpager_team.main.id
 active = %t
}
resource "onpager_team_membership" "operator" {
 team_id = onpager_team.main.id
 user_id = %q
 lead = %t
}
resource "onpager_escalation_chain" "main" {
 name = %q
 team_id = onpager_team.main.id
 steps = [
  {kind="wait", wait_seconds=%d},
  {kind="notify_user", target_user_id=onpager_team_membership.operator.user_id, %s},
  {kind="notify_channel", channel_sources=["service","team"]},
  {kind="repeat"}
 ]
}
`, prefix+"-team", active, prefix+"-service", active, user, updated, name, wait, importance)
	}
	var team, service, originalChain string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"onpager": providerserver.NewProtocol6WithError(onpager.New("test")())},
		CheckDestroy: func(s *terraform.State) error {
			for _, r := range s.RootModule().Resources {
				switch r.Type {
				case "onpager_team":
					v, err := c.GetTeam(context.Background(), r.Primary.ID)
					if err != nil {
						return err
					}
					if v.Active {
						return fmt.Errorf("team was not disabled")
					}
				case "onpager_service":
					v, err := c.GetService(context.Background(), r.Primary.ID)
					if err != nil {
						return err
					}
					if v.Active {
						return fmt.Errorf("service was not disabled")
					}
				case "onpager_team_membership":
					_, err := c.GetMembership(context.Background(), r.Primary.Attributes["team_id"], r.Primary.Attributes["user_id"])
					if !client.IsNotFound(err) {
						return fmt.Errorf("membership not revoked or read failed: %v", err)
					}
				case "onpager_escalation_chain":
					v, err := c.GetChain(context.Background(), r.Primary.Attributes["team_id"], r.Primary.ID)
					if err != nil {
						return err
					}
					if v.RetiredAt == nil {
						return fmt.Errorf("chain was not archived")
					}
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false, true), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("onpager_escalation_chain.main", "steps.1.importance", "default"),
				func(s *terraform.State) error {
					team = s.RootModule().Resources["onpager_team.main"].Primary.ID
					service = s.RootModule().Resources["onpager_service.main"].Primary.ID
					originalChain = s.RootModule().Resources["onpager_escalation_chain.main"].Primary.ID
					// Recheck the previously reported route-level 404 without
					// configuring channels or sending notifications.
					response, err := c.Do(t.Context(), http.MethodGet, "/v1/teams/"+team+"/oncall-channels?limit=100", nil)
					if err != nil {
						return err
					}
					defer response.Body.Close()
					if response.StatusCode != http.StatusOK {
						return fmt.Errorf("on-call channel catalog returned HTTP %d", response.StatusCode)
					}
					return nil
				},
			)},
			{ResourceName: "onpager_team_membership.operator", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_escalation_chain.main", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
				r := s.RootModule().Resources["onpager_escalation_chain.main"]
				return r.Primary.Attributes["team_id"] + "/" + r.Primary.ID, nil
			}},
			{Config: config(true, true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_escalation_chain.main", "steps.0.wait_seconds", "120"), resource.TestCheckResourceAttr("onpager_escalation_chain.main", "steps.1.importance", "important"), resource.TestCheckResourceAttr("onpager_team_membership.operator", "lead", "true"))},
			{PreConfig: func() {
				if err := c.RemoveMembership(t.Context(), team, user); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, true), Check: resource.TestCheckResourceAttr("onpager_team_membership.operator", "lead", "true")},
			{Config: config(true, false), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team.main", "active", "false"), resource.TestCheckResourceAttr("onpager_service.main", "active", "false"))},
			{ResourceName: "onpager_service.main", ImportState: true, ImportStateVerify: true},
			{Config: config(true, true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team.main", "active", "true"), resource.TestCheckResourceAttr("onpager_service.main", "active", "true"))},
			{PreConfig: func() {
				if err := c.DisableService(t.Context(), service); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, true), Check: resource.TestCheckResourceAttr("onpager_service.main", "active", "true")},
			{Config: strings.Replace(config(true, true), `importance = "important"`, "", 1), Check: resource.TestCheckResourceAttr("onpager_escalation_chain.main", "steps.1.importance", "default")},
			{Config: strings.Replace(config(true, true), `importance = "important"`, "", 1), PlanOnly: true},
			{PreConfig: func() {
				v, err := c.GetChain(t.Context(), team, originalChain)
				if err != nil {
					t.Fatal(err)
				}
				if err = c.ArchiveChain(t.Context(), team, originalChain, v.ETag); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, true), Check: func(s *terraform.State) error {
				if s.RootModule().Resources["onpager_escalation_chain.main"].Primary.ID == originalChain {
					return fmt.Errorf("externally archived chain was not replaced")
				}
				old, err := c.GetChain(t.Context(), team, originalChain)
				if err != nil {
					return err
				}
				if old.RetiredAt == nil {
					return fmt.Errorf("old chain history missing")
				}
				return nil
			}},
		},
	})
}

// Regression for API-004: parent disable and child rename must coexist in a
// single Terraform apply without metadata revision preconditions.
func TestAccParentDisableAndServiceRename(t *testing.T) {
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
	c, err := client.New(endpoint, token)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	if _, err = rand.Read(b); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("tfacc-unversioned-%x", b)
	config := func(active bool) string {
		name := prefix + "-service"
		if !active {
			name = prefix + "-renamed"
		}
		return fmt.Sprintf(`
terraform {
 required_providers { onpager = {source = "onpager/onpager"} }
}
provider "onpager" {}
resource "onpager_team" "main" {
 name = %q
 active = %t
}
resource "onpager_service" "main" {
 name = %q
 team_id = onpager_team.main.id
 active = %t
}
`, prefix+"-team", active, name, active)
	}
	var team, service string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"onpager": providerserver.NewProtocol6WithError(onpager.New("test")())},
		CheckDestroy: func(_ *terraform.State) error {
			gotTeam, err := c.GetTeam(context.Background(), team)
			if err != nil {
				return err
			}
			gotService, err := c.GetService(context.Background(), service)
			if err != nil {
				return err
			}
			if gotTeam.Active || gotService.Active {
				return fmt.Errorf("destroy did not disable retained team/service")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(true), Check: func(s *terraform.State) error {
				team = s.RootModule().Resources["onpager_team.main"].Primary.ID
				service = s.RootModule().Resources["onpager_service.main"].Primary.ID
				return nil
			}},
			{Config: config(false), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("onpager_team.main", "active", "false"),
				resource.TestCheckResourceAttr("onpager_service.main", "active", "false"),
				resource.TestCheckResourceAttr("onpager_service.main", "name", prefix+"-renamed"),
				func(s *terraform.State) error {
					if s.RootModule().Resources["onpager_team.main"].Primary.ID != team || s.RootModule().Resources["onpager_service.main"].Primary.ID != service {
						return fmt.Errorf("disable/rename replaced an existing UUID")
					}
					return nil
				},
			)},
			{Config: config(false), PlanOnly: true},
		},
	})
}
