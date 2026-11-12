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

// No rotations, memberships or destinations are configured: this lifecycle is passive.
func TestAccScheduleHandover(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var team, schedule, sibling string
	var siblingRevision int64
	config := func(active, enabled bool) string {
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "handover" {
 name = %q
 active = %t
}


resource "onpager_schedule" "handover" {
 team_id = onpager_team.handover.id
 name = %q
 enabled = %t
}
resource "onpager_schedule_handover" "main" {
 team_id = onpager_team.handover.id
 schedule_id = onpager_schedule.handover.id
 enabled = false
 oncall_channel_ids = []
}
`, prefix+"-handover", active, prefix+"-schedule", enabled)
	}
	readSibling := func() error {
		v, err := c.GetScheduleHandover(context.Background(), team, sibling)
		if err != nil {
			return err
		}
		if v.Revision != siblingRevision || v.Enabled || len(v.OncallChannelIDs) != 0 {
			return fmt.Errorf("unmanaged sibling handover changed")
		}
		return nil
	}
	t.Cleanup(func() {
		if sibling != "" {
			v, err := c.GetSchedule(context.Background(), sibling)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err = c.DisableSchedule(context.Background(), sibling, v.Revision); err != nil {
				t.Error(err)
			}
		}
	})
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		v, err := c.GetScheduleHandover(context.Background(), team, schedule)
		if err != nil {
			return err
		}
		if v.Revision < 1 || v.Enabled || len(v.OncallChannelIDs) != 0 {
			return fmt.Errorf("handover destroy did not confirm canonical cleanup")
		}
		return readSibling()
	}, Steps: []resource.TestStep{
		{Config: config(true, true), Check: func(s *terraform.State) error {
			team = s.RootModule().Resources["onpager_team.handover"].Primary.ID
			schedule = s.RootModule().Resources["onpager_schedule.handover"].Primary.ID
			v, err := c.CreateSchedule(context.Background(), team, prefix+"-sibling", nil)
			if err != nil {
				return err
			}
			sibling = v.ID
			h, err := c.PutScheduleHandover(context.Background(), team, sibling, client.ScheduleHandover{})
			siblingRevision = h.Revision
			return err
		}},
		{ResourceName: "onpager_schedule_handover.main", ImportState: true, ImportStateVerify: true},
		{Config: config(true, true), PlanOnly: true},
		{Config: config(true, false), Check: func(*terraform.State) error { return readSibling() }},
		{Config: config(false, false), Check: func(*terraform.State) error { return readSibling() }},
		{ResourceName: "onpager_schedule_handover.main", ImportState: true, ImportStateVerify: true},
		{Config: config(false, false), PlanOnly: true},
	}})
}

// Channel-selection drift needs an explicitly supplied passive conversation
// target. Handover stays disabled and the delivery fixture is never modified.
func TestAccScheduleHandoverChannels(t *testing.T) {
	c, prefix := routingAccSetup(t)
	channel, target := os.Getenv("ONPAGER_ACC_DELIVERY_CHANNEL_ID"), os.Getenv("ONPAGER_ACC_DELIVERY_TARGET_ID")
	if channel == "" || target == "" {
		t.Skip("handover selection update/drift needs explicit passive ONPAGER_ACC_DELIVERY_CHANNEL_ID and ONPAGER_ACC_DELIVERY_TARGET_ID; no target discovery is performed")
	}
	var team, schedule, binding string
	config := func(selected, active bool) string {
		channels := "[]"
		if selected {
			channels = "[onpager_team_oncall_channel.handover.id]"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "handover" {
 name = %q
 active = %t
}
resource "onpager_schedule" "handover" {
 team_id = onpager_team.handover.id
 name = %q
 enabled = %t
}
resource "onpager_team_oncall_channel" "handover" {
 team_id = onpager_team.handover.id
 delivery_channel_id = %q
 target_id = %q
}
resource "onpager_schedule_handover" "main" {
 team_id = onpager_team.handover.id
 schedule_id = onpager_schedule.handover.id
 enabled = false
 oncall_channel_ids = %s
 depends_on = [onpager_team_oncall_channel.handover]
}
`, prefix+"-handover-channels", active, prefix+"-schedule", active, channel, target, channels)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		v, err := c.GetScheduleHandover(context.Background(), team, schedule)
		if err != nil {
			return err
		}
		if v.Enabled || len(v.OncallChannelIDs) != 0 {
			return fmt.Errorf("retirement retained handover selections")
		}
		if _, err = c.GetChannelBinding(context.Background(), "team_oncall", team, binding); !client.IsNotFound(err) {
			return fmt.Errorf("test-owned channel binding was not removed: %v", err)
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(true, true), Check: func(s *terraform.State) error {
			team = s.RootModule().Resources["onpager_team.handover"].Primary.ID
			schedule = s.RootModule().Resources["onpager_schedule.handover"].Primary.ID
			binding = s.RootModule().Resources["onpager_team_oncall_channel.handover"].Primary.ID
			return nil
		}},
		{ResourceName: "onpager_schedule_handover.main", ImportState: true, ImportStateVerify: true},
		{Config: config(false, true), Check: resource.TestCheckResourceAttr("onpager_schedule_handover.main", "oncall_channel_ids.#", "0")},
		{Config: config(true, true)},
		{PreConfig: func() {
			v, err := c.GetScheduleHandover(t.Context(), team, schedule)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.PutScheduleHandover(t.Context(), team, schedule, client.ScheduleHandover{Revision: v.Revision}); err != nil {
				t.Fatal(err)
			}
		}, Config: config(true, true), Check: resource.TestCheckResourceAttr("onpager_schedule_handover.main", "oncall_channel_ids.#", "1")},
		{Config: config(true, true), PlanOnly: true},
		{Config: config(true, false)},
		{ResourceName: "onpager_schedule_handover.main", ImportState: true, ImportStateVerify: true},
		{Config: config(true, false), PlanOnly: true},
	}})
}
