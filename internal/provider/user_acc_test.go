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

// Local users cannot be physically deleted. This explicit fixture opt-in
// acknowledges that test-created, deactivated identities remain after cleanup.
func TestAccLocalUser(t *testing.T) {
	if os.Getenv("ONPAGER_ACC_LOCAL_USERS_DISPOSABLE") != "1" {
		t.Skip("local-user acceptance requires ONPAGER_ACC_LOCAL_USERS_DISPOSABLE=1; retired test identities are retained")
	}
	c, prefix := routingAccSetup(t)
	var id string
	t.Cleanup(func() {
		if id != "" {
			if err := c.DeactivateUser(context.Background(), id); err != nil {
				t.Errorf("deactivate own test user: %v", err)
			}
		}
	})
	config := func(name, email string, active bool, initial bool) string {
		password := ""
		if initial {
			password = `initial_password = "tfacc-synthetic-initial-password"`
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_user" "test" {
 display_name = %q
 login = %q
 email = %s
 active = %t
 %s
}
`, name, prefix+"-user", email, active, password)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		v, err := c.GetManagedUser(context.Background(), id)
		if err != nil {
			return err
		}
		if v.Active {
			return fmt.Errorf("destroy left its test user active")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(prefix, fmt.Sprintf("%q", prefix+"@example.invalid"), true, true), Check: resource.ComposeTestCheckFunc(func(s *terraform.State) error {
			id = s.RootModule().Resources["onpager_user.test"].Primary.ID
			return nil
		}, resource.TestCheckNoResourceAttr("onpager_user.test", "initial_password"))},
		{ResourceName: "onpager_user.test", ImportState: true, ImportStateVerify: true},
		{Config: config(prefix+"-updated", "null", false, false), Check: resource.TestCheckResourceAttr("onpager_user.test", "active", "false")},
		{ResourceName: "onpager_user.test", ImportState: true, ImportStateVerify: true},
		{Config: config(prefix+"-updated", "null", false, false), PlanOnly: true},
		{Config: config(prefix+"-updated", "null", true, false)},
		{PreConfig: func() {
			if _, err := c.UpdateLocalUser(t.Context(), id, client.LocalUserPatch{DisplayName: new(prefix + "-drift")}); err != nil {
				t.Fatal(err)
			}
		}, Config: config(prefix+"-updated", "null", true, false)},
		{Config: config(prefix+"-updated", "null", true, false), PlanOnly: true},
	}})
}
