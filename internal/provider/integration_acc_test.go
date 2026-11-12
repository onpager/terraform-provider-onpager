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

func TestAccIntegration(t *testing.T) {
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
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("tfacc-integration-%x", suffix)
	config := func(name string, enabled, polling bool) string {
		block := ""
		if polling {
			block = `polling = {enabled = false, base_url="http://127.0.0.1:19090/", interval_seconds=120, auth_kind="none"}`
		}
		return fmt.Sprintf(`
terraform {
 required_providers { onpager = {source="onpager/onpager"} }
}
provider "onpager" {}
resource "onpager_integration" "main" {
 name=%q
 source_kind="alertmanager"
 enabled=%t
 %s
}
`, name, enabled, block)
	}
	var id, originalToken string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"onpager": providerserver.NewProtocol6WithError(onpager.New("test")())},
		CheckDestroy: func(*terraform.State) error {
			_, err := c.GetIntegration(context.Background(), id)
			if !client.IsNotFound(err) {
				return fmt.Errorf("destroy did not retire integration: %v", err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(prefix, false, false), Check: func(s *terraform.State) error {
				r := s.RootModule().Resources["onpager_integration.main"]
				id = r.Primary.ID
				originalToken = r.Primary.Attributes["token"]
				if originalToken == "" {
					return fmt.Errorf("one-time token missing")
				}
				if r.Primary.Attributes["enabled"] != "false" {
					return fmt.Errorf("disabled create failed")
				}
				return nil
			}},
			{ResourceName: "onpager_integration.main", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"token"}},
			{Config: config(prefix+"-renamed", true, true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_integration.main", "polling.enabled", "false"), resource.TestCheckResourceAttr("onpager_integration.main", "polling.base_url", "http://127.0.0.1:19090/"), func(s *terraform.State) error {
				if s.RootModule().Resources["onpager_integration.main"].Primary.Attributes["token"] != originalToken {
					return fmt.Errorf("refresh/update lost one-time token")
				}
				return nil
			})},
			{Config: config(prefix+"-renamed", true, true), PlanOnly: true},
			{PreConfig: func() {
				v, err := c.GetIntegration(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				name := prefix + "-external"
				if _, err = c.UpdateIntegration(t.Context(), id, v.Revision, client.IntegrationPatch{Name: &name}); err != nil {
					t.Fatal(err)
				}
			}, Config: config(prefix+"-renamed", true, true), Check: resource.TestCheckResourceAttr("onpager_integration.main", "name", prefix+"-renamed")},
			{Config: config(prefix+"-renamed", false, false), Check: resource.TestCheckNoResourceAttr("onpager_integration.main", "polling.enabled")},
			{Config: config(prefix+"-renamed", false, false), PlanOnly: true},
		},
	})
}
