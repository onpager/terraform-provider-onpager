package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// This uses the real Terraform protocol and an API fixture to expose unstable
// nested defaults, secret persistence, import writes and computed Telegram scope.
func TestBotTerraformFiveMessengersDefaultsImportDriftAndSecrets(t *testing.T) {
	binary, e := exec.LookPath("terraform")
	if e != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	records := map[string]client.Bot{}
	creates, writes, replacements := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.Header.Get("If-Match") != "" || q.URL.RawQuery != "" {
			t.Error("versioned or scanning request")
		}
		id := strings.TrimPrefix(q.URL.Path, "/v1/bots/")
		if q.Method == "POST" && q.URL.Path == "/v1/bots" {
			var in client.BotCreateInput
			if json.NewDecoder(q.Body).Decode(&in) != nil {
				w.WriteHeader(422)
				return
			}
			creates++
			writes++
			id = fmt.Sprintf("%08x-1111-4111-8111-111111111111", creates)
			keys := []string{}
			for key, ch := range in.Secrets {
				if ch.Action == "replace" {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			scope := map[string]string{"telegram": "bot_account", "pachca": "bot_account", "slack": "workspace", "mattermost": "team", "discord": "guild"}[in.Messenger]
			records[id] = client.Bot{ID: id, Messenger: in.Messenger, BotSettings: in.BotSettings, AddressingScope: client.BotScope{Kind: scope, ID: in.AddressingScopeID}, SecretKeys: keys, Runtime: client.BotRuntime{State: "disabled"}}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(records[id])
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
			json.NewEncoder(w).Encode(v)
		case "PUT":
			var in client.BotInput
			if json.NewDecoder(q.Body).Decode(&in) != nil {
				w.WriteHeader(422)
				return
			}
			writes++
			for _, ch := range in.Secrets {
				if ch.Action == "replace" {
					replacements++
				}
			}
			v.BotSettings = in.BotSettings
			records[id] = v
			w.WriteHeader(204)
		case "DELETE":
			writes++
			delete(records, id)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", q.Method, q.URL)
			w.WriteHeader(400)
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
`, server.URL)
	config := func(suffix string) string {
		var b strings.Builder
		b.WriteString(header)
		for _, messenger := range []string{"telegram", "pachca", "slack", "mattermost", "discord"} {
			baseURL := "http://mock:8080/" + messenger
			if suffix == "-updated" {
				baseURL = "http://MOCK:8080/v2/" + messenger + "/"
			} else if suffix != "" && messenger != "mattermost" {
				baseURL = ""
			}
			b.WriteString(botAcceptanceConfiguration("Passive", messenger, "", suffix, baseURL))
		}
		return b.String()
	}
	var telegramID string
	capture := func(s *terraform.State) error {
		telegramID = s.RootModule().Resources["onpager_bot.telegram"].Primary.ID
		return nil
	}
	steps := []resource.TestStep{{Config: config(""), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_bot.telegram", "configuration.mode", "polling"), resource.TestCheckResourceAttr("onpager_bot.telegram", "configuration.allowed_updates.#", "3"), resource.TestCheckResourceAttr("onpager_bot.slack", "configuration.mode", ""), resource.TestCheckResourceAttr("onpager_bot.discord", "pacing.requests", "10"))}, {Config: config(""), PlanOnly: true}}
	for _, m := range []string{"telegram", "pachca", "slack", "mattermost", "discord"} {
		steps = append(steps, resource.TestStep{ResourceName: "onpager_bot." + m, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secrets"}})
	}
	steps = append(steps, resource.TestStep{Config: config("-updated"), Check: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		for _, bot := range records {
			if bot.Configuration.BaseURL != "http://mock:8080/v2/"+bot.Messenger {
				return fmt.Errorf("unexpected API URL for %s: %q", bot.Messenger, bot.Configuration.BaseURL)
			}
		}
		if replacements != 0 {
			return fmt.Errorf("metadata update replayed %d retained replacements", replacements)
		}
		return nil
	}}, resource.TestStep{Config: config("-updated"), PlanOnly: true}, resource.TestStep{PreConfig: func() {
		mu.Lock()
		defer mu.Unlock()
		v := records[telegramID]
		v.Configuration.BaseURL = "http://other-mock/telegram"
		records[telegramID] = v
	}, Config: config("-updated"), Check: resource.TestCheckResourceAttr("onpager_bot.telegram", "configuration.base_url", "http://MOCK:8080/v2/telegram/")}, resource.TestStep{Config: config("-updated"), PlanOnly: true})
	// Runtime observations must never gate configuration or credential repairs.
	// This server only stores JSON; enabled fixtures cannot contact a messenger.
	activeConfig := func(state string) string {
		c := strings.ReplaceAll(config("-"+state), "enabled = false", "enabled = true")
		return strings.ReplaceAll(c, "tfacc-passive-synthetic-token", "tfacc-repaired-synthetic-token-"+state)
	}
	for _, state := range []string{"starting", "reconfiguring", "error"} {
		var beforeWrites, beforeReplacements int
		steps = append(steps, resource.TestStep{
			PreConfig: func() {
				mu.Lock()
				defer mu.Unlock()
				beforeWrites, beforeReplacements = writes, replacements
				for id, v := range records {
					v.Runtime = client.BotRuntime{State: state}
					if state == "error" {
						v.Runtime.ErrorCode = "provider_unavailable"
					}
					records[id] = v
				}
			},
			Config: activeConfig(state),
			Check: func(s *terraform.State) error {
				mu.Lock()
				defer mu.Unlock()
				if writes-beforeWrites != 5 || replacements-beforeReplacements != 5 {
					return fmt.Errorf("runtime %s blocked five bot repairs: writes=%d replacements=%d", state, writes-beforeWrites, replacements-beforeReplacements)
				}
				for _, m := range []string{"telegram", "pachca", "slack", "mattermost", "discord"} {
					if err := resource.TestCheckResourceAttr("onpager_bot."+m, "runtime.state", state)(s); err != nil {
						return err
					}
				}
				return resource.TestCheckResourceAttr("onpager_bot.telegram", "configuration.base_url", "")(s)
			},
		})
	}
	var beforeRuntimeWrites int
	runtimePlanChecks := []plancheck.PlanCheck{}
	for _, m := range []string{"telegram", "pachca", "slack", "mattermost", "discord"} {
		runtimePlanChecks = append(runtimePlanChecks, plancheck.ExpectResourceAction("onpager_bot."+m, plancheck.ResourceActionNoop))
	}
	steps = append(steps, resource.TestStep{
		PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			beforeRuntimeWrites = writes
			for id, v := range records {
				v.Runtime = client.BotRuntime{State: "running", ObservedAt: "2026-11-16T09:00:00Z", LastProbeAt: "2026-11-16T09:00:00Z"}
				records[id] = v
			}
		},
		Config:           activeConfig("error"),
		ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: runtimePlanChecks},
		Check: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if writes != beforeRuntimeWrites {
				return fmt.Errorf("runtime-only change caused %d bot mutations", writes-beforeRuntimeWrites)
			}
			return nil
		},
	})
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if len(records) != 0 {
			return fmt.Errorf("destroy retained bots: creates=%d writes=%d remaining=%d", creates, writes, len(records))
		}
		if creates != 5 {
			return fmt.Errorf("unexpected recreation: %d", creates)
		}
		return nil
	}, Steps: steps})
	_ = writes
}
