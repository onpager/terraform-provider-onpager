package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const defaultsRoleID = "11111111-1111-4111-8111-111111111111"

// Catches loss of the policy's own numeric ETag, extra catalog discovery and
// accidentally placing its revision in the replacement body.
func TestTeamRoleDefaultsDirectReadAndSavedCAS(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/rbac/team-defaults" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("policy must use authenticated direct endpoint only")
		}
		if calls == 1 {
			if r.Method != http.MethodGet || r.Header.Get("If-Match") != "" {
				t.Error("read mutated or sent a write revision")
			}
			w.Header().Set("ETag", `"7"`)
			fmt.Fprint(w, `{"members":[],"leads":[],"revision":7,"display":{"roles":[]}}`)
			return
		}
		var b map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&b)
		if r.Method != http.MethodPut || r.Header.Get("If-Match") != `"7"` || len(b) != 2 || string(b["members"]) != `["11111111-1111-4111-8111-111111111111"]` || string(b["leads"]) != "[]" {
			t.Errorf("replacement lost exact policy CAS/body: %s %s %v", r.Method, r.Header.Get("If-Match"), b)
		}
		w.Header().Set("ETag", `"8"`)
		fmt.Fprint(w, `{"members":["11111111-1111-4111-8111-111111111111"],"leads":[],"revision":8}`)
	}))
	defer s.Close()
	c, _ := New(s.URL, "fixture-token")
	v, err := c.GetTeamRoleDefaults(t.Context())
	if err != nil || v.Revision != 7 || v.Members == nil || v.Leads == nil || v.Committed || calls != 1 {
		t.Fatalf("read did not return complete policy: %#v %v calls=%d", v, err, calls)
	}
	v, err = c.ReplaceTeamRoleDefaults(t.Context(), v.Revision, TeamRoleDefaultsInput{Members: []string{defaultsRoleID}, Leads: []string{}})
	if err != nil || !v.Committed || v.Revision != 8 || len(v.Members) != 1 || calls != 2 {
		t.Fatalf("write not confirmed: %#v %v calls=%d", v, err, calls)
	}
}

// Catches accepting incomplete/null arrays, invalid IDs or a stale/foreign ETag
// as authoritative policy state. A committed malformed body retains only a
// separately validated own revision, never an unproven replacement revision.
func TestTeamRoleDefaultsRejectMalformedViewsAndKeepCommittedOwnRevision(t *testing.T) {
	for _, tc := range []struct {
		name, body, tag string
		revision        int64
	}{
		{"missing members", `{"leads":[],"revision":8}`, `"8"`, 8},
		{"null members", `{"members":null,"leads":[],"revision":8}`, `"8"`, 8},
		{"missing leads", `{"members":[],"revision":8}`, `"8"`, 8},
		{"null leads", `{"members":[],"leads":null,"revision":8}`, `"8"`, 8},
		{"wrong array", `{"members":"private-marker","leads":[],"revision":8}`, `"8"`, 8},
		{"invalid uuid", `{"members":["private-marker"],"leads":[],"revision":8}`, `"8"`, 8},
		{"uppercase uuid", `{"members":["AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"],"leads":[],"revision":8}`, `"8"`, 8},
		{"duplicate uuid", `{"members":["11111111-1111-4111-8111-111111111111","11111111-1111-4111-8111-111111111111"],"leads":[],"revision":8}`, `"8"`, 8},
		{"zero revision", `{"members":[],"leads":[],"revision":0}`, `"0"`, 0},
		{"missing revision", `{"members":[],"leads":[]}`, `"8"`, 0},
		{"null revision", `{"members":[],"leads":[],"revision":null}`, `"8"`, 0},
		{"wrong revision type", `{"members":[],"leads":[],"revision":"8"}`, `"8"`, 0},
		{"missing tag", `{"members":[],"leads":[],"revision":8}`, "", 0},
		{"weak tag", `{"members":[],"leads":[],"revision":8}`, `W/"8"`, 0},
		{"unquoted tag", `{"members":[],"leads":[],"revision":8}`, `8`, 0},
		{"tag mismatch", `{"members":[],"leads":[],"revision":8}`, `"9"`, 0},
		{"malformed json", `{"private-marker":`, `"8"`, 0},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			t.Run(tc.name+method, func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("ETag", tc.tag)
					fmt.Fprint(w, tc.body)
				}))
				defer s.Close()
				c, _ := New(s.URL, "fixture-token")
				var v TeamRoleDefaults
				var err error
				if method == http.MethodGet {
					v, err = c.GetTeamRoleDefaults(t.Context())
				} else {
					v, err = c.ReplaceTeamRoleDefaults(t.Context(), 7, TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}})
				}
				if err == nil || strings.Contains(err.Error(), "private-marker") || v.Committed != (method == http.MethodPut) || v.Revision != tc.revision {
					t.Fatalf("invalid policy accepted or unsafe receipt: %#v err=%v", v, err)
				}
			})
		}
	}
}

func TestTeamRoleDefaultsRejectForeignWriteContentAndRevision(t *testing.T) {
	for _, tc := range []struct {
		body string
		tag  string
		rev  int64
	}{
		{`{"members":["22222222-2222-4222-8222-222222222222"],"leads":[],"revision":8}`, `"8"`, 8},
		{`{"members":[],"leads":[],"revision":7}`, `"7"`, 0},
		{`{"members":[],"leads":[],"revision":9}`, `"9"`, 0},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", tc.tag)
			fmt.Fprint(w, tc.body)
		}))
		c, _ := New(s.URL, "fixture-token")
		v, err := c.ReplaceTeamRoleDefaults(t.Context(), 7, TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}})
		s.Close()
		if err == nil || !v.Committed || v.Revision != tc.rev {
			t.Fatalf("foreign receipt trusted: %#v %v", v, err)
		}
	}
}

func TestTeamRoleDefaultsInvalidInputNeverRequestsAndFailuresNeverReplay(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusPreconditionFailed)
		fmt.Fprint(w, `{"error":{"code":"revision_mismatch","message":"private-marker"}}`)
	}))
	defer s.Close()
	c, _ := New(s.URL, "fixture-token")
	for _, tc := range []struct {
		rev int64
		in  TeamRoleDefaultsInput
	}{
		{0, TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}}},
		{9223372036854775807, TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}}},
		{7, TeamRoleDefaultsInput{Leads: []string{}}},
		{7, TeamRoleDefaultsInput{Members: []string{}}},
		{7, TeamRoleDefaultsInput{Members: []string{"invalid"}, Leads: []string{}}},
		{7, TeamRoleDefaultsInput{Members: []string{}, Leads: []string{defaultsRoleID, defaultsRoleID}}},
	} {
		if _, err := c.ReplaceTeamRoleDefaults(t.Context(), tc.rev, tc.in); err == nil || calls != 0 {
			t.Fatalf("invalid policy sent: %v calls=%d", err, calls)
		}
	}
	v, err := c.ReplaceTeamRoleDefaults(t.Context(), 7, TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}})
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != 412 || v.Committed || calls != 1 || strings.Contains(err.Error(), "private-marker") {
		t.Fatalf("conflict lost or replayed: %#v %v calls=%d", v, err, calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = c.GetTeamRoleDefaults(ctx); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation lost: %v calls=%d", err, calls)
	}
}

func TestTeamRoleDefaultsBoundedResponseAndExactSuccessStatus(t *testing.T) {
	for _, status := range []int{200, 204, 201, 403, 404, 412, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("ETag", `"8"`)
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, strings.Repeat(" ", (4<<20)+1))
				} else {
					fmt.Fprint(w, `{"members":[],"leads":[],"revision":8}`)
				}
			}))
			defer s.Close()
			c, _ := New(s.URL, "fixture-token")
			v, err := c.ReplaceTeamRoleDefaults(t.Context(), 7, TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}})
			wantRevision := int64(0)
			if status == 201 {
				wantRevision = 8
			}
			if err == nil || calls != 1 || v.Committed != (status >= 200 && status < 300) || v.Revision != wantRevision {
				t.Fatalf("invalid status/response accepted: %#v %v calls=%d", v, err, calls)
			}
		})
	}
}
