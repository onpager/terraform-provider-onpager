package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const botJSON = `{"id":"11111111-1111-4111-8111-111111111111","messenger":"telegram","display_name":"Passive","public_handle":"","enrollment_url":"https://t.me/terraform_passive","enabled":false,"visibility":"global","addressing_scope":{"kind":"bot_account","id":""},"configuration":{"mode":"polling","allowed_updates":["callback_query","message","my_chat_member"]},"pacing":{"requests":10,"window_ms":1000,"search_requests":0},"team_ids":[],"secret_keys":["token"],"runtime":{"state":"disabled"}}`

func botInput() client.BotInput {
	return client.BotInput{BotSettings: client.BotSettings{DisplayName: "Passive", EnrollmentURL: "https://t.me/terraform_passive", Enabled: false, Visibility: "global", Configuration: client.BotConfiguration{Mode: "polling", AllowedUpdates: []string{"callback_query", "message", "my_chat_member"}}, Pacing: client.BotPacing{Requests: 10, WindowMS: 1000}, TeamIDs: []string{}}, Secrets: map[string]client.BotSecretChange{"token": {Action: "replace", Value: "synthetic-secret"}}}
}

func TestBotUnversionedDisabledCRUDAndSecrets(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-Match") != "" || r.URL.RawQuery != "" {
			t.Error("unversioned bot request carried CAS/query")
		}
		if calls == 1 {
			if r.Method != "POST" || r.URL.Path != "/v1/bots" {
				t.Fatal("wrong create")
			}
		} else if r.URL.Path != "/v1/bots/"+teamID {
			t.Fatal("must use direct UUID")
		}
		switch r.Method {
		case "POST", "PUT":
			var b map[string]json.RawMessage
			if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
				t.Fatal(e)
			}
			if string(b["enabled"]) != "false" || b["revision"] != nil || b["key"] != nil || b["addressing_scope_id"] != nil {
				t.Fatalf("wrong atomic disabled payload: %s", b)
			}
			if r.Method == "PUT" {
				if b["messenger"] != nil {
					t.Fatal("PUT repeated immutable messenger")
				}
				w.WriteHeader(204)
			} else {
				w.WriteHeader(201)
				io.WriteString(w, botJSON)
			}
		case "GET":
			io.WriteString(w, botJSON)
		case "DELETE":
			b, _ := io.ReadAll(r.Body)
			if len(b) != 0 {
				t.Fatal("delete body")
			}
			w.WriteHeader(204)
		default:
			t.Fatal("unexpected operation")
		}
	})
	v, e := c.CreateBot(t.Context(), client.BotCreateInput{BotInput: botInput(), Messenger: "telegram"})
	if e != nil || !v.Committed || v.ID != teamID || v.Enabled {
		t.Fatalf("create: %+v %v", v, e)
	}
	if v, e = c.GetBot(t.Context(), teamID); e != nil || v.Runtime.State != "disabled" {
		t.Fatalf("read: %+v %v", v, e)
	}
	if v, e = c.UpdateBot(t.Context(), teamID, "telegram", botInput()); e != nil || !v.Committed || v.ID != teamID {
		t.Fatalf("update receipt: %+v %v", v, e)
	}
	if e = c.DeleteBot(t.Context(), teamID); e != nil || calls != 4 {
		t.Fatalf("delete: %v calls=%d", e, calls)
	}
}

func TestBotReadRejectsIncompleteNullForeignAndInvalidViews(t *testing.T) {
	bodies := []string{`{}`, strings.Replace(botJSON, `"enabled":false,`, "", 1), strings.Replace(botJSON, `"enabled":false`, `"enabled":null`, 1), strings.Replace(botJSON, `"requests":10`, `"requests":null`, 1), strings.Replace(botJSON, `"kind":"bot_account"`, `"kind":"workspace"`, 1), strings.Replace(botJSON, `"state":"disabled"`, `"state":"alien"`, 1), strings.Replace(botJSON, teamID, serviceID, 1), strings.Replace(botJSON, `"secret_keys":["token"]`, `"secret_keys":[]`, 1), strings.Replace(botJSON, `"team_ids":[]`, `"team_ids":null`, 1)}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			if _, e := c.GetBot(t.Context(), teamID); e == nil {
				t.Fatal("accepted unsafe view")
			}
		})
	}
}

func TestBotCommittedCreateRetainsIdentityAndErrorsNeverLeakOrReplay(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%q,"enabled":"bad"}`, teamID)
	})
	v, e := c.CreateBot(t.Context(), client.BotCreateInput{BotInput: botInput(), Messenger: "telegram"})
	if e == nil || !v.Committed || v.ID != teamID {
		t.Fatalf("lost committed identity: %+v %v", v, e)
	}
	for _, status := range []int{401, 403, 404, 409, 422, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				code := "bot_in_use"
				if status == 404 {
					code = "not_found"
				}
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"synthetic-secret"}}`, code)
			})
			v, e := c.UpdateBot(t.Context(), teamID, "telegram", botInput())
			var api *client.APIError
			if !errors.As(e, &api) || v.Committed || calls != 1 || strings.Contains(e.Error(), "synthetic-secret") {
				t.Fatalf("unsafe update %+v %v", v, e)
			}
			e = c.DeleteBot(t.Context(), teamID)
			if (e == nil) != (status == 404) || calls != 2 {
				t.Fatalf("delete status %v", e)
			}
		})
	}
}

func TestBotValidationAllMessengersAndSecretActions(t *testing.T) {
	for _, m := range []string{"telegram", "pachca", "slack", "mattermost", "discord"} {
		t.Run(m, func(t *testing.T) {
			in := botInput()
			scope := "scope"
			in.Configuration = client.BotConfiguration{}
			if m == "telegram" || m == "pachca" {
				in.Configuration.Mode = "polling"
			}
			if m == "telegram" {
				scope = ""
			}
			if m == "slack" {
				in.Secrets["app_token"] = client.BotSecretChange{Action: "replace", Value: "synthetic-app"}
			}
			if m == "mattermost" {
				in.Configuration.BaseURL = "https://chat.invalid"
				in.Secrets["action_secret"] = client.BotSecretChange{Action: "replace", Value: "synthetic-action"}
			}
			if e := client.ValidateBotCreateInput(client.BotCreateInput{BotInput: in, Messenger: m, AddressingScopeID: scope}); e != nil {
				t.Fatal(e)
			}
		})
	}
	for name, change := range map[string]func(*client.BotCreateInput){
		"telegram scope":          func(v *client.BotCreateInput) { v.AddressingScopeID = "123" },
		"required link":           func(v *client.BotCreateInput) { v.EnrollmentURL = "" },
		"whitespace label":        func(v *client.BotCreateInput) { v.DisplayName = " Passive" },
		"byte limit":              func(v *client.BotCreateInput) { v.DisplayName = strings.Repeat("é", 101) },
		"foreign configuration":   func(v *client.BotCreateInput) { v.Configuration.BaseURL = "https://foreign.invalid" },
		"required token disabled": func(v *client.BotCreateInput) { v.Secrets = nil },
		"secret action": func(v *client.BotCreateInput) {
			v.Secrets["token"] = client.BotSecretChange{Action: "keep", Value: "secret"}
		},
		"blank secret": func(v *client.BotCreateInput) {
			v.Secrets["token"] = client.BotSecretChange{Action: "replace", Value: " "}
		},
		"unknown secret": func(v *client.BotCreateInput) { v.Secrets["unknown"] = client.BotSecretChange{Action: "delete"} },
		"global grants":  func(v *client.BotCreateInput) { v.TeamIDs = []string{teamID} },
		"search quota":   func(v *client.BotCreateInput) { v.Pacing.SearchRequests = 2 },
		"bad update":     func(v *client.BotCreateInput) { v.Configuration.AllowedUpdates = []string{"UPPER"} },
		"secret proxy": func(v *client.BotCreateInput) {
			v.Secrets["proxy_url"] = client.BotSecretChange{Action: "replace", Value: "https://proxy.invalid?secret=marker"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := client.BotCreateInput{BotInput: botInput(), Messenger: "telegram"}
			change(&v)
			c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid configuration made request") })
			if _, e := c.CreateBot(t.Context(), v); e == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestBotRejectsNormalizingURLsAndRequiredSecretDeletionBeforeHTTP(t *testing.T) {
	for name, change := range map[string]func(*client.BotCreateInput){
		"normalizing enrollment": func(v *client.BotCreateInput) { v.EnrollmentURL = "https://chat.invalid/a b" },
	} {
		t.Run(name, func(t *testing.T) {
			v := client.BotCreateInput{BotInput: botInput(), Messenger: "telegram"}
			change(&v)
			c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Error("normalizing value made HTTP request") })
			if _, e := c.CreateBot(t.Context(), v); e == nil {
				t.Error("accepted server-normalizing URL")
			}
		})
	}
	in := botInput()
	in.Secrets = map[string]client.BotSecretChange{"token": {Action: "delete"}}
	c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Error("required secret deletion made HTTP request") })
	if _, e := c.UpdateBot(t.Context(), teamID, "telegram", in); e == nil {
		t.Error("required token deletion accepted")
	}
}

func TestBotReadRejectsMissingTransportDefaultsAndPreservesUnexpectedSuccessIdentity(t *testing.T) {
	for _, body := range []string{strings.Replace(botJSON, `"mode":"polling",`, "", 1), strings.Replace(botJSON, `"mode":"polling"`, `"mode":null`, 1), strings.Replace(botJSON, `"allowed_updates":["callback_query","message","my_chat_member"]`, `"allowed_updates":[]`, 1)} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, e := c.GetBot(t.Context(), teamID); e == nil {
			t.Error("accepted missing server transport defaults")
		}
	}
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, botJSON) })
	v, e := c.CreateBot(t.Context(), client.BotCreateInput{BotInput: botInput(), Messenger: "telegram"})
	if e == nil || !v.Committed || v.ID != teamID {
		t.Fatalf("unexpected successful status lost usable identity: %+v %v", v, e)
	}
}
