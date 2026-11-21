package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const notificationDefaultsAddress = "onpager_user_notification_defaults.test"

func TestUserNotificationDefaultsTerraformLifecycle(t *testing.T) {
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")

	// Specify the wire contract independently of the provider and its Go DTOs.
	snapshot := func(contacts, standard, important string, enabled bool) map[string]any {
		t.Helper()
		var result map[string]any
		err := json.Unmarshal([]byte(fmt.Sprintf(`{"enabled":%t,"contacts":%s,"policies":{"default":%s,"important":%s}}`, enabled, contacts, standard, important)), &result)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	const chatChannel = "33333333-3333-4333-8333-333333333333"
	const unconfigured = `{"configured":false,"steps":[]}`
	const emptyPolicy = `{"configured":true,"steps":[]}`
	contact := fmt.Sprintf(`[{"id":%q,"channel_id":%q,"display_name":"Test phone","require_verification":true},
		{"id":%q,"channel_id":%q,"display_name":"Test chat","require_verification":false,"lookup_attribute":"email"}]`, fTeam, fService, fOtherTeam, chatChannel)
	notify := fmt.Sprintf(`{"configured":true,"steps":[{"kind":"notify","contact_id":%q},{"kind":"wait","wait_seconds":17},{"kind":"notify","contact_id":%q}]}`, fOtherTeam, fTeam)
	empty := snapshot("[]", unconfigured, unconfigured, false)
	configuredEmpty := snapshot("[]", emptyPolicy, unconfigured, false)
	contactOnly := snapshot(contact, unconfigured, unconfigured, false)
	populated := snapshot(contact, notify, emptyPolicy, true)

	var mu sync.Mutex
	saved := configuredEmpty
	revision, writes := 3, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.RequestURI() != "/v1/settings/user-notification-defaults" || r.Header.Get("If-Match") != "" {
			t.Errorf("unexpected template request: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !reflect.DeepEqual(body, populated) && !reflect.DeepEqual(body, configuredEmpty) && !reflect.DeepEqual(body, empty) {
				t.Errorf("unexpected replacement body: %v", body)
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			saved = body
			writes++
			revision++
		case http.MethodGet:
		default:
			t.Errorf("unexpected template method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		receipt := map[string]any{"revision": revision}
		for k, v := range saved {
			receipt[k] = v
		}
		_ = json.NewEncoder(w).Encode(receipt)
	}))
	defer server.Close()

	header := strings.Replace(routingAccHeader, `provider "onpager" {}`, fmt.Sprintf("provider \"onpager\" {\n endpoint = %q\n token = \"fixture-token\"\n}", server.URL), 1)
	config := header + fmt.Sprintf(`
resource "terraform_data" "slot" { input = %q }
resource "onpager_user_notification_defaults" "test" {
 enabled = true
 contacts = [
  {id=terraform_data.slot.output, channel_id=%q, display_name="Test phone"},
  {id=%q, channel_id=%q, display_name="Test chat", require_verification=false, lookup_attribute="email"}
 ]
 policies = {
  default = {configured=true, steps=[
   {kind="notify", contact_id=%q},
   {kind="wait", wait_seconds=17},
   {kind="notify", contact_id=terraform_data.slot.output}
  ]}
  important = {configured=true, steps=[]}
 }
}
`, fTeam, fService, fOtherTeam, chatChannel, fOtherTeam)
	disabledConfig := header + `
resource "onpager_user_notification_defaults" "test" {
 enabled = false
 contacts = []
 policies = {
  default = {configured=true, steps=[]}
  important = {configured=false, steps=[]}
 }
}`
	external := func(v map[string]any) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			saved = v
			revision++
		}
	}
	check := func(want map[string]any, wantWrites int) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(saved, want) || writes != wantWrites {
				return fmt.Errorf("saved template %v after %d writes, expected %v after %d", saved, writes, want, wantWrites)
			}
			return resource.TestCheckResourceAttr(notificationDefaultsAddress, "revision", strconv.Itoa(revision))(s)
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(saved, empty) || writes != 4 {
				return fmt.Errorf("destroy left %v after %d writes, want the canonical reset after 4", saved, writes)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: disabledConfig, ExpectError: regexp.MustCompile("(?i)import")},
			{PreConfig: external(contactOnly), Config: disabledConfig, ExpectError: regexp.MustCompile("(?i)import")},
			{PreConfig: external(empty), Config: config, Check: resource.ComposeTestCheckFunc(check(populated, 1),
				resource.TestCheckResourceAttr(notificationDefaultsAddress, "id", "tenant"),
				resource.TestCheckResourceAttr(notificationDefaultsAddress, "contacts.0.require_verification", "true"),
				resource.TestCheckResourceAttr(notificationDefaultsAddress, "contacts.0.lookup_attribute", ""),
				resource.TestCheckResourceAttr(notificationDefaultsAddress, "policies.default.steps.1.wait_seconds", "17"),
			)},
			{ResourceName: notificationDefaultsAddress, ImportState: true, ImportStateId: "tenant", ImportStateVerify: true},
			{PreConfig: external(populated), Config: strings.Replace(config, `display_name="Test phone"`, `display_name="Test phone", lookup_attribute=""`, 1), PlanOnly: true},
			{PreConfig: external(contactOnly), Config: config, Check: check(populated, 2)},
			{Config: disabledConfig, Check: check(configuredEmpty, 3)},
			{ResourceName: notificationDefaultsAddress, ImportState: true, ImportStateId: "tenant", ImportStateVerify: true},
			{Config: disabledConfig, PlanOnly: true},
		},
	})
}
