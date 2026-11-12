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

// All targets belong to this test. The predicate is false, and no alert,
// admission, preview, delivery or preexisting configuration is touched.
func TestAccSilenceLifecycle(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var teams []string
	for i := range 2 {
		team, e := c.CreateTeam(t.Context(), fmt.Sprintf("%s-silence-%d", prefix, i))
		if e != nil {
			t.Fatal(e)
		}
		teams = append(teams, team.ID)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if e := c.DisableTeam(ctx, team.ID); e != nil {
				t.Errorf("disable owned silence team: %v", e)
			}
		})
	}
	var id string
	t.Cleanup(func() {
		if id == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		v, e := c.GetSilence(ctx, id)
		if client.IsNotFound(e) {
			return
		}
		if e == nil {
			e = c.DeleteSilence(ctx, id, v.Revision)
		}
		if e != nil {
			t.Errorf("delete owned silence: %v", e)
		}
	})
	config := func(updated bool) string {
		targets := fmt.Sprintf("[%q, %q]", teams[0], teams[1])
		comment := ""
		start := "2024-01-01T00:00:00.123456Z"
		end := "2024-01-02T00:00:00.654321Z"
		if updated {
			targets = fmt.Sprintf("[%q]", teams[1])
			comment = "updated"
			start = "2037-01-01T00:00:00.123456Z"
			end = "2037-01-02T00:00:00.654321Z"
		}
		return routingAccHeader + fmt.Sprintf(`resource "onpager_silence" "test" {
 scope = "team"
 team_ids = %s
 expression = "false"
 comment = %q
 starts_at = %q
 ends_at = %q
}
`, targets, comment, start, end)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, e := c.GetSilence(ctx, id)
		if !client.IsNotFound(e) {
			return fmt.Errorf("silence DELETE did not remove exact UUID: %v", e)
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_silence.test", "status", "expired"), resource.TestCheckResourceAttr("onpager_silence.test", "team_ids.#", "2"), func(s *terraform.State) error {
			id = s.RootModule().Resources["onpager_silence.test"].Primary.ID
			return nil
		})},
		{ResourceName: "onpager_silence.test", ImportState: true, ImportStateVerify: true},
		{Config: config(false), PlanOnly: true},
		{Config: config(true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_silence.test", "team_ids.#", "1"), resource.TestCheckResourceAttr("onpager_silence.test", "comment", "updated"), resource.TestCheckResourceAttr("onpager_silence.test", "status", "scheduled"))},
		{ResourceName: "onpager_silence.test", ImportState: true, ImportStateVerify: true},
		{PreConfig: func() {
			v, e := c.GetSilence(t.Context(), id)
			if e != nil {
				t.Fatal(e)
			}
			in := v.SilenceInput
			in.Comment = "external drift"
			if _, e = c.UpdateSilence(t.Context(), id, v.Revision, in); e != nil {
				t.Fatal(e)
			}
		}, Config: config(true), Check: resource.TestCheckResourceAttr("onpager_silence.test", "comment", "updated")},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			v, e := c.GetSilence(t.Context(), id)
			if e == nil {
				e = c.DeleteSilence(t.Context(), id, v.Revision)
			}
			if e != nil {
				t.Fatal(e)
			}
		}, Config: config(true), Check: func(s *terraform.State) error {
			next := s.RootModule().Resources["onpager_silence.test"].Primary.ID
			if id == next {
				return fmt.Errorf("externally deleted silence was not recreated")
			}
			id = next
			return nil
		}},
		{PreConfig: func() {
			if e := c.DisableTeam(t.Context(), teams[1]); e != nil {
				t.Fatal(e)
			}
		}, Config: config(true), PlanOnly: true},
	}})
}
