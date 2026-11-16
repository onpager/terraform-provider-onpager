package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const responderTeam = "11111111-1111-4111-8111-111111111111"
const responderChain = "22222222-2222-4222-8222-222222222222"
const responderZero = "00000000-0000-0000-0000-000000000000"
const responderAbsent = `{"team_id":"00000000-0000-0000-0000-000000000000","importance":"","revision":0,"strategy":"","chain_id":"00000000-0000-0000-0000-000000000000","chain_steps":null}`

func responderFixture(strategy string) string {
	if strategy == "team_fanout" {
		return fmt.Sprintf(`{"team_id":%q,"importance":"important","revision":7,"strategy":"team_fanout","chain_id":%q,"chain_steps":[]}`, responderTeam, responderZero)
	}
	return fmt.Sprintf(`{"team_id":%q,"importance":"important","revision":7,"strategy":"team_chain","chain_id":%q,"chain_steps":[{"kind":"notify_user","target_user_id":%q,"importance":"important"},{"kind":"notify_oncall","schedule_id":%q},{"kind":"wait","wait_seconds":60}]}`, responderTeam, responderChain, responderTeam, responderChain)
}

// Catches accidental default-slot fallback, fake CAS, compatibility steps, and extra discovery.
func TestTeamResponderPolicyExactSlotAndServerDerivedPublication(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/prefix/v1/teams/"+responderTeam+"/responder-policies" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Onpager-Timezone") != "UTC" || r.Header.Get("If-Match") != "" {
			t.Error("lost direct authenticated slot or introduced CAS")
		}
		switch r.Method {
		case "GET":
			if r.URL.RawQuery != "importance=important" {
				t.Error("read omitted exact importance")
			}
			fmt.Fprint(w, responderAbsent)
		case "POST":
			var b map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&b)
			if r.URL.RawQuery != "" || len(b) != 3 || string(b["strategy"]) != `"team_chain"` || string(b["importance"]) != `"important"` || string(b["chain_id"]) != `"`+responderChain+`"` {
				t.Errorf("publication payload: %v", b)
			}
			w.WriteHeader(201)
			fmt.Fprint(w, responderFixture("team_chain"))
		case "DELETE":
			raw, _ := io.ReadAll(r.Body)
			if r.URL.RawQuery != "importance=important" || len(raw) != 0 {
				t.Error("reset omitted exact slot or added a body")
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected operation")
		}
	}))
	defer s.Close()
	c, _ := New(s.URL+"/prefix", "fixture-token", WithTimezone("UTC"))
	v, err := c.GetTeamResponderPolicy(t.Context(), responderTeam, "important")
	if err != nil || v.Configured || v.Committed {
		t.Fatalf("absence: %+v %v", v, err)
	}
	v, err = c.PublishTeamResponderPolicy(t.Context(), responderTeam, TeamResponderPolicyInput{Importance: "important", Strategy: "team_chain", ChainID: responderChain})
	if err != nil || !v.Configured || !v.Committed || v.Revision != 7 || len(v.ChainSteps) != 3 {
		t.Fatalf("publication: %+v %v", v, err)
	}
	if err = c.ResetTeamResponderPolicy(t.Context(), responderTeam, "important"); err != nil || calls != 3 {
		t.Fatalf("reset %v calls=%d", err, calls)
	}
}

func TestTeamResponderPolicyFanoutOmitsChainAndPreservesCommitOnMalformedSuccess(t *testing.T) {
	for _, body := range []string{responderFixture("team_fanout"), `{}`, `{"revision":8}`, `{`, strings.Repeat("x", (4<<20)+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var b map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&b)
				if len(b) != 2 || b["chain_id"] != nil || b["steps"] != nil {
					t.Error("fanout sent chain or compatibility snapshot")
				}
				w.WriteHeader(201)
				fmt.Fprint(w, body)
			}))
			defer s.Close()
			c, _ := New(s.URL, "secret-token")
			v, err := c.PublishTeamResponderPolicy(t.Context(), responderTeam, TeamResponderPolicyInput{Importance: "important", Strategy: "team_fanout"})
			if !v.Committed || calls != 1 || (err == nil) != (body == responderFixture("team_fanout")) {
				t.Fatalf("committed=%v err=%v calls=%d", v.Committed, err, calls)
			}
			if err != nil && (strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), body) && len(body) > 50) {
				t.Fatal("response/token leaked")
			}
		})
	}
}

func TestTeamResponderPolicyRejectsMalformedIdentityAndSnapshot(t *testing.T) {
	valid := responderFixture("team_chain")
	cases := []string{`{}`, `null`, `[]`, strings.Replace(responderAbsent, `"chain_steps":null`, `"chain_steps":[]`, 1), strings.Replace(responderAbsent, `"revision":0`, `"revision":null`, 1), strings.Replace(responderAbsent, `"team_id":"`+responderZero+`"`, `"team_id":"`+responderTeam+`"`, 1), strings.Replace(valid, `"important"`, `"default"`, 1), strings.Replace(valid, `"revision":7`, `"revision":0`, 1), strings.Replace(valid, `"wait_seconds":60`, `"wait_seconds":9223372037`, 1), strings.Replace(valid, `"wait_seconds":60`, `"wait_seconds":0`, 1), strings.Replace(valid, `"kind":"wait","wait_seconds":60`, `"kind":"notify_user","target_user_id":"`+responderTeam+`"`, 1), strings.Replace(valid, `"kind":"notify_user"`, `"kind":"notify_channel"`, 1), strings.Replace(valid, `"kind":"notify_user"`, `"kind":"notify_user","webhook_id":"`+responderTeam+`"`, 1), strings.Replace(valid, `"kind":"notify_user"`, `"kind":"notify_user","channel_sources":["team"]`, 1), strings.Replace(valid, `"kind":"notify_user"`, `"kind":"notify_user","target_channel_id":"`+responderTeam+`"`, 1), strings.Replace(valid, `"kind":"notify_user"`, `"kind":"notify_user","target_id":"`+responderTeam+`"`, 1), strings.Replace(valid, `"kind":"wait"`, `"kind":"wait","importance":"important"`, 1), strings.Replace(valid, `"kind":"notify_oncall"`, `"kind":"notify_oncall","target_user_id":"`+responderTeam+`"`, 1), strings.Replace(responderFixture("team_fanout"), `"chain_steps":[]`, `"chain_steps":null`, 1), strings.Replace(valid, `"chain_id":"`+responderChain+`"`, `"chain_id":"`+responderZero+`"`, 1), fmt.Sprintf(`{"team_id":%q,"importance":"important","revision":1,"strategy":"team_chain","chain_id":%q,"chain_steps":[{"kind":"wait","wait_seconds":1}]}`, responderTeam, responderChain)}
	for i, body := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer s.Close()
			c, _ := New(s.URL, "fixture-token")
			v, err := c.GetTeamResponderPolicy(t.Context(), responderTeam, "important")
			if err == nil || v.Configured {
				t.Fatalf("malformed response trusted: %s %+v %v", body, v, err)
			}
		})
	}
}

func TestTeamResponderPolicyHTTPFailuresNeverProveAbsenceOrRetry(t *testing.T) {
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, status := range []int{401, 403, 404, 409, 412, 500} {
			t.Run(fmt.Sprint(method, status), func(t *testing.T) {
				calls := 0
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":{"code":"not_found","message":"private-response"}}`)
				}))
				defer s.Close()
				c, _ := New(s.URL, "secret-token")
				var err error
				var v TeamResponderPolicy
				switch method {
				case "GET":
					v, err = c.GetTeamResponderPolicy(t.Context(), responderTeam, "default")
				case "POST":
					v, err = c.PublishTeamResponderPolicy(t.Context(), responderTeam, TeamResponderPolicyInput{Importance: "default", Strategy: "team_fanout"})
				case "DELETE":
					err = c.ResetTeamResponderPolicy(t.Context(), responderTeam, "default")
				}
				var api *APIError
				if !errors.As(err, &api) || api.StatusCode != status || v.Committed || calls != 1 || strings.Contains(err.Error(), "private-response") {
					t.Fatalf("HTTP error lost/retried: %+v %v calls=%d", v, err, calls)
				}
			})
		}
	}
}

func TestTeamResponderPolicyInvalidInputsAndCancellation(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, responderAbsent) }))
	defer s.Close()
	c, _ := New(s.URL, "fixture-token")
	for _, in := range []TeamResponderPolicyInput{{Importance: "", Strategy: "team_fanout"}, {Importance: "other", Strategy: "team_fanout"}, {Importance: "default", Strategy: "team_chain"}, {Importance: "default", Strategy: "team_fanout", ChainID: responderChain}, {Importance: "default", Strategy: "other"}} {
		if _, err := c.PublishTeamResponderPolicy(t.Context(), responderTeam, in); err == nil {
			t.Fatal("invalid input accepted", in)
		}
	}
	if _, err := c.GetTeamResponderPolicy(t.Context(), "invalid", "default"); err == nil {
		t.Fatal("invalid team accepted")
	}
	if err := c.ResetTeamResponderPolicy(t.Context(), responderTeam, ""); err == nil {
		t.Fatal("implicit default reset accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetTeamResponderPolicy(ctx, responderTeam, "default"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("invalid/cancelled input reached API", calls)
	}
}
