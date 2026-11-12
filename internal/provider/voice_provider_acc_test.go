package provider_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Always disabled, with synthetic credentials/campaign IDs. The audited voice
// management handlers validate locally; this test cannot start a call, verify a
// phone, probe an account, or mutate vendor-side configuration.
func TestAccVoiceProvider(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var id string
	ids := map[string]bool{}
	t.Cleanup(func() {
		for id := range ids {
			if err := c.DeleteVoiceProvider(context.Background(), id); err != nil {
				t.Errorf("cleanup disabled voice provider: %v", err)
			}
		}
	})
	config := func(updated bool) string {
		name, campaign := prefix, "tfacc-passive-calls"
		if updated {
			name += "-updated"
			campaign = "tfacc-passive-calls-updated"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_voice_provider" "test" {
 kind = "zvonok"
 display_name = %q
 enabled = false
 configuration = {
  call_campaign_id = %q
  verification_campaign_id = "tfacc-passive-verification"
 }
 secrets = { public_key = { action = "replace", value = "tfacc-synthetic-public-key" } }
}
`, name, campaign)
	}
	capture := func(s *terraform.State) error {
		id = s.RootModule().Resources["onpager_voice_provider.test"].Primary.ID
		ids[id] = true
		return nil
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(s *terraform.State) error {
		if row := s.RootModule().Resources["onpager_voice_provider.test"]; row != nil && row.Primary != nil && row.Primary.ID != "" {
			ids[row.Primary.ID] = true
		}
		for id := range ids {
			_, err := c.GetVoiceProvider(t.Context(), id)
			if !client.IsNotFound(err) {
				return fmt.Errorf("destroy did not confirm voice-provider retirement: %v", err)
			}
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_voice_provider.test", "enabled", "false"), resource.TestCheckResourceAttr("onpager_voice_provider.test", "runtime_status", "disabled"), resource.TestCheckResourceAttr("onpager_voice_provider.test", "configuration.max_text_runes", "500"))},
		{ResourceName: "onpager_voice_provider.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secrets"}},
		{Config: config(true), Check: resource.TestCheckResourceAttr("onpager_voice_provider.test", "display_name", prefix+"-updated")},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			v, err := c.GetVoiceProvider(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.UpdateVoiceProvider(t.Context(), id, client.VoiceProviderInput{DisplayName: prefix + "-drift", Enabled: false, Visibility: v.Visibility, Configuration: v.Configuration, TeamIDs: v.TeamIDs})
			if err != nil {
				t.Fatal(err)
			}
		}, Config: config(true), Check: resource.TestCheckResourceAttr("onpager_voice_provider.test", "display_name", prefix+"-updated")},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			if err := c.DeleteVoiceProvider(t.Context(), id); err != nil {
				t.Fatal(err)
			}
		}, Config: config(true), Check: capture},
	}})
}
