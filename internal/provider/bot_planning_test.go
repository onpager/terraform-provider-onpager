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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func botFiveConfiguration(prefix, suffix string) string {
	var b strings.Builder
	for _, m := range []string{"telegram", "pachca", "slack", "mattermost", "discord"} {
		extra := ""
		cfg := "{}"
		secrets := `token = { action = "replace", value = "tfacc-passive-synthetic-token" }`
		if m == "telegram" {
			extra = `enrollment_url = "https://t.me/terraform_passive"`
		} else {
			extra = `addressing_scope_id = "terraform-passive-scope"`
		}
		if m == "slack" {
			secrets += `
 app_token = { action = "replace", value = "tfacc-passive-synthetic-app" }`
		}
		if m == "mattermost" {
			cfg = `{base_url = "https://terraform-passive.invalid"}`
			secrets += `
 action_secret = { action = "replace", value = "tfacc-passive-synthetic-action" }`
		}
		fmt.Fprintf(&b, "\nresource \"onpager_bot\" %q {\n messenger = %q\n display_name = %q\n enabled = false\n %s\n configuration = %s\n pacing = {}\n secrets = {\n %s\n }\n}\n", m, m, prefix+"-"+m+suffix, extra, cfg, secrets)
	}
	return b.String()
}

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
			if in.Enabled {
				t.Error("enabled fixture could start runtime")
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
			if in.Enabled {
				t.Error("enabled fixture")
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
	config := func(s string) string { return header + botFiveConfiguration("Passive", s) }
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
		if replacements != 0 {
			return fmt.Errorf("metadata update replayed %d retained replacements", replacements)
		}
		return nil
	}}, resource.TestStep{Config: config("-updated"), PlanOnly: true}, resource.TestStep{PreConfig: func() {
		mu.Lock()
		defer mu.Unlock()
		v := records[telegramID]
		v.DisplayName = "External drift"
		records[telegramID] = v
	}, Config: config("-updated"), Check: resource.TestCheckResourceAttr("onpager_bot.telegram", "display_name", "Passive-telegram-updated")}, resource.TestStep{Config: config("-updated"), PlanOnly: true})
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
