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

const iamID = "11111111-1111-4111-8111-111111111111"
const iamRecipient = "22222222-2222-4222-8222-222222222222"

func iamClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := client.New(s.URL, "iam-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIAMServiceAccountDirectLifecycle(t *testing.T) {
	step := 0
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if r.Header.Get("If-Match") != "" || r.URL.RawQuery != "" {
			t.Error("account API is unversioned and direct")
		}
		if step == 1 || step == 3 {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if string(body["owner_team_id"]) != "null" {
				t.Error("explicit null owner was omitted")
			}
		}
		if step >= 4 {
			want := "/deactivate"
			if step == 5 {
				want = "/reactivate"
			}
			if r.Method != "POST" || r.URL.Path != "/v1/service-accounts/"+iamID+want || r.ContentLength != 0 {
				t.Error("unexpected state command")
			}
			w.WriteHeader(204)
			return
		}
		if step == 2 && (r.Method != "GET" || r.URL.Path != "/v1/service-accounts/"+iamID) {
			t.Error("account refresh must read exact ID")
		}
		if step == 3 && r.Method != "PUT" {
			t.Error("metadata update must use PUT")
		}
		fmt.Fprintf(w, `{"id":%q,"display_name":"Automation","owner_team_id":null,"active":true}`, iamID)
	})
	if _, err := c.CreateServiceAccount(t.Context(), client.ServiceAccountInput{DisplayName: "Automation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetServiceAccount(t.Context(), iamID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateServiceAccount(t.Context(), iamID, client.ServiceAccountInput{DisplayName: "Automation"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeactivateServiceAccount(t.Context(), iamID); err != nil {
		t.Fatal(err)
	}
	if err := c.ReactivateServiceAccount(t.Context(), iamID); err != nil {
		t.Fatal(err)
	}
	if step != 5 {
		t.Fatal("unexpected requests")
	}
}

func TestIAMRoleOwnETagAndImmutableUpdatePayload(t *testing.T) {
	step := 0
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if step >= 3 && r.Header.Get("If-Match") != `"7"` {
			t.Error("role must send its own strong numeric ETag")
		}
		if step == 3 {
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["key"]; ok {
				t.Error("immutable key sent in PUT")
			}
			if _, ok := body["scope_type"]; ok {
				t.Error("immutable scope sent in PUT")
			}
			if string(body["description"]) != `""` || string(body["permission_keys"]) != `[]` {
				t.Error("empty values must replace existing content")
			}
		}
		if step == 4 {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("ETag", `"7"`)
		fmt.Fprintf(w, `{"id":%q,"key":"automation","name":"Automation","description":"","scope_type":"tenant","permission_keys":[],"builtin":false,"revision":7}`, iamID)
	})
	in := client.RBACRoleInput{Key: "automation", Name: "Automation", ScopeType: "tenant", PermissionKeys: []string{}}
	if _, err := c.CreateRBACRole(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRBACRole(t.Context(), iamID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateRBACRole(t.Context(), iamID, 7, client.RBACRoleUpdate{Name: "Automation", PermissionKeys: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRBACRole(t.Context(), iamID, 7); err != nil {
		t.Fatal(err)
	}
}

func TestIAMRoleRejectsMissingOrMismatchedETagAndBuiltin(t *testing.T) {
	for _, tag := range []string{"", `W/"7"`, `"8"`, `"7"`} {
		t.Run(tag, func(t *testing.T) {
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", tag)
				fmt.Fprintf(w, `{"id":%q,"key":"automation","name":"Automation","description":"","scope_type":"tenant","permission_keys":[],"builtin":%t,"revision":7}`, iamID, tag == `"7"`)
			})
			if _, err := c.GetRBACRole(t.Context(), iamID); err == nil {
				t.Fatal("unsafe role response accepted")
			}
		})
	}
}

func TestIAMAssignmentExactLifecycleAndResponseIdentity(t *testing.T) {
	step := 0
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if r.Header.Get("If-Match") != "" || r.URL.RawQuery != "" {
			t.Error("assignment is immutable and unversioned")
		}
		if step == 1 {
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			if string(body["target_team_id"]) != "null" {
				t.Error("tenant target must be null")
			}
		} else if r.URL.Path != "/v1/rbac/assignments/"+iamID {
			t.Error("must use direct immutable assignment identity")
		}
		if step == 3 {
			w.WriteHeader(204)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"role_id":%q,"recipient":{"kind":"service_account","id":%q},"target_team_id":null,"protected":false}`, iamID, iamID, iamRecipient)
	})
	in := client.RoleAssignmentInput{RoleID: iamID, Recipient: client.RoleRecipient{Kind: "service_account", ID: iamRecipient}}
	if _, err := c.CreateRoleAssignment(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRoleAssignment(t.Context(), iamID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRoleAssignment(t.Context(), iamID); err != nil {
		t.Fatal(err)
	}
}

func TestIAMMalformedReadAndErrorsAreSafe(t *testing.T) {
	for _, body := range []string{
		`{"id":"` + iamID + `","display_name":"Automation","active":true}`,
		`{"id":"` + iamID + `","display_name":"Automation","owner_team_id":null}`,
		`{"id":"` + iamRecipient + `","display_name":"Automation","owner_team_id":null,"active":true}`,
		`{"id":"` + iamID + `","display_name":"Automation","owner_team_id":"invalid","active":true}`,
		`{"id":"` + iamID + `","display_name":"Automation","owner_team_id":null,"active":null}`,
	} {
		c := iamClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := c.GetServiceAccount(t.Context(), iamID); err == nil {
			t.Fatal("incomplete or wrong identity accepted")
		}
	}
	for _, status := range []int{403, 409, 412} {
		calls := 0
		c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"code":"forbidden","message":"iam-test-secret"}}`)
		})
		if _, err := c.GetRoleAssignment(t.Context(), iamID); err == nil || strings.Contains(err.Error(), "iam-test-secret") {
			t.Fatal("failure was swallowed or secret exposed")
		}
		if calls != 1 {
			t.Fatal("request retried")
		}
	}
}

func TestIAMAssignmentDeleteDistinguishesDeniedAndAbsent(t *testing.T) {
	for _, status := range []int{403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			})
			err := c.DeleteRoleAssignment(t.Context(), iamID)
			if (status == 403) != (err != nil) {
				t.Fatal("delete must retain denied writes and accept confirmed absence")
			}
		})
	}
}
