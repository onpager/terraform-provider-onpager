package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const permissionFixture = `{"key":"future.family.action","name":"Future permission","description":"","section":"future.family","scopes":["tenant","team"],"actors":["user","service_account"],"target":"team","sensitive":false,"custom_grantable":false}`

// Catches catalog reads being converted to paginated/selector or mutation calls,
// and false flags being lost during typed metadata decoding.
func TestRBACPermissionsCompleteMetadataRead(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/control/v1/rbac/permissions" || r.URL.RawQuery != "" || r.Header.Get("If-Match") != "" || r.ContentLength != 0 {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer catalog-fixture-secret" || r.Header.Get("Onpager-Timezone") != "UTC" {
			t.Error("catalog read lost configured bearer/timezone")
		}
		fmt.Fprintf(w, "[%s]", permissionFixture)
	}))
	defer s.Close()
	c, err := client.New(s.URL+"/control", "catalog-fixture-secret", client.WithTimezone("UTC"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ListRBACPermissions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(got) != 1 {
		t.Fatalf("catalog calls=%d rows=%d", calls, len(got))
	}
	p := got[0]
	if p.Key != "future.family.action" || p.Name != "Future permission" || p.Description != "" || p.Section != "future.family" || p.Target != "team" || p.Sensitive || p.CustomGrantable || strings.Join(p.Scopes, ",") != "tenant,team" || strings.Join(p.Actors, ",") != "user,service_account" {
		t.Fatalf("metadata changed: %#v", p)
	}
}

// Each required value must be present and non-null, including false booleans.
func TestRBACPermissionsRejectIncompleteOrAmbiguousMetadata(t *testing.T) {
	fields := []string{"key", "name", "description", "section", "scopes", "actors", "target", "sensitive", "custom_grantable"}
	for _, field := range fields {
		for _, mode := range []string{"missing", "null"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				var record map[string]json.RawMessage
				if err := json.Unmarshal([]byte(permissionFixture), &record); err != nil {
					t.Fatal(err)
				}
				if mode == "missing" {
					delete(record, field)
				} else {
					record[field] = json.RawMessage("null")
				}
				body, err := json.Marshal([]map[string]json.RawMessage{record})
				if err != nil {
					t.Fatal(err)
				}
				c := iamClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
				if _, err := c.ListRBACPermissions(t.Context()); err == nil {
					t.Fatal("incomplete catalog metadata was accepted")
				}
			})
		}
	}
	for name, body := range map[string]string{
		"null catalog": "null", "object catalog": `{"items":[]}`, "null record": `[null]`,
		"duplicate permission": "[" + permissionFixture + "," + permissionFixture + "]",
		"invalid JSON":         "[", "empty key": "[" + strings.Replace(permissionFixture, `"future.family.action"`, `""`, 1) + "]",
		"invalid scope":   "[" + strings.Replace(permissionFixture, `"tenant","team"`, `"project"`, 1) + "]",
		"duplicate scope": "[" + strings.Replace(permissionFixture, `"tenant","team"`, `"team","team"`, 1) + "]",
		"invalid actor":   "[" + strings.Replace(permissionFixture, `"user","service_account"`, `"robot"`, 1) + "]",
		"null actor":      "[" + strings.Replace(permissionFixture, `"user","service_account"`, `null`, 1) + "]",
		"duplicate actor": "[" + strings.Replace(permissionFixture, `"user","service_account"`, `"user","user"`, 1) + "]",
		"invalid target":  "[" + strings.Replace(permissionFixture, `"target":"team"`, `"target":"project"`, 1) + "]",
		"string boolean":  "[" + strings.Replace(permissionFixture, `"sensitive":false`, `"sensitive":"false"`, 1) + "]",
		"oversized":       `[` + strings.Repeat(" ", 4<<20) + `]`,
	} {
		t.Run(name, func(t *testing.T) {
			c := iamClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.ListRBACPermissions(t.Context()); err == nil {
				t.Fatal("malformed catalog was accepted")
			}
		})
	}
}

func TestRBACPermissionsEmptyAndFutureKeys(t *testing.T) {
	for _, body := range []string{"[]", "[" + strings.Replace(permissionFixture, "future.family.action", strings.Repeat("future.", 800), 1) + "]"} {
		c := iamClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
		got, err := c.ListRBACPermissions(t.Context())
		if err != nil || got == nil {
			t.Fatalf("valid catalog failed: rows=%#v err=%v", got, err)
		}
	}
}

func TestRBACPermissionsSafeFailuresAndCancellation(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := iamClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"forbidden","message":"iam-test-secret"},"credential":"iam-test-secret"}`)
			})
			_, err := c.ListRBACPermissions(t.Context())
			var apiErr *client.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status || calls != 1 || strings.Contains(err.Error(), "iam-test-secret") {
				t.Fatalf("unsafe/retried catalog failure: %v calls=%d", err, calls)
			}
		})
	}
	c := iamClient(t, func(http.ResponseWriter, *http.Request) { t.Error("cancelled read reached server") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListRBACPermissions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
