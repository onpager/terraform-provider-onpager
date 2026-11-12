package client_test

import (
	"io"
	"net/http"
	"testing"
)

func TestReactivationUsesExplicitEmptyBodyOperations(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		want := "/v1/teams/" + teamID + "/reactivate"
		if calls == 2 {
			want = "/v1/services/" + serviceID + "/reactivate"
		}
		if r.Method != "POST" || r.URL.Path != want {
			t.Errorf("wrong reactivation operation: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Error("reactivation body must be empty")
		}
		w.WriteHeader(204)
	})
	if err := c.ReactivateTeam(t.Context(), teamID); err != nil {
		t.Fatal(err)
	}
	if err := c.ReactivateService(t.Context(), serviceID); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipReadUsesDirectDetailAndPreservesFailures(t *testing.T) {
	const user = "33333333-3333-4333-8333-333333333333"
	for _, status := range []int{200, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.RequestURI() != "/v1/teams/"+teamID+"/members/"+user {
					t.Error("membership must use the direct detail route")
				}
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, `{"team_id":"`+teamID+`","user_id":"`+user+`","lead":true}`)
				} else {
					io.WriteString(w, `{"error":{"code":"not_found"}}`)
				}
			})
			v, err := c.GetMembership(t.Context(), teamID, user)
			if status == 200 && (err != nil || !v.Lead) {
				t.Fatalf("read failed: %+v %v", v, err)
			}
			if status != 200 && err == nil {
				t.Fatal("API failure swallowed")
			}
			if calls != 1 {
				t.Fatal("unexpected fallback or retry")
			}
		})
	}
}
