package client_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const localUserJSON = `{"id":"11111111-1111-4111-8111-111111111111","display_name":"Example user","email_normalized":"user@example.test","active":true,"revision":1,"created_at":"2026-11-16T00:00:00Z","display":{"local_login":"example","auth_methods":["local_password"],"protected":false,"can_update":false,"can_deactivate":false,"can_reactivate":false,"can_reset_password":false}}`

func TestLocalUserClientLifecycle(t *testing.T) {
	for _, policy := range []*bool{nil, new(false), new(true)} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("If-Match") != "" || r.URL.RawQuery != "" {
					t.Error("users must use unversioned direct operations")
				}
				switch calls {
				case 1:
					if r.Method != "POST" || r.URL.Path != "/v1/users" {
						t.Error("wrong create route")
					}
					var in map[string]any
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Fatal(err)
					}
					if in["password"] != "  initial-password  " || in["login"] != "Example" || in["display_name"] != "Example user" {
						t.Error("creation input changed")
					}
					value, present := in["password_change_required"]
					if present != (policy != nil) || present && value != *policy {
						t.Error("creation policy omission/false lost")
					}
					w.WriteHeader(201)
					fmt.Fprint(w, strings.Split(localUserJSON, `,"display":`)[0]+"}")
				case 2:
					if r.Method != "GET" || r.URL.Path != "/v1/users/"+teamID {
						t.Error("wrong direct GET")
					}
					fmt.Fprint(w, localUserJSON)
				case 3:
					if r.Method != "PATCH" || r.URL.Path != "/v1/users/"+teamID {
						t.Error("wrong sparse PATCH")
					}
					var in map[string]any
					json.NewDecoder(r.Body).Decode(&in)
					if len(in) != 1 || in["email"] != nil {
						t.Error("email clear must send explicit null without other fields")
					}
					fmt.Fprintf(w, `{"id":%q}`, teamID)
				case 4, 5:
					suffix := "/deactivate"
					if calls == 5 {
						suffix = "/reactivate"
					}
					body, _ := io.ReadAll(r.Body)
					if r.Method != "POST" || r.URL.Path != "/v1/users/"+teamID+suffix || len(body) != 0 {
						t.Error("wrong lifecycle command")
					}
					w.WriteHeader(204)
				default:
					t.Error("unexpected retry, discovery or password reset")
				}
			})
			v, err := c.CreateLocalUser(t.Context(), client.LocalUserInput{DisplayName: "Example user", Login: "Example", Password: "  initial-password  ", PasswordChangeRequired: policy})
			if err != nil || v.ID != teamID || !v.Committed {
				t.Fatalf("create: %v", err)
			}
			v, err = c.GetManagedUser(t.Context(), teamID)
			if err != nil || v.LocalLogin != "example" || v.Protected || !v.Active {
				t.Fatalf("get: %+v %v", v, err)
			}
			receipt, err := c.UpdateLocalUser(t.Context(), teamID, client.LocalUserPatch{EmailSet: true})
			if err != nil || !receipt.Committed {
				t.Fatalf("patch: %v", err)
			}
			if err = c.DeactivateUser(t.Context(), teamID); err != nil {
				t.Fatal(err)
			}
			if err = c.ReactivateUser(t.Context(), teamID); err != nil {
				t.Fatal(err)
			}
			if calls != 5 {
				t.Fatal("wrong operation count")
			}
		})
	}
}

func TestLocalUserClientRejectsMalformedReads(t *testing.T) {
	for name, body := range map[string]string{
		"missing active":    strings.Replace(localUserJSON, `"active":true,`, "", 1),
		"missing display":   strings.Split(localUserJSON, `,"display":`)[0] + "}",
		"missing protected": strings.Replace(localUserJSON, `"protected":false,`, "", 1),
		"missing login":     strings.Replace(localUserJSON, `"local_login":"example",`, "", 1),
		"null display":      strings.Split(localUserJSON, `,"display":`)[0] + `,"display":null}`,
		"wrong ID":          strings.Replace(localUserJSON, teamID, serviceID, 1),
	} {
		t.Run(name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.GetManagedUser(t.Context(), teamID); err == nil {
				t.Fatal("malformed user accepted")
			}
		})
	}
}

func TestLocalUserClientPreservesCommittedWrites(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"id":%q}`, teamID)
			return
		}
		fmt.Fprint(w, `{"id":"not-an-id"}`)
	})
	v, err := c.CreateLocalUser(t.Context(), client.LocalUserInput{})
	if err == nil || !v.Committed || v.ID != teamID {
		t.Fatal("created identity forgotten after malformed receipt")
	}
	r, err := c.UpdateLocalUser(t.Context(), teamID, client.LocalUserPatch{EmailSet: true})
	if err == nil || !r.Committed || r.ID != teamID {
		t.Fatal("committed PATCH intent forgotten")
	}
}

func TestLocalUserClientFailureIsSanitizedAndNotRetried(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 412, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"not_found","message":"initial-password-secret"}}`)
			})
			v, err := c.GetManagedUser(t.Context(), teamID)
			if err == nil || v.Committed || strings.Contains(err.Error(), "initial-password-secret") {
				t.Fatal("read failure lost or secret leaked")
			}
			err = c.DeactivateUser(t.Context(), teamID)
			if (err == nil) != (status == 404) || calls != 2 {
				t.Fatal("deactivation failure retried or swallowed")
			}
		})
	}
}
