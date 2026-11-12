package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Every case owns a fresh disabled configuration and synthetic credentials; no
// contacts, targets or messenger network validation are performed. Disabled
// configurations do not require a deployment callback origin.
func TestAccBots(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("live API acceptance is opt-in")
	}
	for _, tc := range []struct{ name, messenger, mode string }{
		{"telegram", "telegram", ""}, {"pachca", "pachca", ""},
		{"slack", "slack", ""}, {"mattermost", "mattermost", ""},
		{"discord", "discord", ""}, {"telegram_webhook", "telegram", "webhook"},
		{"pachca_webhook", "pachca", "webhook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messenger := tc.messenger
			c, prefix := routingAccSetup(t)
			var id string
			t.Cleanup(func() {
				if id != "" {
					if e := c.DeleteBot(context.Background(), id); e != nil {
						t.Errorf("cleanup %s bot: %v", messenger, e)
					}
				}
			})
			name := "onpager_bot." + messenger
			config := func(suffix string) string {
				return routingAccHeader + botAcceptanceConfiguration(prefix, messenger, tc.mode, suffix)
			}
			capture := func(s *terraform.State) error {
				// Capture the exact case-owned identity before a verification error
				// can stop cleanup, including an apply whose check never ran.
				if s != nil {
					if row := s.RootModule().Resources[name]; row != nil && row.Primary != nil && row.Primary.ID != "" {
						id = row.Primary.ID
					}
				}
				return nil
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: routingAccFactories(),
				CheckDestroy: func(s *terraform.State) error {
					capture(s)
					if id == "" {
						return nil
					}
					if _, e := c.GetBot(t.Context(), id); !client.IsNotFound(e) {
						return fmt.Errorf("%s bot remained after destroy: %v", messenger, e)
					}
					return nil
				},
				Steps: []resource.TestStep{
					{Config: config(""), Check: resource.ComposeTestCheckFunc(capture,
						resource.TestCheckResourceAttr(name, "enabled", "false"),
						resource.TestCheckResourceAttr(name, "runtime.state", "disabled"))},
					{Config: config(""), PlanOnly: true},
					{ResourceName: name, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secrets"}},
					{Config: config("-updated"), Check: resource.ComposeTestCheckFunc(capture,
						resource.TestCheckResourceAttr(name, "display_name", prefix+"-"+messenger+"-updated"))},
					{Config: config("-updated"), PlanOnly: true},
					{PreConfig: func() {
						v, e := c.GetBot(t.Context(), id)
						if e != nil {
							t.Fatal(e)
						}
						in := client.BotInput{BotSettings: v.BotSettings}
						in.DisplayName = prefix + "-external-drift"
						if _, e = c.UpdateBot(t.Context(), id, messenger, in); e != nil {
							t.Fatal(e)
						}
					}, Config: config("-updated"), Check: resource.ComposeTestCheckFunc(capture,
						resource.TestCheckResourceAttr(name, "display_name", prefix+"-"+messenger+"-updated"))},
					{Config: config("-updated"), PlanOnly: true},
				},
			})
		})
	}
}

func botAcceptanceConfiguration(prefix, messenger, mode, suffix string) string {
	extra := `addressing_scope_id = "terraform-passive-scope"`
	configuration := "{}"
	secrets := `token = { action = "replace", value = "tfacc-passive-synthetic-token" }`
	switch messenger {
	case "telegram":
		extra = `enrollment_url = "https://t.me/terraform_passive"`
	case "slack":
		secrets += `
 app_token = { action = "replace", value = "tfacc-passive-synthetic-app" }`
	case "mattermost":
		configuration = `{base_url = "https://terraform-passive.invalid"}`
		secrets += `
 action_secret = { action = "replace", value = "tfacc-passive-synthetic-action" }`
	}
	if mode == "webhook" {
		configuration = `{mode = "webhook"}`
		secrets += `
 webhook_secret = { action = "replace", value = "tfacc-passive-synthetic-webhook" }`
	}
	return fmt.Sprintf(`
resource "onpager_bot" %q {
 messenger = %q
 display_name = %q
 enabled = false
 %s
 configuration = %s
 pacing = {}
 secrets = {
 %s
 }
}
`, messenger, messenger, prefix+"-"+messenger+suffix, extra, configuration, secrets)
}

// Rejected enable/default-enabled requests must not commit an enabled fixture.
// These checks require an absent origin, observed through this exact owned bot;
// no deployment inspection or mutation is part of the resource or test setup.
func TestAccBotCallbackPreconditions(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("live API acceptance is opt-in")
	}
	for _, messenger := range []string{"mattermost", "telegram", "pachca"} {
		t.Run(messenger, func(t *testing.T) {
			c, prefix := routingAccSetup(t)
			in := client.BotCreateInput{Messenger: messenger, AddressingScopeID: prefix + "-scope", BotInput: client.BotInput{
				BotSettings: client.BotSettings{DisplayName: prefix, Enabled: false, Visibility: "global", TeamIDs: []string{}, Pacing: client.BotPacing{Requests: 10, WindowMS: 1000}},
				Secrets:     map[string]client.BotSecretChange{"token": {Action: "replace", Value: "tfacc-passive-synthetic-token"}},
			}}
			if messenger == "mattermost" {
				in.Configuration.BaseURL = "https://terraform-passive.invalid"
				in.Secrets["action_secret"] = client.BotSecretChange{Action: "replace", Value: "tfacc-passive-synthetic-action"}
			} else {
				in.Configuration.Mode = "webhook"
				in.Secrets["webhook_secret"] = client.BotSecretChange{Action: "replace", Value: "tfacc-passive-synthetic-webhook"}
				if messenger == "telegram" {
					in.AddressingScopeID = ""
					in.EnrollmentURL = "https://t.me/terraform_passive"
					in.Configuration.AllowedUpdates = []string{"callback_query", "message", "my_chat_member"}
				}
			}
			created, err := c.CreateBot(t.Context(), in)
			t.Cleanup(func() {
				if created.ID == "" {
					return
				}
				if err := c.DeleteBot(context.Background(), created.ID); err != nil {
					t.Errorf("cleanup callback-contract bot: %v", err)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			readDisabled := func() client.Bot {
				t.Helper()
				v, err := c.GetBot(t.Context(), created.ID)
				if err != nil {
					t.Fatal(err)
				}
				if v.Enabled || v.Runtime.State != "disabled" {
					t.Fatal("callback-contract fixture must remain disabled")
				}
				return v
			}
			before := readDisabled()
			if before.CallbackURL != "" {
				t.Skip("negative callback-origin checks require a stand without BOT_CALLBACK_ORIGIN; disabled lifecycle is covered independently")
			}
			// Disabled metadata changes retain the existing credential set.
			update := client.BotInput{BotSettings: before.BotSettings}
			update.DisplayName += "-metadata"
			if _, err := c.UpdateBot(t.Context(), created.ID, messenger, update); err != nil {
				t.Fatal(err)
			}
			before = readDisabled()
			if before.DisplayName != update.DisplayName || before.CallbackURL != "" || !reflect.DeepEqual(before.SecretKeys, created.SecretKeys) {
				t.Fatal("disabled metadata update did not preserve configuration/credential metadata")
			}
			toObject := func(v any) map[string]any {
				t.Helper()
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal("encode callback-contract fixture")
				}
				var out map[string]any
				if json.Unmarshal(raw, &out) != nil {
					t.Fatal("decode callback-contract fixture")
				}
				return out
			}
			for _, explicit := range []bool{false, true} {
				body := toObject(in)
				delete(body, "enabled")
				if explicit {
					body["enabled"] = true
				}
				botAccExpectError(t, c, http.MethodPost, "/v1/bots", "", body, 422, "bot_callback_origin_required")
			}
			body := toObject(update)
			body["display_name"] = prefix + "-must-not-commit"
			body["secrets"] = map[string]any{"proxy_url": map[string]any{"action": "replace", "value": "http://127.0.0.1:9"}}
			delete(body, "enabled")
			botAccExpectError(t, c, http.MethodPut, "/v1/bots/"+created.ID, created.ID, body, 400, "")
			body["enabled"] = true
			botAccExpectError(t, c, http.MethodPut, "/v1/bots/"+created.ID, created.ID, body, 422, "bot_callback_origin_required")
			after := readDisabled()
			if !reflect.DeepEqual(before.BotSettings, after.BotSettings) || !reflect.DeepEqual(before.SecretKeys, after.SecretKeys) || after.CallbackURL != "" {
				t.Fatal("rejected enable/missing-enabled request changed committed settings or credential keys")
			}
		})
	}
}

func botAccExpectError(t *testing.T, c *client.Client, method, route, ownedID string, body any, wantStatus int, wantCode string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal("encode callback-contract request")
	}
	request, err := http.NewRequestWithContext(t.Context(), method, strings.TrimRight(os.Getenv("ONPAGER_ACC_ENDPOINT"), "/")+route, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("construct callback-contract request")
	}
	request.Header.Set("Authorization", "Bearer "+os.Getenv("ONPAGER_ACC_TOKEN"))
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := transport.Do(request)
	if err != nil {
		t.Fatal("callback-contract request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		t.Fatal("read callback-contract response")
	}
	var envelope struct {
		ID    string `json:"id"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &envelope)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if method == http.MethodPost {
			ownedID = envelope.ID
		}
		if ownedID != "" {
			if err := c.DeleteBot(context.Background(), ownedID); err != nil {
				t.Error("cleanup unexpectedly committed callback-contract request failed")
			}
		}
	}
	if response.StatusCode != wantStatus || wantCode != "" && envelope.Error.Code != wantCode {
		t.Fatalf("%s callback-contract request: HTTP %d, expected HTTP %d and the documented error code", method, response.StatusCode, wantStatus)
	}
}
