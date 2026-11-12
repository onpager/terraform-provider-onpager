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

func TestAccFoundationResources(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run against an explicitly configured local OnPager API")
	}
	endpoint, token := os.Getenv("ONPAGER_ACC_ENDPOINT"), os.Getenv("ONPAGER_ACC_TOKEN")
	if endpoint == "" || token == "" {
		t.Fatal("set ONPAGER_ACC_ENDPOINT and ONPAGER_ACC_TOKEN")
	}
	t.Setenv("ONPAGER_ENDPOINT", endpoint)
	t.Setenv("ONPAGER_TOKEN", token)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	c, err := client.New(endpoint, token)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("tfacc-%x", suffix)
	var team, service, originalService string
	config := func(updated bool) string {
		alphaName, serviceName, owner := prefix+"-alpha", prefix+"-service", "alpha"
		if updated {
			alphaName, serviceName, owner = prefix+"-renamed", prefix+"-payments", "beta"
		}
		return fmt.Sprintf(`
terraform {
  required_providers {
    onpager = { source = "onpager/onpager" }
  }
}
provider "onpager" {}
resource "onpager_team" "alpha" { name = %q }
resource "onpager_team" "beta" { name = %q }
resource "onpager_service" "main" {
 name = %q
 team_id = onpager_team.%s.id
}
`, alphaName, prefix+"-beta", serviceName, owner)
	}
	ctx := t.Context()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"onpager": providerserver.NewProtocol6WithError(onpager.New("test")())},
		CheckDestroy: func(s *terraform.State) error {
			for name, r := range s.RootModule().Resources {
				switch r.Type {
				case "onpager_team":
					got, err := c.GetTeam(context.Background(), r.Primary.ID)
					if err != nil {
						return fmt.Errorf("read retained %s: %w", name, err)
					}
					if got.Active {
						return fmt.Errorf("destroy did not disable %s", name)
					}
				case "onpager_service":
					got, err := c.GetService(context.Background(), r.Primary.ID)
					if err != nil {
						return err
					}
					if got.Active {
						return fmt.Errorf("destroy did not disable %s", name)
					}
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("onpager_team.alpha", "active", "true"),
				func(s *terraform.State) error {
					team = s.RootModule().Resources["onpager_team.alpha"].Primary.ID
					service = s.RootModule().Resources["onpager_service.main"].Primary.ID
					originalService = service
					return nil
				},
			)},
			{ResourceName: "onpager_team.alpha", ImportState: true, ImportStateVerify: true},
			{ResourceName: "onpager_service.main", ImportState: true, ImportStateVerify: true},
			{Config: config(true), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttrPair("onpager_service.main", "team_id", "onpager_team.beta", "id"),
				resource.TestCheckResourceAttr("onpager_service.main", "name", prefix+"-payments"),
				func(s *terraform.State) error {
					if s.RootModule().Resources["onpager_service.main"].Primary.ID != originalService {
						return fmt.Errorf("transfer replaced the service UUID")
					}
					return nil
				},
			)},
			{PreConfig: func() {
				if _, err := c.RenameTeam(ctx, team, prefix+"-external"); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("onpager_team.alpha", "name", prefix+"-renamed"),
			)},
			{Config: config(true), PlanOnly: true},
			{PreConfig: func() {
				if err := c.DisableService(ctx, service); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true), Check: resource.TestCheckResourceAttr("onpager_service.main", "active", "true")},
		},
	})
}
