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

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Exercise the real Terraform protocol with a local management API fixture.
// The fixture has no dispatcher and never constructs or contacts Zvonok.
func TestVoiceProviderTerraformDefaultsImportsDriftAndSecretChanges(t *testing.T) {
	binary, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	records := map[string]client.VoiceProvider{}
	creates, replacements, writes := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.Header.Get("If-Match") != "" {
			t.Error("invented voice CAS")
		}
		id := strings.TrimPrefix(q.URL.Path, "/v1/voice-providers/")
		if q.Method == "POST" && q.URL.Path == "/v1/voice-providers" {
			var in struct {
				client.VoiceProviderInput
				Kind string `json:"kind"`
			}
			if err := json.NewDecoder(q.Body).Decode(&in); err != nil {
				t.Error(err)
				w.WriteHeader(422)
				return
			}
			if in.Secrets["public_key"].Action != "replace" || in.Secrets["public_key"].Value == "" {
				t.Error("create omitted required synthetic credential")
				w.WriteHeader(422)
				return
			}
			creates++
			replacements++
			writes++
			id = fmt.Sprintf("%08x-1111-4111-8111-111111111111", creates)
			records[id] = client.VoiceProvider{ID: id, Kind: in.Kind, DisplayName: in.DisplayName, Enabled: in.Enabled, Visibility: in.Visibility, Configuration: in.Configuration, TeamIDs: in.TeamIDs, SecretKeys: []string{"public_key"}, Runtime: client.VoiceProviderRuntime{State: "disabled"}}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(records[id])
			return
		}
		if q.URL.Path != "/v1/voice-providers/"+id {
			t.Error("unexpected route", q.URL.Path)
			w.WriteHeader(404)
			return
		}
		v, ok := records[id]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			return
		}
		switch q.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(v)
		case "PUT":
			writes++
			var in client.VoiceProviderInput
			if err := json.NewDecoder(q.Body).Decode(&in); err != nil {
				t.Error(err)
				w.WriteHeader(422)
				return
			}
			for _, s := range in.Secrets {
				if s.Action == "replace" {
					replacements++
				}
			}
			v.DisplayName = in.DisplayName
			v.Enabled = in.Enabled
			v.Visibility = in.Visibility
			v.Configuration = in.Configuration
			v.TeamIDs = in.TeamIDs
			records[id] = v
			w.WriteHeader(204)
		case "DELETE":
			writes++
			delete(records, id)
			w.WriteHeader(204)
		default:
			t.Error("unsupported voice operation", q.Method)
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	header := fmt.Sprintf(`
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-only"
}
resource "terraform_data" "campaign" { input = "calls" }
`, server.URL)
	config := func(name, enabled, secret string) string {
		baseSetting := ""
		switch name {
		case "Initial":
			baseSetting = `base_url = "http://voice-mock:8080/v1/phones"`
		case "Renamed":
			baseSetting = `base_url = "http://VOICE-MOCK:8080/v2/phones/"`
		}
		return header + fmt.Sprintf(`
resource "onpager_voice_provider" "test" {
 kind = "zvonok"
 display_name = %q
 %s
 configuration = {
  %s
  call_campaign_id = terraform_data.campaign.output
  verification_campaign_id = "verify"
 }
 %s
}
output "voice_provider_runtime_state" {
 value = onpager_voice_provider.test.runtime_status
}
`, name, enabled, baseSetting, secret)
	}
	secret := `secrets = { public_key = { action = "replace", value = "fixture-key" } }`
	rotated := `secrets = { public_key = { action = "replace", value = "rotated-fixture-key" } }`
	var id string
	capture := func(s *terraform.State) error {
		id = s.RootModule().Resources["onpager_voice_provider.test"].Primary.ID
		return nil
	}
	checkReplacements := func(want int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if replacements != want {
				return fmt.Errorf("wanted %d credential writes, got %d", want, replacements)
			}
			return nil
		}
	}
	checkEndpoint := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if got := records[id].Configuration.BaseURL; got != want {
				return fmt.Errorf("stored voice API URL %q, want %q", got, want)
			}
			return nil
		}
	}
	var beforeRuntimeWrites int
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if len(records) != 0 {
			return fmt.Errorf("destroy retained managed voice configuration")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config("Initial", "enabled = false", secret), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_voice_provider.test", "configuration.base_url", "http://voice-mock:8080/v1/phones"), resource.TestCheckResourceAttr("onpager_voice_provider.test", "configuration.max_text_runes", "500"), resource.TestCheckResourceAttr("onpager_voice_provider.test", "enabled", "false"), checkReplacements(1))},
		{ResourceName: "onpager_voice_provider.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secrets"}},
		{Config: config("Initial", "enabled = false", secret), PlanOnly: true},
		{Config: config("Renamed", "enabled = false", secret), Check: resource.ComposeTestCheckFunc(checkEndpoint("http://voice-mock:8080/v2/phones"), checkReplacements(1))},
		{Config: config("Renamed", "enabled = false", rotated), Check: checkReplacements(2)},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			v := records[id]
			v.Configuration.BaseURL = "http://other-mock/phones"
			records[id] = v
		}, Config: config("Renamed", "enabled = false", rotated), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_voice_provider.test", "configuration.base_url", "http://VOICE-MOCK:8080/v2/phones/"), checkEndpoint("http://voice-mock:8080/v2/phones"), checkReplacements(2))},
		{Config: config("Renamed", "enabled = false", rotated), PlanOnly: true},
		{PreConfig: func() { mu.Lock(); defer mu.Unlock(); delete(records, id) }, Config: config("Renamed", "enabled = false", rotated), Check: resource.ComposeTestCheckFunc(capture, checkReplacements(3))},
		{Config: config("Renamed", "enabled = null", rotated), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_voice_provider.test", "enabled", "true"), checkReplacements(3))},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			v := records[id]
			v.Runtime = client.VoiceProviderRuntime{State: "starting"}
			records[id] = v
		}, Config: config("Repair while starting", "enabled = true", secret), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_voice_provider.test", "runtime_status", "starting"), resource.TestCheckResourceAttr("onpager_voice_provider.test", "configuration.base_url", ""), checkEndpoint(""), checkReplacements(4))},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			beforeRuntimeWrites = writes
			v := records[id]
			v.Runtime = client.VoiceProviderRuntime{State: "running", ObservedAt: "2026-11-16T09:00:00Z"}
			records[id] = v
		}, Config: config("Repair while starting", "enabled = true", secret), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction("onpager_voice_provider.test", plancheck.ResourceActionNoop),
			plancheck.ExpectKnownOutputValue("voice_provider_runtime_state", knownvalue.StringExact("running")),
		}}, Check: func(s *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if writes != beforeRuntimeWrites {
				return fmt.Errorf("runtime-only change caused %d voice-provider mutations", writes-beforeRuntimeWrites)
			}
			return resource.TestCheckOutput("voice_provider_runtime_state", "running")(s)
		}},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			v := records[id]
			v.Runtime = client.VoiceProviderRuntime{State: "reconfiguring"}
			records[id] = v
		}, Config: config("Repair while reconfiguring", "enabled = true", rotated), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_voice_provider.test", "runtime_status", "reconfiguring"), checkReplacements(5))},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			v := records[id]
			v.Runtime = client.VoiceProviderRuntime{State: "error", ErrorCode: "provider_unavailable"}
			records[id] = v
		}, Config: config("Repair while error", "enabled = true", secret), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_voice_provider.test", "runtime_status", "error"), checkReplacements(6))},
		{Config: config("Renamed", "enabled = false", rotated), Check: checkReplacements(7)},
		{Config: config("Renamed", "enabled = false", `secrets = null`), Check: checkReplacements(7)},
		{Config: config("Renamed", "enabled = false", `secrets = { public_key = { action = "keep" } }`), Check: checkReplacements(7)},
		{Config: config("Renamed", "enabled = false", `secrets = { public_key = { action = "keep" } }`), PlanOnly: true},
	}})
}
