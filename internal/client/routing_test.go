package client_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const routingOwner = "11111111-1111-4111-8111-111111111111"
const routingID = "22222222-2222-4222-8222-222222222222"
const routingChannel = "33333333-3333-4333-8333-333333333333"
const routingTarget = "44444444-4444-4444-8444-444444444444"

func routingClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, e := client.New(s.URL, "test-token")
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestRoutingFilterPublicationAndDirectRead(t *testing.T) {
	for _, scope := range []string{"team", "service"} {
		t.Run(scope, func(t *testing.T) {
			calls := 0
			c := routingClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				prefix := "/v1/" + scope + "s/" + routingOwner + "/alert-filter"
				if r.Header.Get("If-Match") != "" {
					t.Error("publication must not invent CAS")
				}
				switch calls {
				case 1:
					if r.Method != "POST" || r.URL.Path != prefix+"/revisions" {
						t.Errorf("publication route: %s %s", r.Method, r.URL.Path)
					}
					var b map[string]any
					json.NewDecoder(r.Body).Decode(&b)
					if b["active"] != false || b["cel_source"] != "true" || len(b) != 2 {
						t.Errorf("atomic inactive payload: %#v", b)
					}
				case 2:
					if r.Method != "GET" || r.URL.Path != prefix || r.URL.RawQuery != "" {
						t.Error("refresh must directly GET current definition")
					}
				case 3:
					if r.Method != "POST" || r.URL.Path != prefix+"/disable" {
						t.Error("destroy must disable")
					}
					w.WriteHeader(204)
					return
				default:
					t.Fatal("unexpected request")
				}
				fmt.Fprintf(w, `{"target_kind":%q,"target_id":%q,"revision":7,"cel_source":"true","active":false}`, scope, routingOwner)
			})
			v, e := c.PublishAlertFilter(t.Context(), scope, routingOwner, client.AlertFilterInput{CELSource: "true", Active: false})
			if e != nil || v.Active || v.Revision != 7 {
				t.Fatalf("publication: %#v %v", v, e)
			}
			v, e = c.GetAlertFilter(t.Context(), scope, routingOwner)
			if e != nil || v.TargetID != routingOwner || v.Active {
				t.Fatalf("read: %#v %v", v, e)
			}
			if e = c.DisableAlertFilter(t.Context(), scope, routingOwner); e != nil {
				t.Fatal(e)
			}
			if calls != 3 {
				t.Fatal(calls)
			}
		})
	}
}
func TestRoutingRulePublicationAndDirectRead(t *testing.T) {
	calls := 0
	c := routingClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		base := "/v1/teams/" + routingOwner + "/escalation-rules"
		wantMethod, wantPath := "POST", base
		if calls == 2 {
			wantPath += "/" + routingID + "/revisions"
		}
		if calls == 3 {
			wantMethod = "GET"
			wantPath += "/" + routingID
		}
		if calls == 4 {
			wantPath += "/" + routingID + "/disable"
		}
		if r.Method != wantMethod || r.URL.Path != wantPath || r.Header.Get("If-Match") != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if calls < 3 {
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if b["active"] != false || b["chain_id"] != routingTarget || b["cel_source"] != "true" || b["name"] != "Passive" {
				t.Errorf("publication input %#v", b)
			}
		}
		if calls == 4 {
			w.WriteHeader(204)
			return
		}
		fmt.Fprintf(w, `{"rule_id":%q,"team_id":%q,"chain_id":%q,"name":"Passive","cel_source":"true","active":false,"revision":2}`, routingID, routingOwner, routingTarget)
	})
	in := client.EscalationRuleInput{Name: "Passive", CELSource: "true", ChainID: routingTarget, Active: false}
	a, e := c.CreateEscalationRule(t.Context(), routingOwner, in)
	if e != nil || a.RuleID != routingID {
		t.Fatalf("%#v %v", a, e)
	}
	a, e = c.PublishEscalationRule(t.Context(), routingOwner, routingID, in)
	if e != nil || a.Active {
		t.Fatalf("%#v %v", a, e)
	}
	a, e = c.GetEscalationRule(t.Context(), routingOwner, routingID)
	if e != nil || a.Revision != 2 {
		t.Fatalf("%#v %v", a, e)
	}
	if e = c.DisableEscalationRule(t.Context(), routingOwner, routingID); e != nil {
		t.Fatal(e)
	}
}
func TestRoutingCatalogBindingUsesBindingIdentity(t *testing.T) {
	for _, kind := range []string{"team_alert", "service_alert", "team_oncall"} {
		t.Run(kind, func(t *testing.T) {
			prefix := "/v1/teams/" + routingOwner + "/alert-channels"
			if kind == "service_alert" {
				prefix = "/v1/services/" + routingOwner + "/alert-channels"
			}
			if kind == "team_oncall" {
				prefix = "/v1/teams/" + routingOwner + "/oncall-channels"
			}
			c := routingClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-Match") != "" {
					t.Error("bindings have no CAS")
				}
				want := prefix
				if r.Method != "POST" {
					want += "/" + routingID
				}
				if r.URL.Path != want {
					t.Errorf("binding UUID expected in URL: %s", r.URL.Path)
				}
				if r.Method == "POST" || r.Method == "PUT" {
					var b map[string]string
					json.NewDecoder(r.Body).Decode(&b)
					if b["channel_id"] != routingChannel || b["target_id"] != routingTarget || len(b) != 2 {
						t.Errorf("explicit target: %#v", b)
					}
				}
				if r.Method == "DELETE" {
					w.WriteHeader(204)
					return
				}
				fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"target_id":%q,"unavailable":true}`, routingID, routingChannel, routingTarget)
			})
			in := client.ChannelBindingInput{ChannelID: routingChannel, TargetID: routingTarget}
			a, e := c.CreateChannelBinding(t.Context(), kind, routingOwner, in)
			if e != nil || a.ID != routingID {
				t.Fatalf("%#v %v", a, e)
			}
			a, e = c.GetChannelBinding(t.Context(), kind, routingOwner, routingID)
			if e != nil || !a.Unavailable {
				t.Fatalf("%#v %v", a, e)
			}
			if kind != "team_oncall" {
				a, e = c.UpdateChannelBinding(t.Context(), kind, routingOwner, routingID, in)
				if e != nil || a.ID != routingID {
					t.Fatalf("%#v %v", a, e)
				}
			}
			if e = c.DeleteChannelBinding(t.Context(), kind, routingOwner, routingID); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestRoutingRejectsUnexpectedIdentityAndRetainsHTTPFailures(t *testing.T) {
	c := routingClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"target_kind":"team","target_id":"`+routingID+`","cel_source":"true","revision":1}`)
	})
	if _, e := c.GetAlertFilter(t.Context(), "team", routingOwner); e == nil {
		t.Fatal("foreign identity accepted")
	}
	for _, status := range []int{403, 404, 500} {
		c = routingClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"not_found"}}`)
		})
		_, e := c.GetChannelBinding(t.Context(), "team_alert", routingOwner, routingID)
		if e == nil || client.IsNotFound(e) != (status == 404) {
			t.Fatalf("status %d: %v", status, e)
		}
	}
}

func TestRoutingOncallBindingUpdateIsNeverEmulated(t *testing.T) {
	calls := 0
	c := routingClient(t, func(http.ResponseWriter, *http.Request) { calls++ })
	if _, e := c.UpdateChannelBinding(t.Context(), "team_oncall", routingOwner, routingID, client.ChannelBindingInput{ChannelID: routingChannel, TargetID: routingTarget}); e == nil {
		t.Fatal("unsupported update accepted")
	}
	if calls != 0 {
		t.Fatal("oncall update sent an invented endpoint or implicit delete/create")
	}
}
func TestRoutingRepeatedCleanupOnlyIgnoresConfirmedAbsence(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		c := routingClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			code := "forbidden"
			if status == 404 {
				code = "not_found"
			}
			fmt.Fprintf(w, `{"error":{"code":%q}}`, code)
		})
		for _, e := range []error{c.DisableAlertFilter(t.Context(), "team", routingOwner), c.DisableEscalationRule(t.Context(), routingOwner, routingID), c.DeleteChannelBinding(t.Context(), "team_alert", routingOwner, routingID)} {
			if (e == nil) != (status == 404) {
				t.Fatalf("cleanup status %d: %v", status, e)
			}
		}
	}
}
