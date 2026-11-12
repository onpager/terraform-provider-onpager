package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestDataSourceExactSelectorsReadTheSelectedIdentity(t *testing.T) {
	for _, kind := range []string{"user", "team", "service", "role", "group"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			name := " Ops \"\\東京 "
			route, field, value := "/v1/teams", "name", name
			switch kind {
			case "user":
				route, field, value = "/v1/users", "local_login", " Operator "
			case "service":
				route = "/v1/services"
			case "role":
				route, field, value = "/v1/rbac/roles", "key", "tenant_observer"
			case "group":
				route = "/v1/groups"
			}
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.Header.Get("If-Match") != "" {
					t.Error("data reads must be GET without mutation CAS")
				}
				if calls == 1 {
					if r.URL.Path != route || r.URL.Query().Get("limit") != "2" || r.URL.Query().Has("q") {
						t.Error("wrong exact catalog request", r.URL)
					}
					var filters []struct{ Field, Op, Value string }
					if err := json.Unmarshal([]byte(r.URL.Query().Get("filter")), &filters); err != nil {
						t.Fatal(err)
					}
					if len(filters) < 1 || filters[0].Field != field || filters[0].Op != "equal" || filters[0].Value != value {
						t.Error("selector was rewritten or not filtered server-side", filters)
					}
					if kind == "service" && (len(filters) != 2 || filters[1].Field != "team_id" || filters[1].Value != serviceID) {
						t.Error("missing expected owner filter")
					}
					fmt.Fprintf(w, `{"items":[{"id":%q}],"has_more":false}`, teamID)
					return
				}
				if calls != 2 || r.URL.Path != route+"/"+teamID || r.URL.RawQuery != "" {
					t.Error("selected identity must use direct detail GET")
				}
				switch kind {
				case "user":
					fmt.Fprintf(w, `{"id":%q,"display_name":"Operator","active":false,"email_normalized":"operator@example.test","revision":1,"created_at":"2026-01-01T00:00:00Z"}`, teamID)
				case "team":
					fmt.Fprintf(w, `{"id":%q,"name":%q,"active":false}`, teamID, name)
				case "service":
					fmt.Fprintf(w, `{"id":%q,"team_id":%q,"name":%q,"active":false}`, teamID, serviceID, name)
				case "role":
					w.Header().Set("ETag", `"1"`)
					fmt.Fprintf(w, `{"id":%q,"key":"tenant_observer","name":"Observer","description":"","scope_type":"tenant","permission_keys":[],"builtin":true,"revision":1}`, teamID)
				case "group":
					fmt.Fprintf(w, `{"id":%q,"key":"operators","name":%q,"description":"","system":true,"revision":1}`, teamID, name)
				}
			})
			var got string
			var err error
			switch kind {
			case "user":
				var v client.User
				v, err = c.LookupUser(t.Context(), field, value)
				got = v.ID
				if v.Active {
					t.Error("inactive user lost")
				}
			case "team":
				var v client.Team
				v, err = c.LookupTeam(t.Context(), field, value)
				got = v.ID
			case "service":
				var v client.Service
				v, err = c.LookupService(t.Context(), field, value, serviceID)
				got = v.ID
			case "role":
				var v client.RBACRole
				v, err = c.LookupRBACRole(t.Context(), field, value)
				got = v.ID
				if !v.Builtin {
					t.Error("builtin roles must be readable")
				}
			case "group":
				var v client.Group
				v, err = c.LookupGroup(t.Context(), field, value)
				got = v.ID
			}
			if err != nil || got != teamID || calls != 2 {
				t.Fatalf("lookup failed: %s %v calls=%d", got, err, calls)
			}
		})
	}
}

func TestDataSourceSelectionRejectsAbsentAmbiguousAndMalformedPages(t *testing.T) {
	for _, body := range []string{
		`{"items":[],"has_more":false}`,
		`{"items":[{"id":"` + teamID + `"},{"id":"` + serviceID + `"}],"has_more":false}`,
		`{"items":[{"id":"` + teamID + `"}],"has_more":true}`,
		`{"items":[{"id":"` + teamID + `"}]}`,
		`{"items":null,"has_more":false}`,
		`{"has_more":false}`,
		`{"items":[{}],"has_more":false}`,
		`{"items":[{"id":"00000000-0000-0000-0000-000000000000"}],"has_more":false}`,
	} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) })
			if _, err := c.LookupTeam(t.Context(), "name", "Ops"); err == nil || calls != 1 {
				t.Fatalf("untrustworthy selection accepted or scanned: %v calls=%d", err, calls)
			}
		})
	}
}

func TestDataSourceDirectReadChecksIdentityOwnerAndRequiredMetadata(t *testing.T) {
	for _, tc := range []struct{ name, body, owner string }{
		{"wrong ID", `{"id":"` + serviceID + `","team_id":"` + serviceID + `","name":"S","active":false}`, serviceID},
		{"wrong owner", `{"id":"` + teamID + `","team_id":"` + serviceID + `","name":"S","active":false}`, teamID},
		{"missing active", `{"id":"` + teamID + `","team_id":"` + serviceID + `","name":"S"}`, serviceID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "" || r.URL.Path != "/v1/services/"+teamID {
					t.Error("UUID must not scan catalogs")
				}
				fmt.Fprint(w, tc.body)
			})
			if _, err := c.LookupService(t.Context(), "id", teamID, tc.owner); err == nil {
				t.Fatal("invalid direct result accepted")
			}
		})
	}
}

func TestDataSourceInvalidSelectorsNeverReachNetwork(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) })
	for _, tc := range []struct{ field, value string }{
		{"id", "not-a-uuid"}, {"q", "Ops"}, {"name", " "}, {"name", strings.Repeat("я", 2049)}, {"name", strings.Repeat("\\", 4096)},
	} {
		if _, err := c.LookupTeam(t.Context(), tc.field, tc.value); err == nil {
			t.Error("invalid selector accepted", tc.field)
		}
	}
	if _, err := c.LookupUser(t.Context(), "login", "operator"); err == nil {
		t.Error("generic login must not imply local/OIDC identity")
	}
	if _, err := c.LookupService(t.Context(), "name", "S", "invalid"); err == nil {
		t.Error("invalid expected owner accepted")
	}
	if calls != 0 {
		t.Errorf("invalid config reached HTTP %d times", calls)
	}
}

func TestDataSourceReadErrorsAndContextRemainActionable(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":"forbidden","message":"secret-body"}}`)
	})
	_, err := c.LookupUser(t.Context(), "email", "operator@example.test")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 || strings.Contains(err.Error(), "secret-body") {
		t.Fatal("read authority was hidden", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = c.LookupUser(ctx, "id", teamID); !errors.Is(err, context.Canceled) {
		t.Fatal("context cancellation lost", err)
	}
}

func TestDataSourceDeliveryChannelReadsOnlyKnownMetadata(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/delivery/channels/"+teamID || r.URL.RawQuery != "" {
					t.Error("channel lookup must not discover targets")
				}
				reason, caps := "", `["conversation"]`
				if unavailable {
					reason, caps = "provider_unavailable", `[]`
				}
				fmt.Fprintf(w, `{"id":%q,"display_name":"Chat","kind":"slack","contract_version":"1","enabled":false,"capabilities":%s,"config":{},"unavailable_reason":%q}`, teamID, caps, reason)
			})
			got, err := c.GetDeliveryChannel(t.Context(), teamID)
			if err != nil || got.ID != teamID || got.Enabled || (unavailable && got.UnavailableReason != "provider_unavailable") {
				t.Fatal(got, err)
			}
		})
	}
}
