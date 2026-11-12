package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Only unique disabled definitions are created. No alerts, source registration,
// previews, explicit index rebuilds, held decisions or notifications are invoked.
func TestAccInhibitionRule(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var id string
	cleanup := func() error {
		if id == "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		v, e := c.GetInhibitionRule(ctx, id)
		if client.IsNotFound(e) {
			return nil
		}
		if e != nil {
			return e
		}
		return c.DeleteInhibitionRule(ctx, id, v.Revision)
	}
	t.Cleanup(func() {
		if e := cleanup(); e != nil {
			t.Errorf("delete owned inhibition fixture: %v", e)
		}
	})
	config := func(updated bool) string {
		source, action := "false", "keep"
		if updated {
			source, action = "false || false", "release"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_inhibition_rule" "passive" {
 name = %q
 source_expression = %q
 target_expression = "false"
 matchers = [{source = "terraform/source.key", target = "terraform/target.key"}]
 resolved_target_action = %q
 enabled = false
}
`, prefix+"-inhibition", source, action)
	}
	capture := func(s *terraform.State) error {
		id = s.RootModule().Resources["onpager_inhibition_rule.passive"].Primary.ID
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, e := c.GetInhibitionRule(ctx, id); !client.IsNotFound(e) {
				return fmt.Errorf("deleted inhibition configuration remains readable or unconfirmed: %v", e)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_inhibition_rule.passive", "enabled", "false"), resource.TestCheckResourceAttr("onpager_inhibition_rule.passive", "index_state", "ready"))},
			{ResourceName: "onpager_inhibition_rule.passive", ImportState: true, ImportStateVerify: true},
			{Config: config(false), PlanOnly: true},
			{Config: config(true)},
			{Config: config(true), PlanOnly: true},
			{PreConfig: func() {
				v, e := c.GetInhibitionRule(t.Context(), id)
				if e != nil {
					t.Fatal(e)
				}
				v.Name = prefix + "-external"
				v.SourceExpression = "false"
				v.Enabled = false
				if _, e = c.UpdateInhibitionRule(t.Context(), id, v.Revision, v.InhibitionInput); e != nil {
					t.Fatal(e)
				}
			}, Config: config(true), Check: resource.TestCheckResourceAttr("onpager_inhibition_rule.passive", "name", prefix+"-inhibition")},
			{PreConfig: func() {
				if e := cleanup(); e != nil {
					t.Fatal(e)
				}
			}, Config: config(true), Check: capture},
			{ResourceName: "onpager_inhibition_rule.passive", ImportState: true, ImportStateVerify: true},
			{Config: config(true), PlanOnly: true},
		},
	})
}
