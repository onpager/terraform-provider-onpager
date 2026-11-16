package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestRoutingSettingsClientNullableRouteAndExactUnversionedRequests(t *testing.T) {
	for _, target := range []*client.DefaultRoute{nil, {TargetKind: "team", TargetID: teamID}, {TargetKind: "service", TargetID: serviceID}} {
		calls := 0
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.RequestURI() != "/v1/settings/routing" || r.Header.Get("If-Match") != "" {
				t.Error("unexpected query/revision/route")
			}
			if calls == 1 {
				if r.Method != "GET" {
					t.Error("must read settings")
				}
				fmt.Fprint(w, `{"default_route":null}`)
				return
			}
			if r.Method != "PATCH" {
				t.Error("must PATCH full nullable route")
			}
			var fields map[string]json.RawMessage
			data, _ := io.ReadAll(r.Body)
			if json.Unmarshal(data, &fields) != nil || len(fields) != 1 || fields["default_route"] == nil {
				t.Error("missing explicit nullable route")
			}
			if target == nil && string(fields["default_route"]) != "null" {
				t.Error("clearing must send null")
			}
			_, _ = w.Write(data)
		})
		if v, e := c.GetRoutingSettings(t.Context()); e != nil || v.DefaultRoute != nil {
			t.Fatal("null route read failed", e)
		}
		v, e := c.UpdateRoutingSettings(t.Context(), client.RoutingSettings{DefaultRoute: target})
		if e != nil || !v.Committed || (v.DefaultRoute == nil) != (target == nil) || target != nil && *v.DefaultRoute != *target || calls != 2 {
			t.Fatal("full route mutation failed", e)
		}
	}
}

func TestRoutingSettingsClientRejectsMalformedSettings(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"default_route":{}}`, `{"default_route":{"target_kind":"user","target_id":"` + teamID + `"}}`, `{"default_route":{"target_kind":"team","target_id":null}}`, `{"default_route":{"target_kind":"team","target_id":"00000000-0000-0000-0000-000000000000"}}`} {
		c := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
		if _, e := c.GetRoutingSettings(t.Context()); e == nil {
			t.Fatal("malformed settings accepted")
		}
	}
}

func TestRoutingSettingsClientCommittedMalformedReceiptAndSanitizedErrors(t *testing.T) {
	for _, status := range []int{200, 204, 401, 403, 404, 409, 412, 500} {
		calls := 0
		c := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.WriteHeader(status)
			if status != 204 {
				fmt.Fprint(w, `{"error":{"code":"forbidden","message":"private-token-secret"}}`)
			}
		})
		v, e := c.UpdateRoutingSettings(t.Context(), client.RoutingSettings{DefaultRoute: &client.DefaultRoute{TargetKind: "team", TargetID: teamID}})
		if e == nil || v.Committed != (status < 300) || calls != 1 || strings.Contains(e.Error(), "private-token-secret") {
			t.Fatal("committed/error/retry contract violated")
		}
	}
}

func TestRoutingSettingsClientRejectsInvalidDesiredTargetBeforeRequest(t *testing.T) {
	c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid target must not cause I/O") })
	for _, route := range []client.DefaultRoute{{TargetKind: "user", TargetID: teamID}, {TargetKind: "team", TargetID: "invalid"}} {
		if _, e := c.UpdateRoutingSettings(t.Context(), client.RoutingSettings{DefaultRoute: &route}); e == nil {
			t.Fatal("invalid target accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := c.GetRoutingSettings(ctx); e != context.Canceled {
		t.Fatal("context cancellation lost", e)
	}
}
