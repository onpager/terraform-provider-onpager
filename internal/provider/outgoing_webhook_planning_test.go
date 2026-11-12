package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func TestOutgoingWebhookPlanRequestIDRemainsUnknownUntilApply(t *testing.T) {
	for _, explicitUnknown := range []bool{false, true} {
		t.Run(fmt.Sprint(explicitUnknown), func(t *testing.T) {
			r := onpager.NewOutgoingWebhookResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("planning made an API call") })
			values := outgoingValues("")
			values["request_id"] = types.StringNull()
			if explicitUnknown {
				values["request_id"] = types.StringUnknown()
			}
			config := valueState(t, s, values)
			values["request_id"] = types.StringUnknown()
			proposed := valueState(t, s, values)
			nullState := proposed
			nullState.RemoveResource(t.Context())
			for range 2 {
				resp := frameworkresource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: proposed.Raw}}
				r.(frameworkresource.ResourceWithModifyPlan).ModifyPlan(t.Context(), frameworkresource.ModifyPlanRequest{Config: tfsdk.Config{Schema: s, Raw: config.Raw}, State: nullState, Plan: resp.Plan}, &resp)
				var key types.String
				resp.Plan.GetAttribute(t.Context(), path.Root("request_id"), &key)
				if resp.Diagnostics.HasError() || !key.IsUnknown() {
					t.Fatalf("repeated fresh planning resolved a create key: %v", resp.Diagnostics)
				}
			}
		})
	}
}

// Exercise Terraform's actual plan/apply protocol against an in-process HTTP
// fixture. No OnPager, credentials, downloads or destination requests are used.
func TestOutgoingWebhookTerraformReplacementRequestIDs(t *testing.T) {
	for _, teamScope := range []bool{false, true} {
		t.Run(fmt.Sprint(teamScope), func(t *testing.T) {
			fixture := newOutgoingPlanningFixture(t)
			name := "onpager_outgoing_webhook.test"
			if teamScope {
				name = "onpager_team_outgoing_webhook.test"
			}
			config := func(purpose, display, owner string) string {
				team := ""
				if teamScope {
					team = fmt.Sprintf("team_id = %q", owner)
				}
				return fixture.header + fmt.Sprintf(`
resource %q "test" {
 %s
 purpose = %q
 display_name = %q
 enabled = false
 request = { endpoint = "http://passive.invalid/hook", body_template = "{}" }
}
`, strings.Split(name, ".")[0], team, purpose, display)
			}
			var previousID, previousKey string
			check := func(replaced bool) resource.TestCheckFunc {
				return func(state *terraform.State) error {
					row := state.RootModule().Resources[name]
					id, key := row.Primary.ID, row.Primary.Attributes["request_id"]
					if previousID != "" {
						if replaced && (id == previousID || key == previousKey) {
							return fmt.Errorf("replacement reused the old identity/idempotency key")
						}
						if !replaced && (id != previousID || key != previousKey) {
							return fmt.Errorf("ordinary update changed identity/idempotency key")
						}
					}
					previousID, previousKey = id, key
					return nil
				}
			}
			steps := []resource.TestStep{
				{Config: config("notification", "Initial", fTeam), Check: check(false)},
				{Config: config("notification", "Updated", fTeam), Check: check(false)},
				{Config: config("escalation", "Updated", fTeam), Check: check(true)},
				{Config: config("escalation", "Updated", fTeam), Taint: []string{name}, Check: check(true)},
			}
			if teamScope {
				steps = append(steps, resource.TestStep{Config: config("escalation", "Updated", fOtherTeam), Check: check(true)})
			}
			owner := fTeam
			if teamScope {
				owner = fOtherTeam
			}
			steps = append(steps, resource.TestStep{Config: config("escalation", "Updated", owner), PlanOnly: true})
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), Steps: steps})
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			expected := 3
			if teamScope {
				expected = 4
			}
			if len(fixture.keys) != expected {
				t.Fatalf("creates=%d, want %d", len(fixture.keys), expected)
			}
		})
	}
}

func TestOutgoingWebhookTerraformDependentRequestID(t *testing.T) {
	fixture := newOutgoingPlanningFixture(t)
	config := fixture.header + fmt.Sprintf(`
resource "terraform_data" "request" { input = %q }
resource "onpager_outgoing_webhook" "test" {
 request_id = terraform_data.request.output
 purpose = "notification"
 display_name = "Explicit dependent key"
 enabled = false
 request = { endpoint = "http://passive.invalid/hook", body_template = "{}" }
}
`, fOtherTeam)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), Steps: []resource.TestStep{{Config: config, Check: resource.TestCheckResourceAttr("onpager_outgoing_webhook.test", "request_id", fOtherTeam)}, {Config: config, PlanOnly: true}}})
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.keys) != 1 || !fixture.keys[fOtherTeam] {
		t.Fatal("explicit dependency key was replaced")
	}
}

type outgoingPlanningRecord struct {
	owner    string
	input    client.OutgoingWebhookInput
	revision int
}
type outgoingPlanningFixture struct {
	mu      sync.Mutex
	keys    map[string]bool
	records map[string]*outgoingPlanningRecord
	header  string
}

func newOutgoingPlanningFixture(t *testing.T) *outgoingPlanningFixture {
	t.Helper()
	terraformPath, e := exec.LookPath("terraform")
	if e != nil {
		t.Fatal("installed Terraform CLI is required for outgoing-webhook planning tests")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	fixture := &outgoingPlanningFixture{keys: map[string]bool{}, records: map[string]*outgoingPlanningRecord{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		route := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		owner, id := "", ""
		if len(route) >= 3 && route[1] == "teams" {
			owner = route[2]
			route = append([]string{route[0]}, route[3:]...)
		}
		if len(route) < 2 || route[0] != "v1" || route[1] != "outgoing-webhooks" {
			t.Errorf("unexpected fixture request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if len(route) == 3 {
			id = route[2]
		}
		if r.Method == "POST" && id == "" {
			var body struct {
				client.OutgoingWebhookInput
				RequestID string `json:"request_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid create body")
				w.WriteHeader(422)
				return
			}
			if fixture.keys[body.RequestID] {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"error":{"code":"webhook_conflict"}}`)
				return
			}
			fixture.keys[body.RequestID] = true
			id = fmt.Sprintf("%08x-1111-4111-8111-111111111111", len(fixture.keys))
			fixture.records[id] = &outgoingPlanningRecord{owner: owner, input: body.OutgoingWebhookInput, revision: 1}
		}
		record := fixture.records[id]
		if record == nil || record.owner != owner {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			return
		}
		if r.Method == "PUT" || r.Method == "DELETE" {
			if r.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, record.revision) {
				w.WriteHeader(412)
				return
			}
			if r.Method == "DELETE" {
				record.input.Enabled = false
			} else {
				if json.NewDecoder(r.Body).Decode(&record.input) != nil {
					t.Error("invalid update body")
					w.WriteHeader(422)
					return
				}
			}
			record.revision++
		}
		view := client.OutgoingWebhook{ID: id, Purpose: record.input.Purpose, DisplayName: record.input.DisplayName, Enabled: record.input.Enabled, ETag: fmt.Sprintf(`"%d"`, record.revision), Request: record.input.Request, RecipientFields: record.input.RecipientFields, Secrets: []client.OutgoingWebhookSecret{}, RuntimeStatus: "disabled"}
		if owner != "" {
			view.Scope.TeamID = &owner
		}
		w.Header().Set("ETag", view.ETag)
		json.NewEncoder(w).Encode(view)
	}))
	t.Cleanup(server.Close)
	fixture.header = fmt.Sprintf(`
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-only"
}
`, server.URL)
	return fixture
}
