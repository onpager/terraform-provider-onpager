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

const inhibitionJSON = `{"id":"11111111-1111-4111-8111-111111111111","revision":7,"name":"Passive","source_expression":"false","target_expression":"false","matchers":[{"source":" app/name ","target":"target.name"}],"resolved_target_action":"keep","enabled":false,"index_state":"ready","author":{"user_id":null,"service_account_id":"22222222-2222-4222-8222-222222222222"},"created_at":"2026-11-04T00:00:00Z","updated_at":"2026-11-04T00:00:00Z"}`

func inhibitionInput() client.InhibitionInput {
	return client.InhibitionInput{Name: "Passive", SourceExpression: "false", TargetExpression: "false", Matchers: []client.InhibitionMatcher{{Source: " app/name ", Target: "target.name"}}, ResolvedTargetAction: "keep", Enabled: false}
}

// Omitting false, using index-state as CAS, or disabling on destroy breaks this lifecycle.
func TestInhibitionAtomicDisabledCreateOwnRevisionAndDelete(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.RawQuery != "" {
			t.Fatal("unexpected query")
		}
		if calls == 1 {
			if r.Method != "POST" || r.URL.Path != "/v1/inhibition-rules" || r.Header.Get("If-Match") != "" {
				t.Fatal("wrong create route")
			}
		} else if r.URL.Path != "/v1/inhibition-rules/"+teamID {
			t.Fatal("not an exact UUID route")
		}
		switch r.Method {
		case "POST", "PUT":
			var body map[string]json.RawMessage
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Fatal(e)
			}
			if len(body) != 6 || string(body["enabled"]) != "false" || string(body["matchers"]) != `[{"source":" app/name ","target":"target.name"}]` {
				t.Fatalf("lost typed false/literal keys: %s", body)
			}
			if r.Method == "PUT" {
				if r.Header.Get("If-Match") != `"7"` {
					t.Fatal("wrong own CAS")
				}
				fmt.Fprintf(w, `{"id":%q,"revision":8}`, teamID)
			} else {
				w.WriteHeader(201)
				io.WriteString(w, inhibitionJSON)
			}
		case "GET":
			if r.Header.Get("If-Match") != "" {
				t.Fatal("read sent CAS")
			}
			io.WriteString(w, inhibitionJSON)
		case "DELETE":
			if r.Header.Get("If-Match") != `"8"` {
				t.Fatal("delete lost CAS")
			}
			b, _ := io.ReadAll(r.Body)
			if len(b) != 0 {
				t.Fatal("DELETE has body")
			}
			w.WriteHeader(204)
		default:
			t.Fatal("unexpected operation")
		}
	})
	got, e := c.CreateInhibitionRule(t.Context(), inhibitionInput())
	if e != nil || !got.Committed || got.Enabled || got.Revision != 7 {
		t.Fatalf("create: %+v %v", got, e)
	}
	got, e = c.GetInhibitionRule(t.Context(), teamID)
	if e != nil || got.Enabled || got.IndexState != "ready" {
		t.Fatalf("direct disabled read: %+v %v", got, e)
	}
	receipt, e := c.UpdateInhibitionRule(t.Context(), teamID, 7, inhibitionInput())
	if e != nil || !receipt.Committed || receipt.ID != teamID || receipt.Revision != 8 {
		t.Fatalf("update: %+v %v", receipt, e)
	}
	if e = c.DeleteInhibitionRule(t.Context(), teamID, 8); e != nil {
		t.Fatal(e)
	}
	if calls != 4 {
		t.Fatalf("unexpected mutation replay: %d", calls)
	}
}

// A zero-valued bool is valid only when the server actually included enabled:false.
func TestInhibitionRejectsMissingNullMalformedAndForeignSuccessfulReads(t *testing.T) {
	cases := []string{`{}`, strings.Replace(inhibitionJSON, `"enabled":false,`, "", 1), strings.Replace(inhibitionJSON, `"enabled":false`, `"enabled":null`, 1), strings.Replace(inhibitionJSON, `"matchers":[{"source":" app/name ","target":"target.name"}]`, `"matchers":[{"source":" app/name "}]`, 1), strings.Replace(inhibitionJSON, `"index_state":"ready"`, `"index_state":"alien"`, 1), strings.Replace(inhibitionJSON, teamID, serviceID, 1), strings.Replace(inhibitionJSON, `"revision":7`, `"revision":0`, 1)}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			if _, e := c.GetInhibitionRule(t.Context(), teamID); e == nil {
				t.Fatal("accepted malformed read")
			}
		})
	}
}

func TestInhibitionMalformedCommittedResponsesKeepOnlyTrustedIdentity(t *testing.T) {
	for _, tc := range []struct {
		body, id string
		revision int64
	}{
		{`{"id":"11111111-1111-4111-8111-111111111111","revision":8}`, teamID, 8},
		{`{"id":"22222222-2222-4222-8222-222222222222","revision":8}`, "", 0},
		{`{"id":"11111111-1111-4111-8111-111111111111"}`, teamID, 0},
		{`{`, "", 0},
	} {
		t.Run(tc.body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) })
			v, e := c.UpdateInhibitionRule(t.Context(), teamID, 7, inhibitionInput())
			valid := tc.body == `{"id":"11111111-1111-4111-8111-111111111111","revision":8}`
			if (e == nil) != valid || !v.Committed || v.ID != tc.id || v.Revision != tc.revision {
				t.Fatalf("unsafe receipt %+v %v", v, e)
			}
		})
	}
	// A full create may be incomplete after committing; identity/revision must survive diagnostics.
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%q,"revision":1}`, teamID)
	})
	v, e := c.CreateInhibitionRule(t.Context(), inhibitionInput())
	if e == nil || !v.Committed || v.ID != teamID || v.Revision != 1 {
		t.Fatalf("lost partial create %+v %v", v, e)
	}
}

func TestInhibitionErrorsDoNotReplayOrConfirmUnauthorizedAbsence(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 412, 422, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"code":"not_found","message":"credential secret"}}`)
			})
			v, e := c.UpdateInhibitionRule(t.Context(), teamID, 7, inhibitionInput())
			var api *client.APIError
			if !errors.As(e, &api) || api.StatusCode != status || v.Committed || calls != 1 || strings.Contains(e.Error(), "secret") {
				t.Fatalf("unsafe error %+v %v", v, e)
			}
			e = c.DeleteInhibitionRule(t.Context(), teamID, 7)
			if (e == nil) != (status == 404) || calls != 2 {
				t.Fatal("retirement status swallowed/replayed")
			}
		})
	}
}

func TestInhibitionInvalidIdentityOrRevisionNeverWrites(t *testing.T) {
	c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid CAS made request") })
	for _, revision := range []int64{0, -1} {
		if _, e := c.UpdateInhibitionRule(t.Context(), teamID, revision, inhibitionInput()); e == nil {
			t.Fatal("invalid revision accepted")
		}
		if e := c.DeleteInhibitionRule(t.Context(), teamID, revision); e == nil {
			t.Fatal("invalid delete revision accepted")
		}
	}
	if _, e := c.GetInhibitionRule(t.Context(), "invalid"); e == nil {
		t.Fatal("invalid UUID accepted")
	}
}

// A malformed nonidentity field must not erase a usable committed UUID/revision.
func TestInhibitionCommittedCreateKeepsIdentityWhenConfigurationTypeIsMalformed(t *testing.T) {
	body := strings.Replace(inhibitionJSON, `"enabled":false`, `"enabled":"false"`, 1)
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); io.WriteString(w, body) })
	v, e := c.CreateInhibitionRule(t.Context(), inhibitionInput())
	if e == nil || !v.Committed || v.ID != teamID || v.Revision != 7 {
		t.Fatalf("lost usable committed identity after malformed settings: %+v %v", v, e)
	}
}
