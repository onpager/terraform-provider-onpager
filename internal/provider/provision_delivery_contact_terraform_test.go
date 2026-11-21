package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The fixture only accepts the public management routes used by this test.
// An attempt to enroll, verify, or deliver through another API fails the test.
func provisionDeliveryFixture(t *testing.T, serve http.HandlerFunc) string {
	t.Helper()
	binary, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-only" || r.URL.RawQuery != "" || r.Header.Get("If-Match") != "" {
			t.Error("unexpected credentials, query, or conditional write on management request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		serve(w, r)
	}))
	t.Cleanup(server.Close)
	return strings.Replace(routingAccHeader, `provider "onpager" {}`, fmt.Sprintf("provider \"onpager\" {\n endpoint = %q\n token = \"fixture-only\"\n}", server.URL), 1)
}

func TestProvisionDeliveryContactTerraformLifecycle(t *testing.T) {
	const address = "onpager_user_bot_contact.employee"
	const user = "11111111-1111-4111-8111-111111111111"
	const bot = "22222222-2222-4222-8222-222222222222"
	const otherBot = "33333333-3333-4333-8333-333333333333"
	const templateMethod = "44444444-4444-4444-8444-444444444444"
	const newMethod = "55555555-5555-4555-8555-555555555555"
	const collection = "/v1/users/" + user + "/bot-contacts"

	type card struct {
		ID        string `json:"id"`
		Bot       string `json:"bot_id"`
		Name      string `json:"display_name"`
		Recipient string `json:"recipient_id,omitempty"`
		Verified  bool   `json:"verified"`
	}
	type write struct {
		method string
		body   map[string]any
		result card
	}
	// The first POST adopts the method already referenced by both template policies.
	// Later, a disappeared method must be recreated with its new UUID (201).
	writes := []write{
		{"POST", map[string]any{"bot_id": bot, "display_name": "On call", "recipient_id": "100", "verified": true}, card{templateMethod, bot, "On call", "100", true}},
		{"PATCH", map[string]any{"display_name": "Renamed"}, card{templateMethod, bot, "Renamed", "100", true}},
		{"PATCH", map[string]any{"recipient_id": "200"}, card{templateMethod, bot, "Renamed", "200", false}},
		{"PATCH", map[string]any{"bot_id": bot, "verified": true}, card{templateMethod, bot, "Renamed", "200", true}},
		{"PATCH", map[string]any{"bot_id": otherBot, "recipient_id": "200", "verified": true}, card{templateMethod, otherBot, "Renamed", "200", true}},
		{"PATCH", map[string]any{"bot_id": bot}, card{templateMethod, bot, "Renamed", "", false}},
		{"PATCH", map[string]any{"display_name": "Renamed"}, card{templateMethod, bot, "Renamed", "", false}},
		{"POST", map[string]any{"bot_id": bot, "display_name": "Renamed"}, card{newMethod, bot, "Renamed", "", false}},
	}
	var mu sync.Mutex
	current := card{ID: templateMethod, Bot: bot, Name: "Template draft"}
	applied, deleted := 0, false
	header := provisionDeliveryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == collection+"/"+current.ID && current.ID != "":
			_ = json.NewEncoder(w).Encode(current)
			return
		case r.Method == "GET" && r.URL.Path == collection+"/"+templateMethod && current.ID == "":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			return
		case r.Method == "DELETE" && r.URL.Path == collection+"/"+current.ID && current.ID != "":
			current, deleted = card{}, true
			w.WriteHeader(204)
			return
		case r.Method == "POST" && r.URL.Path == collection,
			r.Method == "PATCH" && r.URL.Path == collection+"/"+current.ID && current.ID != "":
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || applied >= len(writes) {
				t.Errorf("unexpected contact mutation %d", applied+1)
				w.WriteHeader(400)
				return
			}
			next := writes[applied]
			if next.method != r.Method || !reflect.DeepEqual(body, next.body) {
				t.Errorf("contact mutation %d: got %s %v, want %s %v", applied+1, r.Method, body, next.method, next.body)
				w.WriteHeader(422)
				return
			}
			if current.ID == "" {
				w.WriteHeader(201)
			}
			applied++
			current = next.result
			_ = json.NewEncoder(w).Encode(current)
		default:
			t.Errorf("unexpected operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	})
	config := func(name, selectedBot, input, optional string) string {
		return header + fmt.Sprintf(`
resource "terraform_data" "binding" {
 input = { recipient = %q, verified = true }
}
resource "onpager_user_bot_contact" "employee" {
 user_id = %q
 bot_id = %q
 display_name = %q
 %s
}
`, input, user, selectedBot, name, optional)
	}
	const recipient = "recipient_id = terraform_data.binding.output.recipient"
	const verified = "verified = terraform_data.binding.output.verified"
	bound := recipient + "\n" + verified
	check := func(want card) resource.TestCheckFunc {
		checkRecipient := resource.TestCheckResourceAttr(address, "recipient_id", want.Recipient)
		if want.Recipient == "" {
			checkRecipient = resource.TestCheckNoResourceAttr(address, "recipient_id")
		}
		return resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr(address, "id", want.ID),
			resource.TestCheckResourceAttr(address, "display_name", want.Name),
			resource.TestCheckResourceAttr(address, "bot_id", want.Bot),
			resource.TestCheckResourceAttr(address, "verified", fmt.Sprint(want.Verified)),
			checkRecipient,
		)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if applied != len(writes) || !deleted || current.ID != "" {
				return fmt.Errorf("contact lifecycle ended after %d writes, deleted=%t", applied, deleted)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("On call", bot, "100", bound), Check: check(writes[0].result)},
			{ResourceName: address, ImportState: true, ImportStateId: user + "/" + templateMethod, ImportStateVerify: true},
			{Config: config("On call", bot, "100", bound), PlanOnly: true},
			{Config: config("Renamed", bot, "100", `recipient_id = "ignored"
 verified = false
 lifecycle { ignore_changes = [recipient_id, verified] }`), Check: check(writes[1].result)},
			{Config: config("Renamed", bot, "200", recipient), Check: check(writes[2].result)},
			{Config: config("Renamed", bot, "200", verified), Check: check(writes[3].result)},
			{Config: config("Renamed", otherBot, "200", bound), Check: check(writes[4].result)},
			{Config: config("Renamed", bot, "200", ""), Check: check(writes[5].result)},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); current.Name = "External edit" }, Config: config("Renamed", bot, "200", ""), Check: check(writes[6].result)},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); current = card{} }, Config: config("Renamed", bot, "200", ""), Check: check(writes[7].result)},
			{Config: config("Renamed", bot, "200", ""), PlanOnly: true},
		},
	})
}
