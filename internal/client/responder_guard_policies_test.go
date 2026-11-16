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

const responderGuardTeamID = "11111111-1111-4111-8111-111111111111"

// A policy publication is an exact team write, without fake CAS, collection
// discovery, a follow-up read, or a guessed default reset payload.
func TestTeamResponderGuardPolicyDirectLifecycle(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/teams/"+responderGuardTeamID+"/responder-guard-policies" || r.URL.RawQuery != "" || r.Header.Get("If-Match") != "" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("policy did not use its direct authenticated unversioned team endpoint")
		}
		switch calls {
		case 1:
			if r.Method != "GET" {
				t.Error("read mutated")
			}
			fmt.Fprint(w, `{"max_open_requests_per_subject":357,"revision":0,"is_default":true}`)
		case 2, 3:
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			want := "1"
			if calls == 3 {
				want = "1000"
			}
			if r.Method != "POST" || len(body) != 1 || string(body["max_open_requests_per_subject"]) != want {
				t.Errorf("invalid publication: %s %v", r.Method, body)
			}
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"max_open_requests_per_subject":%s,"revision":%d,"is_default":false}`, want, calls)
		case 4, 5:
			if r.Method != "DELETE" || r.ContentLength != 0 {
				t.Error("reset must DELETE without a body")
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected discovery, read or retry")
		}
	}))
	defer s.Close()
	c, _ := New(s.URL, "fixture-token")
	v, err := c.GetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID)
	if err != nil || !v.IsDefault || v.Revision != 0 || v.MaxOpenRequestsPerSubject != 357 || v.Committed {
		t.Fatalf("default not recognized: %+v %v", v, err)
	}
	for i, limit := range []int64{1, 1000} {
		v, err = c.PublishTeamResponderGuardPolicy(t.Context(), responderGuardTeamID, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: limit})
		if err != nil || v.IsDefault || v.Revision != int64(i+2) || v.MaxOpenRequestsPerSubject != limit || !v.Committed {
			t.Fatalf("publication receipt lost: %+v %v", v, err)
		}
	}
	for range 2 {
		if err := c.ResetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 5 {
		t.Fatal("lifecycle made extra requests", calls)
	}
}

func TestTeamResponderGuardPolicyMalformedViewsAndCommittedReceipts(t *testing.T) {
	for _, body := range []string{
		`{"revision":8,"is_default":false}`,
		`{"max_open_requests_per_subject":null,"revision":8,"is_default":false}`,
		`{"max_open_requests_per_subject":4,"is_default":false}`,
		`{"max_open_requests_per_subject":4,"revision":null,"is_default":false}`,
		`{"max_open_requests_per_subject":4,"revision":8}`,
		`{"max_open_requests_per_subject":4,"revision":8,"is_default":null}`,
		`{"max_open_requests_per_subject":0,"revision":8,"is_default":false}`,
		`{"max_open_requests_per_subject":1001,"revision":8,"is_default":false}`,
		`{"max_open_requests_per_subject":4,"revision":0,"is_default":false}`,
		`{"max_open_requests_per_subject":4,"revision":8,"is_default":true}`,
		`{"max_open_requests_per_subject":4,"revision":-1,"is_default":false}`,
		`{"max_open_requests_per_subject":"private-marker","revision":8,"is_default":false}`,
		`null`, `{`, `[]`,
	} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(method+body, func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if method == "POST" {
						w.WriteHeader(201)
					}
					fmt.Fprint(w, body)
				}))
				defer s.Close()
				c, _ := New(s.URL, "fixture-token")
				var v TeamResponderGuardPolicy
				var err error
				if method == "GET" {
					v, err = c.GetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID)
				} else {
					v, err = c.PublishTeamResponderGuardPolicy(t.Context(), responderGuardTeamID, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: 4})
				}
				if err == nil || strings.Contains(err.Error(), "private-marker") || v.Committed != (method == "POST") || v.Revision != 0 {
					t.Fatalf("malformed response trusted: %+v %v", v, err)
				}
			})
		}
	}
	for _, body := range []string{`{"max_open_requests_per_subject":5,"revision":8,"is_default":false}`, `{"max_open_requests_per_subject":4,"revision":0,"is_default":true}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); fmt.Fprint(w, body) }))
		c, _ := New(s.URL, "fixture-token")
		v, err := c.PublishTeamResponderGuardPolicy(t.Context(), responderGuardTeamID, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: 4})
		s.Close()
		if err == nil || !v.Committed || v.Revision != 0 {
			t.Fatalf("foreign publication trusted: %+v %v", v, err)
		}
	}
}

func TestTeamResponderGuardPolicyInvalidInputsAndHTTPErrors(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":"permission_denied","message":"private-marker"}}`)
	}))
	defer s.Close()
	c, _ := New(s.URL, "fixture-token")
	for _, id := range []string{"", "invalid", "00000000-0000-0000-0000-000000000000", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", responderGuardTeamID + "/extra"} {
		if _, err := c.GetTeamResponderGuardPolicy(t.Context(), id); err == nil {
			t.Error("invalid read owner accepted")
		}
		if _, err := c.PublishTeamResponderGuardPolicy(t.Context(), id, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: 1}); err == nil {
			t.Error("invalid publish owner accepted")
		}
		if err := c.ResetTeamResponderGuardPolicy(t.Context(), id); err == nil {
			t.Error("invalid reset owner accepted")
		}
	}
	for _, limit := range []int64{-1, 0, 1001} {
		if _, err := c.PublishTeamResponderGuardPolicy(t.Context(), responderGuardTeamID, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: limit}); err == nil {
			t.Error("invalid limit accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid inputs reached HTTP", calls)
	}
	for _, operation := range []string{"read", "publish", "reset"} {
		var err error
		if operation == "read" {
			_, err = c.GetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID)
		} else if operation == "publish" {
			var v TeamResponderGuardPolicy
			v, err = c.PublishTeamResponderGuardPolicy(t.Context(), responderGuardTeamID, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: 4})
			if v.Committed {
				t.Error("denied write marked committed")
			}
		} else {
			err = c.ResetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID)
		}
		var api *APIError
		if !errors.As(err, &api) || api.StatusCode != 403 || api.Code != "permission_denied" || strings.Contains(err.Error(), "private-marker") {
			t.Fatalf("HTTP failure lost or leaked: %v", err)
		}
	}
	if calls != 3 {
		t.Fatal("failed request was retried", calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetTeamResponderGuardPolicy(ctx, responderGuardTeamID); !errors.Is(err, context.Canceled) || calls != 3 {
		t.Fatal("context cancellation lost", err, calls)
	}
}

func TestTeamResponderGuardPolicyBoundsStatusAndTransport(t *testing.T) {
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, tc := range []struct {
			name   string
			status int
			body   string
		}{
			{"oversized", 200, strings.Repeat("x", (4<<20)+1)},
			{"wrong success", 202, `{"max_open_requests_per_subject":4,"revision":8,"is_default":false}`},
			{"not found", 404, `{"error":{"code":"not_found"}}`},
		} {
			t.Run(method+tc.name, func(t *testing.T) {
				calls := 0
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				}))
				defer s.Close()
				c, _ := New(s.URL, "fixture-token")
				var err error
				if method == "GET" {
					_, err = c.GetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID)
				} else if method == "POST" {
					var v TeamResponderGuardPolicy
					v, err = c.PublishTeamResponderGuardPolicy(t.Context(), responderGuardTeamID, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: 4})
					if v.Committed != (tc.status < 300) {
						t.Error("lost committed outcome")
					}
				} else {
					err = c.ResetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID)
				}
				if err == nil || calls != 1 {
					t.Fatal("invalid status/body accepted or retried", err, calls)
				}
			})
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s.Close()
	c, _ := New(s.URL, "fixture-token")
	_, err := c.GetTeamResponderGuardPolicy(t.Context(), responderGuardTeamID)
	if err == nil || strings.Contains(err.Error(), s.URL) || strings.Contains(err.Error(), "fixture-token") {
		t.Fatal("transport details leaked", err)
	}
}

type responderGuardRoundTripFunc func(*http.Request) (*http.Response, error)

func (f responderGuardRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type responderGuardReadFunc func([]byte) (int, error)

func (f responderGuardReadFunc) Read(p []byte) (int, error) { return f(p) }

// A body-read failure after successful response headers must retain the known
// committed outcome, preserve cancellation and never expose transport details.
func TestTeamResponderGuardPolicyBodyReadFailurePreservesCancellationAndCommit(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(fmt.Sprint(canceled, method), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				c, _ := New("http://fixture.invalid", "fixture-token")
				c.http.Transport = responderGuardRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					status := 200
					if method == "POST" {
						status = 201
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(responderGuardReadFunc(func([]byte) (int, error) {
						if canceled {
							cancel()
						}
						return 0, errors.New("private-marker")
					})), Request: r}, nil
				})
				var v TeamResponderGuardPolicy
				var err error
				if method == "GET" {
					v, err = c.GetTeamResponderGuardPolicy(ctx, responderGuardTeamID)
				} else {
					v, err = c.PublishTeamResponderGuardPolicy(ctx, responderGuardTeamID, TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: 4})
				}
				if err == nil || strings.Contains(err.Error(), "private-marker") || errors.Is(err, context.Canceled) != canceled || v.Committed != (method == "POST") || v.Revision != 0 {
					t.Fatalf("body failure lost cancellation/commit or leaked details: %+v %v", v, err)
				}
			})
		}
	}
}
