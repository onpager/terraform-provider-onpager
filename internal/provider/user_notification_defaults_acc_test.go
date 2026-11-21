package provider_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestAccUserNotificationDefaults(t *testing.T) {
	if os.Getenv("ONPAGER_ACC_USER_NOTIFICATION_DEFAULTS_DISPOSABLE") != "1" {
		t.Skip("requires ONPAGER_ACC_USER_NOTIFICATION_DEFAULTS_DISPOSABLE=1 and an exclusively disposable empty tenant template")
	}
	c, _ := routingAccSetup(t)
	initial, err := c.GetUserNotificationDefaults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !initial.IsEmpty() {
		t.Fatal("refusing to change a populated or enabled tenant template")
	}

	// Keep the template disabled at every step. A randomized wait identifies
	// this test's snapshots without referring to any real contacts or channels.
	firstWait := 1 + rand.Int64N(1_000_000_000)
	secondWait := firstWait + 1
	desired := func(seconds int64) client.UserNotificationDefaultsInput {
		in := client.EmptyUserNotificationDefaults()
		in.Policies.Default.Configured = true
		in.Policies.Important.Configured = true
		in.Policies.Important.Steps = []client.NotificationDefaultStep{{Kind: "wait", WaitSeconds: &seconds}}
		return in
	}
	config := func(seconds int64) string {
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_user_notification_defaults" "test" {
 enabled = false
 contacts = []
 policies = {
  default = {configured=true, steps=[]}
  important = {configured=true, steps=[{kind="wait", wait_seconds=%d}]}
 }
}`, seconds)
	}
	check := func(seconds int64) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			current, err := c.GetUserNotificationDefaults(t.Context())
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current.UserNotificationDefaultsInput, desired(seconds)) || current.Revision <= initial.Revision {
				return fmt.Errorf("saved template does not match the test snapshot")
			}
			return resource.TestCheckResourceAttr(notificationDefaultsAddress, "revision", strconv.FormatInt(current.Revision, 10))(s)
		}
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		current, err := c.GetUserNotificationDefaults(ctx)
		if err != nil {
			t.Errorf("read template during cleanup: %v", err)
			return
		}
		if current.IsEmpty() {
			return
		}
		if !reflect.DeepEqual(current.UserNotificationDefaultsInput, desired(firstWait)) &&
			!reflect.DeepEqual(current.UserNotificationDefaultsInput, desired(secondWait)) {
			t.Error("cleanup found a foreign notification template")
			return
		}
		if _, err := c.ReplaceUserNotificationDefaults(ctx, client.EmptyUserNotificationDefaults()); err != nil {
			t.Errorf("clear owned notification template: %v", err)
		}
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			current, err := c.GetUserNotificationDefaults(t.Context())
			if err != nil {
				return err
			}
			if !current.IsEmpty() || current.Revision <= initial.Revision {
				return fmt.Errorf("destroy did not publish the canonical empty template")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(firstWait), Check: check(firstWait)},
			{ResourceName: notificationDefaultsAddress, ImportState: true, ImportStateId: "tenant", ImportStateVerify: true},
			{Config: config(firstWait), PlanOnly: true},
			{Config: config(secondWait), Check: check(secondWait)},
			{Config: config(secondWait), PlanOnly: true},
		},
	})
}
