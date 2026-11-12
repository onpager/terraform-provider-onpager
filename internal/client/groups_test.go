package client_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func groupResponse(id string, revision int) string {
	return fmt.Sprintf(`{"id":%q,"key":"operations","name":"Operations","description":"","system":false,"revision":%d}`, id, revision)
}
func mappingResponse(group, id string, revision int) string {
	return fmt.Sprintf(`{"id":%q,"group_id":%q,"provider_slug":"primary","issuer":"https://idp.test","client_id":"onpager","external_key":" Ops ","revision":%d}`, id, group, revision)
}
func TestGroupsMetadataAndDeleteHaveDistinctConcurrency(t *testing.T) {
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		switch step {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/groups" || r.Header.Get("If-Match") != "" {
				t.Fatal("unexpected create")
			}
			fmt.Fprint(w, groupResponse(teamID, 1))
		case 2:
			if r.Method != "GET" || r.URL.Path != "/v1/groups/"+teamID {
				t.Fatal("expected direct read")
			}
			fmt.Fprint(w, groupResponse(teamID, 1))
		case 3:
			if r.Method != "PATCH" || r.Header.Get("If-Match") != "" {
				t.Fatal("metadata PATCH must be unversioned")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 1 || body["description"] != "" {
				t.Fatalf("sparse clear lost: %#v", body)
			}
			fmt.Fprint(w, groupResponse(teamID, 2))
		case 4:
			if r.Method != "DELETE" || r.Header.Get("If-Match") != `"2"` {
				t.Fatal("delete must use own revision")
			}
			w.WriteHeader(204)
		default:
			t.Fatal("unexpected retries")
		}
	})
	if _, e := c.CreateGroup(t.Context(), client.GroupInput{Key: "operations", Name: "Operations"}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.GetGroup(t.Context(), teamID); e != nil {
		t.Fatal(e)
	}
	empty := ""
	if _, e := c.UpdateGroup(t.Context(), teamID, client.GroupUpdate{Description: &empty}); e != nil {
		t.Fatal(e)
	}
	if e := c.DeleteGroup(t.Context(), teamID, 2); e != nil {
		t.Fatal(e)
	}
	if step != 4 {
		t.Fatal(step)
	}
}
func TestGroupsManualMembershipUsesOnlyDirectSource(t *testing.T) {
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if r.URL.Path != "/v1/groups/"+teamID+"/manual-members/"+serviceID || r.Header.Get("If-Match") != "" {
			t.Fatal("wrong membership route")
		}
		if step == 1 && r.Method == "PUT" {
			w.WriteHeader(204)
			return
		}
		if step == 2 && r.Method == "GET" {
			fmt.Fprintf(w, `{"group_id":%q,"user_id":%q}`, teamID, serviceID)
			return
		}
		if step == 3 && r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		t.Fatal("unexpected collection or mutation")
	})
	if e := c.EnsureGroupMembership(t.Context(), teamID, serviceID); e != nil {
		t.Fatal(e)
	}
	if _, e := c.GetGroupMembership(t.Context(), teamID, serviceID); e != nil {
		t.Fatal(e)
	}
	if e := c.RemoveGroupMembership(t.Context(), teamID, serviceID); e != nil {
		t.Fatal(e)
	}
}
func TestGroupsOIDCMappingUsesScopedIdentityAndOwnCAS(t *testing.T) {
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		path := "/v1/groups/" + teamID + "/oidc-mappings"
		if step > 1 {
			path += "/" + serviceID
		}
		if r.URL.Path != path {
			t.Fatal(r.URL.Path)
		}
		switch step {
		case 1:
			if r.Method != "POST" || r.Header.Get("If-Match") != "" {
				t.Fatal("create")
			}
		case 2:
			if r.Method != "GET" {
				t.Fatal("direct get")
			}
		case 3:
			if r.Method != "PUT" || r.Header.Get("If-Match") != `"7"` {
				t.Fatal("replace CAS")
			}
		case 4:
			if r.Method != "DELETE" || r.Header.Get("If-Match") != `"8"` {
				t.Fatal("delete CAS")
			}
			w.WriteHeader(204)
			return
		default:
			t.Fatal("extra request")
		}
		if r.Method != "GET" {
			var body client.OIDCGroupMappingInput
			json.NewDecoder(r.Body).Decode(&body)
			if body.ExternalKey != " Ops " {
				t.Fatal("literal external key changed")
			}
		}
		rev := 7
		if step == 3 {
			rev = 8
		}
		fmt.Fprint(w, mappingResponse(teamID, serviceID, rev))
	})
	in := client.OIDCGroupMappingInput{ProviderSlug: "primary", Issuer: "https://idp.test", ClientID: "onpager", ExternalKey: " Ops "}
	if _, e := c.CreateOIDCGroupMapping(t.Context(), teamID, in); e != nil {
		t.Fatal(e)
	}
	if _, e := c.GetOIDCGroupMapping(t.Context(), teamID, serviceID); e != nil {
		t.Fatal(e)
	}
	if _, e := c.UpdateOIDCGroupMapping(t.Context(), teamID, serviceID, 7, in); e != nil {
		t.Fatal(e)
	}
	if e := c.DeleteOIDCGroupMapping(t.Context(), teamID, serviceID, 8); e != nil {
		t.Fatal(e)
	}
}
func TestGroupsRejectMalformedResponsesAndIdentityMismatch(t *testing.T) {
	for _, body := range []string{`{}`, `{"id":"` + teamID + `","key":"operations","name":"Operations","revision":1}`, groupResponse(serviceID, 1), groupResponse(teamID, 0)} {
		t.Run(body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, e := c.GetGroup(t.Context(), teamID); e == nil {
				t.Fatal("malformed group accepted")
			}
		})
	}
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, mappingResponse(serviceID, teamID, 1)) })
	if _, e := c.GetOIDCGroupMapping(t.Context(), teamID, serviceID); e == nil {
		t.Fatal("wrong mapping identity accepted")
	}
	c = fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"group_id":%q,"user_id":%q}`, serviceID, teamID)
	})
	if _, e := c.GetGroupMembership(t.Context(), teamID, serviceID); e == nil {
		t.Fatal("wrong manual source accepted")
	}
}
func TestGroupsDeleteAllowsOnlyConfirmedAbsenceAndNeverRetries(t *testing.T) {
	for _, status := range []int{403, 404, 409, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			})
			if e := c.DeleteGroup(t.Context(), teamID, 1); (e == nil) != (status == 404) {
				t.Fatal(e)
			}
			if calls != 1 {
				t.Fatal("retried mutation")
			}
		})
	}
}

func TestGroupsMalformedCreateRetainsKnownIdentity(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":%q,"revision":"invalid"}`, teamID)
	})
	v, err := c.CreateGroup(t.Context(), client.GroupInput{Key: "ops", Name: "Operations"})
	if err == nil || v.ID != teamID {
		t.Fatal("malformed success must return an error and retain known created identity")
	}
}
