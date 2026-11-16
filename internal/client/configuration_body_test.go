package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Header success commits a mutation even if cancellation interrupts reading its
// receipt. The caller must get context.Canceled without losing that outcome.
func TestConfigurationSettingsBodyCancellationRetainsCommit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutates bool
		call    func(*Client, context.Context) (bool, error)
	}{
		{"routing read", false, func(c *Client, ctx context.Context) (bool, error) {
			v, e := c.GetRoutingSettings(ctx)
			return v.Committed, e
		}},
		{"routing write", true, func(c *Client, ctx context.Context) (bool, error) {
			v, e := c.UpdateRoutingSettings(ctx, RoutingSettings{})
			return v.Committed, e
		}},
		{"responder read", false, func(c *Client, ctx context.Context) (bool, error) {
			v, e := c.GetTeamResponderPolicy(ctx, responderGuardTeamID, "default")
			return v.Committed, e
		}},
		{"responder write", true, func(c *Client, ctx context.Context) (bool, error) {
			v, e := c.PublishTeamResponderPolicy(ctx, responderGuardTeamID, TeamResponderPolicyInput{Importance: "default", Strategy: "team_fanout"})
			return v.Committed, e
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c, _ := New("http://fixture.invalid", "fixture-token")
			c.http.Transport = responderGuardRoundTripFunc(func(q *http.Request) (*http.Response, error) {
				status := 200
				if q.Method == "POST" {
					status = 201
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(responderGuardReadFunc(func([]byte) (int, error) { cancel(); return 0, errors.New("private-body-marker") })), Request: q}, nil
			})
			committed, err := tc.call(c, ctx)
			if !errors.Is(err, context.Canceled) || committed != tc.mutates || strings.Contains(err.Error(), "private-body-marker") {
				t.Fatal("body cancellation/commit contract violated", committed, err)
			}
		})
	}
}
