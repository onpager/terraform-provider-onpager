package client_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func webhookResponse(w http.ResponseWriter, id, team, tag string, enabled bool) {
	w.Header().Set("ETag", tag)
	scope := "null"
	if team != "" {
		scope = fmt.Sprintf("%q", team)
	}
	fmt.Fprintf(w, `{"id":%q,"scope":{"team_id":%s},"purpose":"notification","display_name":"Passive","enabled":%t,"etag":%q,"request":{"method":"POST","endpoint":"https://passive.invalid/hook","body_format":"json","body_template":"{}","verify_ssl":true,"timeout_seconds":5,"auth":{"kind":"none"}},"recipient_fields":[],"secrets":[{"key":"token","has_value":true}],"runtime_status":"disabled"}`, id, scope, enabled, tag)
}

func TestOutgoingWebhookDirectLifecycle(t *testing.T) {
	for _, team := range []string{"", iamRecipient} {
		t.Run(team, func(t *testing.T) {
			step := 0
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				step++
				base := "/v1/outgoing-webhooks"
				if team != "" {
					base = "/v1/teams/" + team + "/outgoing-webhooks"
				}
				want := base
				if step > 1 {
					want += "/" + iamID
				}
				if r.URL.Path != want || r.URL.RawQuery != "" {
					t.Errorf("unexpected route: %s", r.URL)
				}
				if step >= 3 && r.Header.Get("If-Match") != `"7"` {
					t.Error("missing own CAS")
				}
				if step == 1 || step == 3 {
					var b map[string]json.RawMessage
					if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
						t.Fatal(e)
					}
					if step == 1 && string(b["request_id"]) != fmt.Sprintf("%q", iamRecipient) {
						t.Error("missing create request UUID")
					}
					if string(b["recipient_fields"]) != `[]` || !strings.Contains(string(b["secrets"]), `"action":"replace"`) {
						t.Error("lost explicit empty/secret change")
					}
				}
				webhookResponse(w, iamID, team, `"7"`, false)
			})
			input := client.OutgoingWebhookInput{Purpose: "notification", DisplayName: "Passive", Request: client.OutgoingWebhookRequest{Method: "POST", Endpoint: "https://passive.invalid/hook", BodyFormat: "json", BodyTemplate: "{}", Auth: client.OutgoingWebhookAuth{Kind: "none"}}, Secrets: map[string]client.OutgoingWebhookSecretChange{"token": {Action: "replace", Value: "private-value"}}}
			if _, e := c.CreateOutgoingWebhook(t.Context(), team, iamRecipient, input); e != nil {
				t.Fatal(e)
			}
			if _, e := c.GetOutgoingWebhook(t.Context(), team, iamID); e != nil {
				t.Fatal(e)
			}
			if _, e := c.UpdateOutgoingWebhook(t.Context(), team, iamID, `"7"`, input); e != nil {
				t.Fatal(e)
			}
			if _, e := c.DisableOutgoingWebhook(t.Context(), team, iamID, `"7"`); e != nil {
				t.Fatal(e)
			}
			if step != 4 {
				t.Fatalf("requests=%d", step)
			}
		})
	}
}

func TestOutgoingWebhookRejectsUnsafeResponses(t *testing.T) {
	for _, mode := range []string{"weak", "mismatch", "foreign", "wrong_scope", "missing", "error"} {
		t.Run(mode, func(t *testing.T) {
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				id, team, tag := iamID, "", `"7"`
				switch mode {
				case "weak":
					tag = `W/"7"`
				case "foreign":
					id = iamRecipient
				case "wrong_scope":
					team = iamRecipient
				case "missing":
					w.Header().Set("ETag", tag)
					fmt.Fprintf(w, `{"id":%q,"etag":%q}`, id, tag)
					return
				case "error":
					w.WriteHeader(403)
					fmt.Fprintf(w, `{"id":%q,"error":{"code":"forbidden","message":"private-value"}}`, id)
					return
				}
				if mode == "mismatch" {
					w.Header().Set("ETag", `"8"`)
					fmt.Fprintf(w, `{"id":%q,"etag":%q}`, id, tag)
					return
				}
				webhookResponse(w, id, team, tag, false)
			})
			v, e := c.GetOutgoingWebhook(t.Context(), "", iamID)
			if e == nil {
				t.Fatal("unsafe response accepted")
			}
			if strings.Contains(e.Error(), "private-value") {
				t.Fatal("secret leaked")
			}
			if mode == "error" && v.ID != "" {
				t.Fatal("error body adopted")
			}
		})
	}
}

func TestOutgoingWebhookCommittedIdentityAndNoReplay(t *testing.T) {
	calls := 0
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("ETag", `"2"`)
		fmt.Fprintf(w, `{"id":%q,"etag":"\\\"2\\\""}`, iamID)
	})
	v, e := c.CreateOutgoingWebhook(t.Context(), "", iamRecipient, client.OutgoingWebhookInput{})
	if e == nil || !v.Committed || v.ID != iamID || calls != 1 {
		t.Fatalf("lost committed identity: %+v %v calls=%d", v, e, calls)
	}
}

func TestOutgoingWebhookPartialETagAndScopeAreNeverTrusted(t *testing.T) {
	for _, mode := range []string{"tag", "scope"} {
		t.Run(mode, func(t *testing.T) {
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"8"`)
				if mode == "tag" {
					fmt.Fprintf(w, `{"id":%q,"etag":"\"7\""}`, iamID)
				} else {
					fmt.Fprintf(w, `{"id":%q,"scope":{"team_id":%q}}`, iamID, iamRecipient)
				}
			})
			v, e := c.CreateOutgoingWebhook(t.Context(), "", iamRecipient, client.OutgoingWebhookInput{})
			if e == nil || !v.Committed {
				t.Fatal("malformed mutation accepted")
			}
			if mode == "tag" && v.ETag != "" {
				t.Fatal("unvalidated partial ETag adopted")
			}
			if mode == "scope" && v.ID != "" {
				t.Fatal("foreign scope identity adopted")
			}
		})
	}
}

func TestOutgoingWebhookForeignUpdateDoesNotExposeForeignRevision(t *testing.T) {
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) { webhookResponse(w, iamRecipient, "", `"99"`, false) })
	v, e := c.UpdateOutgoingWebhook(t.Context(), "", iamID, `"7"`, client.OutgoingWebhookInput{})
	if e == nil || !v.Committed || v.ID != "" || v.ETag != "" {
		t.Fatal("foreign mutation receipt exposed for recovery")
	}
}

func TestOutgoingWebhookIncompleteNestedMetadataIsAnError(t *testing.T) {
	for _, mode := range []string{"secret_presence", "auth_kind", "pair_value", "recipient_required"} {
		t.Run(mode, func(t *testing.T) {
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				recorder := httptest.NewRecorder()
				webhookResponse(recorder, iamID, "", `"7"`, false)
				var body map[string]any
				json.Unmarshal(recorder.Body.Bytes(), &body)
				request := body["request"].(map[string]any)
				switch mode {
				case "secret_presence":
					body["secrets"] = []any{map[string]any{"key": "token"}}
				case "auth_kind":
					request["auth"] = map[string]any{}
				case "pair_value":
					request["query"] = []any{map[string]any{"key": "missing"}}
				case "recipient_required":
					body["recipient_fields"] = []any{map[string]any{"key": "mail", "kind": "email", "label": "Mail"}}
				}
				w.Header().Set("ETag", `"7"`)
				json.NewEncoder(w).Encode(body)
			})
			if _, e := c.GetOutgoingWebhook(t.Context(), "", iamID); e == nil {
				t.Fatal("incomplete nested fields accepted")
			}
		})
	}
}
