package provider_test

import (
	"crypto/rand"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// The test only reads its explicitly supplied global bot. Every mutable
// object belongs to a fresh local user or team, or has a unique test address.
func TestAccProvisionDelivery(t *testing.T) {
	botID := os.Getenv("ONPAGER_ACC_PROVISION_PACHCA_BOT_ID")
	if botID == "" || os.Getenv("ONPAGER_ACC_LOCAL_USERS_DISPOSABLE") != "1" {
		t.Skip("requires ONPAGER_ACC_PROVISION_PACHCA_BOT_ID and ONPAGER_ACC_LOCAL_USERS_DISPOSABLE=1 on a disposable local stand")
	}
	c, prefix := routingAccSetup(t)
	bot, err := c.GetBot(t.Context(), botID)
	if err != nil {
		t.Fatal(err)
	}
	if bot.Messenger != "pachca" || !bot.Enabled || bot.Visibility != "global" {
		t.Fatal("fixture must be an enabled global Pachca bot accessible to a new local user")
	}
	t.Setenv("TF_VAR_initial_password", rand.Text())
	recipient := strconv.FormatUint(1+mrand.Uint64N(1<<50), 10)
	const user = "onpager_user.employee"
	const team = "onpager_team.responders"
	const target = "onpager_delivery_target.warning"
	const contact = "onpager_user_bot_contact.employee"
	const binding = "onpager_team_alert_channel.warning"
	parents := routingAccHeader + fmt.Sprintf(`
variable "initial_password" {
 type = string
 sensitive = true
 ephemeral = true
}
resource "onpager_team" "responders" { name = %q }
resource "onpager_user" "employee" {
 login = %q
 display_name = %q
 initial_password = var.initial_password
}
`, prefix+"-team", prefix+"-employee", prefix+"-employee")
	config := func(display string, verified bool) string {
		return parents + fmt.Sprintf(`
resource "onpager_delivery_target" "warning" {
 channel_id = %q
 snapshot_json = jsonencode({
  BotID = %q
  Kind = "conversation"
  Scope = {Kind = %q, ID = %q}
  ChannelName = %q
 })
}
resource "onpager_team_alert_channel" "warning" {
 team_id = onpager_team.responders.id
 delivery_channel_id = onpager_delivery_target.warning.channel_id
 target_id = onpager_delivery_target.warning.id
}
resource "onpager_user_bot_contact" "employee" {
 user_id = onpager_user.employee.id
 bot_id = %q
 display_name = %q
 recipient_id = %q
 verified = %t
}
`, bot.ID, bot.ID, bot.AddressingScope.Kind, bot.AddressingScope.ID, prefix+"-warning", bot.ID, display, recipient, verified)
	}
	var userID, teamID, targetID, contactID, bindingID string
	check := func(name string, verified bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			records := s.RootModule().Resources
			if userID == "" {
				userID, teamID = records[user].Primary.ID, records[team].Primary.ID
				targetID, contactID, bindingID = records[target].Primary.ID, records[contact].Primary.ID, records[binding].Primary.ID
			} else if userID != records[user].Primary.ID || teamID != records[team].Primary.ID ||
				targetID != records[target].Primary.ID || contactID != records[contact].Primary.ID || bindingID != records[binding].Primary.ID {
				return fmt.Errorf("an in-place update replaced a test resource")
			}
			v, err := c.GetDeliveryTarget(t.Context(), targetID)
			if err != nil {
				return err
			}
			if v.ChannelID != bot.ID || !v.Enabled {
				return fmt.Errorf("target does not reference its enabled fixture channel")
			}
			b, err := c.GetChannelBinding(t.Context(), "team_alert", teamID, bindingID)
			if err != nil {
				return err
			}
			if b.ID == bot.ID || b.TargetID != targetID || b.ChannelID != bot.ID {
				return fmt.Errorf("team alert catalog does not reference the declared target")
			}
			m, err := c.GetUserBotContact(t.Context(), userID, contactID)
			if err != nil {
				return err
			}
			if m.BotID != bot.ID || m.RecipientID != recipient || m.DisplayName != name || m.Verified != verified {
				return fmt.Errorf("administrative contact does not match its configured recipient and verification")
			}
			return nil
		}
	}
	firstName := prefix + "-contact"
	updatedName := firstName + "-updated"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			v, err := c.GetManagedUser(t.Context(), userID)
			if err != nil {
				return err
			}
			group, err := c.GetTeam(t.Context(), teamID)
			if err != nil {
				return err
			}
			if v.Active || group.Active {
				return fmt.Errorf("destroy left the test user or team active")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(firstName, true), Check: check(firstName, true)},
			{ResourceName: target, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"snapshot_json"}},
			{ResourceName: contact, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(*terraform.State) (string, error) { return userID + "/" + contactID, nil }},
			{Config: config(firstName, true), PlanOnly: true},
			{PreConfig: func() {
				if _, err := c.UpdateDeliveryTarget(t.Context(), targetID, false); err != nil {
					t.Fatal(err)
				}
				if _, err := c.UpdateUserBotContact(t.Context(), userID, contactID, client.UserBotContactInput{Verified: new(false)}); err != nil {
					t.Fatal(err)
				}
			}, Config: config(updatedName, true), Check: check(updatedName, true)},
			{Config: config(updatedName, false), Check: check(updatedName, false)},
			{Config: config(updatedName, false), PlanOnly: true},
			{Config: parents, Check: func(*terraform.State) error {
				// Check the child retirements while the user and team are still active.
				retired, err := c.GetDeliveryTarget(t.Context(), targetID)
				if err != nil {
					return err
				}
				if retired.Enabled {
					return fmt.Errorf("destroy left the target enabled")
				}
				if _, err := c.GetUserBotContact(t.Context(), userID, contactID); !client.IsNotFound(err) {
					return fmt.Errorf("expected deleted contact to return 404, got %v", err)
				}
				if _, err := c.GetChannelBinding(t.Context(), "team_alert", teamID, bindingID); !client.IsNotFound(err) {
					return fmt.Errorf("expected removed team binding to return 404, got %v", err)
				}
				return nil
			}},
		},
	})
}
