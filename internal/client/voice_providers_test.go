package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const voiceID = "11111111-1111-4111-8111-111111111111"

func voiceClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := client.New(s.URL+"/control", "test-token", client.WithTimezone("UTC"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func voiceResponse(w http.ResponseWriter) {
	fmt.Fprintf(w, `{"id":%q,"kind":"zvonok","display_name":"Passive voice","enabled":false,"visibility":"global","configuration":{"call_campaign_id":"calls","verification_campaign_id":"verify","max_text_runes":500},"team_ids":[],"secret_keys":["public_key"],"runtime":{"state":"disabled"}}`, voiceID)
}

func voiceInput() client.VoiceProviderInput {
	return client.VoiceProviderInput{DisplayName: "Passive voice", Enabled: false, Visibility: "global", Configuration: client.VoiceProviderConfiguration{CallCampaignID: "calls", VerificationCampaignID: "verify", MaxTextRunes: 500}, Secrets: map[string]client.VoiceProviderSecretChange{"public_key": {Action: "replace", Value: "private-voice-marker"}}}
}

func TestVoiceProviderClientUnversionedDirectLifecycle(t *testing.T) {
	var calls []string
	c := voiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		want := "/control/v1/voice-providers/" + voiceID
		if r.Method == "POST" {
			want = "/control/v1/voice-providers"
		}
		if r.URL.Path != want || r.Header.Get("If-Match") != "" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Onpager-Timezone") != "UTC" {
			t.Error("wrong direct route, credentials, timezone or invented CAS")
		}
		if r.Method == "POST" || r.Method == "PUT" {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if string(body["enabled"]) != "false" || string(body["team_ids"]) != "[]" || body["revision"] != nil || body["request_id"] != nil {
				t.Error("disabled/full settings payload lost or unsupported metadata added")
			}
			if r.Method == "POST" && string(body["kind"]) != `"zvonok"` || r.Method == "PUT" && body["kind"] != nil {
				t.Error("kind must be create-only")
			}
		}
		if r.Method == "PUT" || r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		if r.Method == "POST" {
			w.WriteHeader(201)
		}
		voiceResponse(w)
	})
	v, err := c.CreateVoiceProvider(t.Context(), "zvonok", voiceInput())
	if err != nil || !v.Committed || v.ID != voiceID || v.Enabled {
		t.Fatalf("create: %#v %v", v, err)
	}
	v, err = c.GetVoiceProvider(t.Context(), voiceID)
	if err != nil || v.Committed || v.Runtime.State != "disabled" {
		t.Fatalf("read: %#v %v", v, err)
	}
	v, err = c.UpdateVoiceProvider(t.Context(), voiceID, voiceInput())
	if err != nil || !v.Committed || v.ID != voiceID {
		t.Fatalf("update receipt: %#v %v", v, err)
	}
	if err = c.DeleteVoiceProvider(t.Context(), voiceID); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "POST,GET,PUT,DELETE" {
		t.Fatal("unexpected retry, discovery or follow-up operation", calls)
	}
}

func TestVoiceProviderClientRejectsMalformedViewsAndPreservesKnownCommittedIdentity(t *testing.T) {
	for _, mode := range []string{"missing", "null", "configuration", "runtime", "foreign_id", "kind", "teams", "secrets", "empty_secrets", "timestamp", "json", "enabled_type", "configuration_type"} {
		t.Run(mode, func(t *testing.T) {
			c := voiceClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					w.WriteHeader(201)
				}
				rec := httptest.NewRecorder()
				voiceResponse(rec)
				var body map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
				switch mode {
				case "missing":
					delete(body, "enabled")
				case "null":
					body["secret_keys"] = nil
				case "configuration":
					delete(body["configuration"].(map[string]any), "max_text_runes")
				case "runtime":
					body["runtime"] = map[string]any{}
				case "foreign_id":
					body["id"] = "22222222-2222-4222-8222-222222222222"
				case "kind":
					body["kind"] = "other"
				case "teams":
					body["team_ids"] = []string{"bad"}
				case "secrets":
					body["secret_keys"] = []string{"other"}
				case "empty_secrets":
					body["secret_keys"] = []string{}
				case "timestamp":
					body["runtime"].(map[string]any)["observed_at"] = "bad"
				case "enabled_type":
					body["enabled"] = "false"
				case "configuration_type":
					body["configuration"] = 42
				case "json":
					fmt.Fprint(w, "{")
					return
				}
				_ = json.NewEncoder(w).Encode(body)
			})
			v, err := c.GetVoiceProvider(t.Context(), voiceID)
			if err == nil || v.Committed {
				t.Fatal("malformed read accepted")
			}
			if mode == "foreign_id" {
				if v.ID != "" {
					t.Fatal("foreign read identity exposed")
				}
				return
			}
			v, err = c.CreateVoiceProvider(t.Context(), "zvonok", voiceInput())
			if err == nil || !v.Committed {
				t.Fatal("malformed committed create accepted or commit lost")
			}
			if mode == "kind" || mode == "json" {
				if v.ID != "" {
					t.Fatal("unusable identity exposed")
				}
			} else if mode != "foreign_id" && v.ID != voiceID {
				t.Fatal("known committed identity lost")
			}
			if mode == "empty_secrets" && (v.Kind != "" || v.DisplayName != "" || v.SecretKeys != nil || v.CallbackURL != "" || v.Configuration != (client.VoiceProviderConfiguration{})) {
				t.Fatal("malformed secret metadata exposed more than the committed identity")
			}
		})
	}
}

func TestVoiceProviderClientRejectsNonempty204WithoutLosingCommit(t *testing.T) {
	useHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
		fmt.Fprint(w, `{"secret":"private-voice-marker"}`)
	})
	c, err := client.New("https://control.invalid", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.UpdateVoiceProvider(t.Context(), voiceID, voiceInput())
	if err == nil || !v.Committed || v.ID != voiceID || strings.Contains(err.Error(), "private-voice-marker") {
		t.Fatal("nonempty204 accepted or committed identity lost", err)
	}
}

func TestVoiceProviderClientErrorsAreSafeAndNeverAdoptErrorBody(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 422, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := voiceClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"id":%q,"error":{"code":"forbidden","message":"private-voice-marker"}}`, voiceID)
			})
			v, err := c.CreateVoiceProvider(t.Context(), "zvonok", voiceInput())
			var api *client.APIError
			if !errors.As(err, &api) || api.StatusCode != status || v.ID != "" || v.Committed || strings.Contains(err.Error(), "private-voice-marker") || calls != 1 {
				t.Fatal("unsafe error or replay", err)
			}
		})
	}
	c := voiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
	})
	if err := c.DeleteVoiceProvider(t.Context(), voiceID); err != nil {
		t.Fatal("confirmed repeated delete failed", err)
	}
	for _, bad := range []string{"bad", "00000000-0000-0000-0000-000000000000"} {
		if _, err := c.GetVoiceProvider(t.Context(), bad); err == nil {
			t.Fatal("bad UUID accepted")
		}
	}
}

func TestVoiceProviderClientPreservesSecretOmission(t *testing.T) {
	c := voiceClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["secrets"] != nil {
			t.Error("omitted secrets were emitted")
		}
		w.WriteHeader(204)
	})
	in := voiceInput()
	in.Secrets = nil
	if _, err := c.UpdateVoiceProvider(t.Context(), voiceID, in); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", voiceInput(), voiceInput().Secrets["public_key"]), "private-voice-marker") {
		t.Fatal("secret formatting leak")
	}
}
