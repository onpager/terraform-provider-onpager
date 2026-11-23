package provider_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// An optional slot is destructive: this test claims and finally removes its
// original draft. Both identifiers must name a dedicated employee's fixture.
type managedContactFixture struct {
	user, source, draft, slot string
}

func managedContactAccFixture(t *testing.T, family, sourceEnv string) managedContactFixture {
	t.Helper()
	f := managedContactFixture{
		user:   os.Getenv("ONPAGER_ACC_CONTACT_USER_ID"),
		source: os.Getenv(sourceEnv),
		draft:  os.Getenv("ONPAGER_ACC_" + family + "_DRAFT_ID"),
		slot:   os.Getenv("ONPAGER_ACC_" + family + "_TEMPLATE_ID"),
	}
	if os.Getenv("ONPAGER_ACC_MANAGED_CONTACTS_DISPOSABLE") != "1" || f.user == "" || f.source == "" {
		t.Skipf("requires ONPAGER_ACC_MANAGED_CONTACTS_DISPOSABLE=1, ONPAGER_ACC_CONTACT_USER_ID and %s", sourceEnv)
	}
	if (f.draft == "") != (f.slot == "") {
		t.Fatalf("ONPAGER_ACC_%s_DRAFT_ID and ONPAGER_ACC_%s_TEMPLATE_ID must be supplied together", family, family)
	}
	return f
}

func (f managedContactFixture) templateArgument() string {
	if f.slot == "" {
		return ""
	}
	return fmt.Sprintf("template_contact_id = %q", f.slot)
}

func managedContactRequestIDs() (string, string) {
	tail := rand.Uint64N(1 << 48)
	return fmt.Sprintf("add7c076-4e8d-46a9-a001-%012x", tail), fmt.Sprintf("add7c076-4e8d-46a9-a002-%012x", tail)
}

func managedContactImport(address, user string, method *string, localFields ...string) resource.TestStep {
	return resource.TestStep{
		ResourceName: address, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: localFields,
		ImportStateIdFunc: func(*terraform.State) (string, error) { return user + "/" + *method, nil },
		ImportStateCheck: func(states []*terraform.InstanceState) error {
			if len(states) != 1 || states[0].ID != *method || states[0].Attributes["user_id"] != user {
				return fmt.Errorf("import did not read the exact employee and method")
			}
			for _, field := range localFields {
				if _, present := states[0].Attributes[field]; present {
					return fmt.Errorf("import unexpectedly populated %s", field)
				}
			}
			return nil
		},
	}
}

// Use a fresh address to exercise a persisted import. The removed block
// relinquishes the original address while keeping the method available for
// adoption. The next plan runs against the imported, read-only projection.
func (f managedContactFixture) adoptionSteps(t *testing.T, family, bindingKey, name string, method *string) (resource.TestStep, resource.TestStep) {
	t.Helper()
	config := routingAccHeader + fmt.Sprintf(`
resource "onpager_user_%[1]s_contact" "adopted" {
  user_id = %[2]q
  %[3]s = %[4]q
  display_name = %[5]q
}
`, family, f.user, bindingKey, f.source, name)
	adopt := config + fmt.Sprintf(`
variable "import_method" { type = string }
removed {
  from = onpager_user_%[1]s_contact.employee
  lifecycle { destroy = false }
}
import {
  to = onpager_user_%[1]s_contact.adopted
  id = var.import_method
}
`, family)
	address := "onpager_user_" + family + "_contact.adopted"
	return resource.TestStep{
		PreConfig: func() { t.Setenv("TF_VAR_import_method", f.user+"/"+*method) },
		Config:    adopt,
		Check: func(s *terraform.State) error {
			return resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(address, "id", *method),
				resource.TestCheckNoResourceAttr(address, "request_id"), resource.TestCheckNoResourceAttr(address, "template_contact_id"))(s)
		},
	}, resource.TestStep{Config: config, PlanOnly: true}
}

func TestAccManagedPhoneContact(t *testing.T) {
	f := managedContactAccFixture(t, "PHONE", "ONPAGER_ACC_PHONE_CHANNEL_ID")
	c, prefix := routingAccSetup(t)
	if f.draft != "" {
		draft, err := c.GetUserPhoneContact(t.Context(), f.user, f.draft)
		if err != nil {
			t.Fatal(err)
		}
		if !draft.Draft || draft.UserID != f.user || draft.ChannelID != f.source {
			t.Fatal("the supplied method must be an untouched phone draft for the fixture user and channel")
		}
	}
	const address = "onpager_user_phone_contact.employee"
	key, replacementKey := managedContactRequestIDs()
	// Use the North American fictional-number block. External drift changes the
	// area code while preserving the last four digits returned by Control.
	area, suffix := 200+rand.IntN(699), rand.IntN(100)
	phone := fmt.Sprintf("+1%03d55501%02d", area, suffix)
	driftPhone := fmt.Sprintf("+1%03d55501%02d", area+1, suffix)
	config := func(key, name string, verified bool) string {
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_user_phone_contact" "employee" {
  user_id = %q
  channel_id = %q
  request_id = %q
  display_name = %q
  phone = %q
  verified = %t
  %s
}
`, f.user, f.source, key, name, phone, verified, f.templateArgument())
	}
	var method, original, originalTarget, foreignTarget string
	check := func(name string, verified, recreated bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			method = s.RootModule().Resources[address].Primary.ID
			if original == "" {
				original = method
				if f.draft != "" && method != f.draft {
					return fmt.Errorf("create did not claim the supplied phone draft")
				}
			}
			if (method != original) != recreated {
				return fmt.Errorf("unexpected phone method identity")
			}
			got, err := c.GetUserPhoneContact(t.Context(), f.user, method)
			if err != nil {
				return err
			}
			if got.ChannelID != f.source || got.DisplayName != name || got.MaskedPhone != "+…"+phone[len(phone)-4:] || got.Verified != verified || !got.Enabled || got.Draft {
				return fmt.Errorf("Control returned an unexpected phone binding or verification state")
			}
			if foreignTarget != "" && got.TargetID == foreignTarget {
				return fmt.Errorf("the contact still references the external same-suffix phone")
			}
			originalTarget = got.TargetID
			return resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "target_id", got.TargetID),
				resource.TestCheckResourceAttr(address, "phone", phone),
				resource.TestCheckResourceAttr(address, "verified", fmt.Sprint(verified)),
			)(s)
		}
	}
	absent := func(*terraform.State) error {
		_, err := c.GetUserPhoneContact(t.Context(), f.user, method)
		if !client.IsNotFound(err) {
			return fmt.Errorf("phone GET after destroy: expected confirmed absence, got %v", err)
		}
		return nil
	}
	initial := config(key, prefix, true)
	updated := config(key, prefix+"-updated", false)
	recreated := config(replacementKey, prefix, true)
	importStep := managedContactImport(address, f.user, &method, "phone", "request_id", "template_contact_id")
	adopt, importedPlan := f.adoptionSteps(t, "phone", "channel_id", prefix, &method)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy:             absent,
		Steps: []resource.TestStep{
			{Config: initial, Check: check(prefix, true, false)},
			importStep,
			{Config: initial, PlanOnly: true},
			{PreConfig: func() {
				got, err := c.UpdateUserPhoneContact(t.Context(), f.user, method, client.UserPhoneContactInput{Phone: &driftPhone})
				if err != nil {
					t.Fatal(err)
				}
				foreignTarget = got.TargetID
				if got.Verified || got.TargetID == originalTarget || !strings.HasSuffix(got.MaskedPhone, phone[len(phone)-4:]) {
					t.Fatal("out-of-band phone replacement did not produce a distinct unverified binding with the same suffix")
				}
			}, Config: initial, Check: check(prefix, true, false)},
			{Config: updated, Check: check(prefix+"-updated", false, false)},
			{Config: updated, PlanOnly: true},
			{Config: routingAccHeader, Check: absent},
			{Config: initial, ExpectError: regexp.MustCompile("HTTP 409")},
			{Config: recreated, Check: check(prefix, true, true)},
			{Config: recreated, PlanOnly: true},
			adopt, importedPlan,
		},
	})
}

// Introduce the competing write after Terraform has saved its plan, so the
// provider must use its own prior ETag and surface the server's 412.
type managedContactConcurrentEdit func(context.Context) error

func (edit managedContactConcurrentEdit) CheckPlan(ctx context.Context, _ plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	resp.Error = edit(ctx)
}

func TestAccManagedWebhookContact(t *testing.T) {
	f := managedContactAccFixture(t, "WEBHOOK", "ONPAGER_ACC_WEBHOOK_ID")
	c, prefix := routingAccSetup(t)
	get := func(id string) (client.UserWebhookContact, error) {
		return c.GetUserWebhookContact(t.Context(), f.user, id, f.source)
	}
	if f.draft != "" {
		draft, err := get(f.draft)
		if err != nil {
			t.Fatal(err)
		}
		if !draft.Draft || draft.UserID != f.user || draft.WebhookID != f.source {
			t.Fatal("the supplied method must be an untouched webhook draft for the fixture user and definition")
		}
	}
	const address = "onpager_user_webhook_contact.employee"
	key, replacementKey := managedContactRequestIDs()
	config := func(key, name, login string, enabled bool) string {
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_user_webhook_contact" "employee" {
  user_id = %q
  webhook_id = %q
  request_id = %q
  display_name = %q
  values = { login = %q }
  enabled = %t
  %s
}
`, f.user, f.source, key, name, login, enabled, f.templateArgument())
	}
	var method, original, expectedTag string
	check := func(name, login string, enabled, recreated bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			method = s.RootModule().Resources[address].Primary.ID
			if original == "" {
				original = method
				if f.draft != "" && method != f.draft {
					return fmt.Errorf("create did not claim the supplied webhook draft")
				}
			}
			if (method != original) != recreated {
				return fmt.Errorf("unexpected webhook method identity")
			}
			got, err := get(method)
			if err != nil {
				return err
			}
			if got.DisplayName != name || got.Values["login"] != login || got.Enabled != enabled || !got.Verified || got.Draft {
				return fmt.Errorf("Control returned unexpected webhook contact metadata or recipient values")
			}
			expectedTag = got.ETag
			return resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "etag", got.ETag),
				resource.TestCheckResourceAttr(address, "values.login", login),
				resource.TestCheckResourceAttr(address, "enabled", fmt.Sprint(enabled)),
			)(s)
		}
	}
	concurrent := func(name string) resource.ConfigPlanChecks {
		return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{managedContactConcurrentEdit(func(ctx context.Context) error {
			_, err := c.UpdateUserWebhookContact(ctx, f.user, method, f.source, expectedTag, client.UserWebhookContactPatch{DisplayName: &name})
			return err
		})}}
	}
	checkCompetingWrite := func(name string) func() {
		return func() {
			got, err := get(method)
			if err != nil {
				t.Fatal(err)
			}
			if got.DisplayName != name || got.ETag == expectedTag || got.ID != original {
				t.Fatal("the stale Terraform operation overwrote or removed the competing contact")
			}
		}
	}
	absent := func(*terraform.State) error {
		_, err := get(method)
		if !client.IsNotFound(err) {
			return fmt.Errorf("webhook GET after destroy: expected confirmed absence, got %v", err)
		}
		return nil
	}
	recipient := prefix + "-extension"
	updatedRecipient := recipient + "-changed"
	initial := config(key, prefix, recipient, true)
	updated := config(key, prefix+"-updated", updatedRecipient, false)
	recreated := config(replacementKey, prefix, recipient, true)
	importStep := managedContactImport(address, f.user, &method, "request_id", "template_contact_id")
	adopt, importedPlan := f.adoptionSteps(t, "webhook", "webhook_id", prefix, &method)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy:             absent,
		Steps: []resource.TestStep{
			{Config: initial, Check: check(prefix, recipient, true, false)},
			importStep,
			{Config: initial, PlanOnly: true},
			{Config: updated, ConfigPlanChecks: concurrent("Competing update"), ExpectError: regexp.MustCompile("HTTP 412")},
			{PreConfig: checkCompetingWrite("Competing update"), Config: updated, Check: check(prefix+"-updated", updatedRecipient, false, false)},
			{Config: updated, PlanOnly: true},
			{Config: routingAccHeader, ConfigPlanChecks: concurrent("Competing deletion edit"), ExpectError: regexp.MustCompile("HTTP 412")},
			{PreConfig: checkCompetingWrite("Competing deletion edit"), Config: routingAccHeader, Check: absent},
			{Config: initial, ExpectError: regexp.MustCompile("HTTP 409")},
			{Config: recreated, Check: check(prefix, recipient, true, true)},
			{Config: recreated, PlanOnly: true},
			adopt, importedPlan,
		},
	})
}
