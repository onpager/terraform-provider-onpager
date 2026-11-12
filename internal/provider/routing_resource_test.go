package provider_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func routingFilterValues(owner string) map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), owner: types.StringValue(fTeam), "cel_source": types.StringValue("true"), "active": types.BoolValue(true), "revision": types.Int64Value(1)}
}
func routingRuleValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fService), "team_id": types.StringValue(fTeam), "name": types.StringValue("Passive"), "cel_source": types.StringValue("true"), "chain_id": types.StringValue(fOtherTeam), "active": types.BoolValue(true), "revision": types.Int64Value(1)}
}
func routingBindingValues(owner string) map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fService), owner: types.StringValue(fTeam), "delivery_channel_id": types.StringValue(fOtherTeam), "target_id": types.StringValue(fTeam), "unavailable": types.BoolValue(false)}
}
func TestRoutingReadRetainsInactiveDefinitionsAndHTTPFailures(t *testing.T) {
	cases := []struct {
		r                 resource.Resource
		owner, path, body string
		values            map[string]attr.Value
	}{
		{onpager.NewTeamAlertFilterResource(), "team_id", "/v1/teams/" + fTeam + "/alert-filter", `{"target_kind":"team","target_id":"` + fTeam + `","revision":3,"cel_source":"false","active":false}`, routingFilterValues("team_id")},
		{onpager.NewServiceAlertFilterResource(), "service_id", "/v1/services/" + fTeam + "/alert-filter", `{"target_kind":"service","target_id":"` + fTeam + `","revision":3,"cel_source":"false","active":false}`, routingFilterValues("service_id")},
		{onpager.NewEscalationRuleResource(), "team_id", "/v1/teams/" + fTeam + "/escalation-rules/" + fService, `{"rule_id":"` + fService + `","team_id":"` + fTeam + `","revision":3,"cel_source":"false","active":false,"name":"Passive","chain_id":"` + fOtherTeam + `"}`, routingRuleValues()},
	}
	for i, tc := range cases {
		for _, status := range []int{200, 403, 404, 500} {
			t.Run(fmt.Sprintf("%d/%d", i, status), func(t *testing.T) {
				s := configuredResource(t, tc.r, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.URL.Path != tc.path || r.URL.RawQuery != "" {
						t.Errorf("refresh route: %s %s", r.Method, r.URL.String())
					}
					w.WriteHeader(status)
					if status == 200 {
						io.WriteString(w, tc.body)
					} else {
						code := "forbidden"
						if status == 404 {
							code = "not_found"
						}
						fmt.Fprintf(w, `{"error":{"code":%q}}`, code)
					}
				})
				state := valueState(t, s, tc.values)
				resp := resource.ReadResponse{State: state}
				tc.r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
				if status == 404 {
					if !resp.State.Raw.IsNull() {
						t.Fatal("confirmed absence retained")
					}
					return
				}
				if resp.State.Raw.IsNull() {
					t.Fatal("inactive/unauthorized object removed")
				}
				if status != 200 {
					if !resp.Diagnostics.HasError() {
						t.Fatal("HTTP failure swallowed")
					}
					return
				}
				if resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
				var active types.Bool
				resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
				if active.ValueBool() {
					t.Fatal("inactive state not refreshed")
				}
			})
		}
	}
}
func TestRoutingAtomicInactiveFilterCreate(t *testing.T) {
	r := onpager.NewTeamAlertFilterResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		b, _ := io.ReadAll(q.Body)
		if q.Method != "POST" || q.URL.Path != "/v1/teams/"+fTeam+"/alert-filter/revisions" || string(b) != `{"cel_source":"true","active":false}` {
			t.Errorf("non-atomic publish: %s %s %s", q.Method, q.URL.Path, b)
		}
		io.WriteString(w, `{"target_kind":"team","target_id":"`+fTeam+`","cel_source":"true","active":false,"revision":2}`)
	})
	values := routingFilterValues("team_id")
	values["id"] = types.StringUnknown()
	values["revision"] = types.Int64Unknown()
	values["active"] = types.BoolValue(false)
	plan := valueState(t, s, values)
	var resp resource.CreateResponse
	resp.State.Schema = s
	resp.State.Raw = tftypes.NewValue(plan.Raw.Type(), nil)
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if calls != 1 {
		t.Fatal("publish-then-disable is forbidden")
	}
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if id.ValueString() != fTeam {
		t.Fatal("owner is stable filter identity")
	}
}
func TestRoutingBindingRefreshIdentityAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		r             resource.Resource
		owner, prefix string
	}{
		{onpager.NewTeamAlertChannelResource(), "team_id", "/v1/teams/" + fTeam + "/alert-channels/"},
		{onpager.NewServiceAlertChannelResource(), "service_id", "/v1/services/" + fTeam + "/alert-channels/"},
		{onpager.NewTeamOncallChannelResource(), "team_id", "/v1/teams/" + fTeam + "/oncall-channels/"},
	} {
		for _, status := range []int{200, 403, 404} {
			t.Run(fmt.Sprintf("%s/%d", tc.owner, status), func(t *testing.T) {
				s := configuredResource(t, tc.r, func(w http.ResponseWriter, q *http.Request) {
					if q.Method != "GET" || q.URL.Path != tc.prefix+fService {
						t.Errorf("must use binding UUID: %s %s", q.Method, q.URL.Path)
					}
					w.WriteHeader(status)
					if status == 200 {
						fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"target_id":%q,"unavailable":true}`, fService, fOtherTeam, fTeam)
					} else {
						code := "forbidden"
						if status == 404 {
							code = "not_found"
						}
						fmt.Fprintf(w, `{"error":{"code":%q}}`, code)
					}
				})
				state := valueState(t, s, routingBindingValues(tc.owner))
				resp := resource.ReadResponse{State: state}
				tc.r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
				if status == 404 {
					if !resp.State.Raw.IsNull() {
						t.Fatal("missing binding retained")
					}
					return
				}
				if resp.State.Raw.IsNull() {
					t.Fatal("inaccessible binding removed")
				}
				if status == 403 {
					if !resp.Diagnostics.HasError() {
						t.Fatal("403 swallowed")
					}
					return
				}
				if resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
				var unavailable types.Bool
				resp.State.GetAttribute(t.Context(), path.Root("unavailable"), &unavailable)
				if !unavailable.ValueBool() {
					t.Fatal("unavailable binding must be retained")
				}
			})
		}
	}
}
func TestRoutingDestroyFailureAndScopedImports(t *testing.T) {
	for _, tc := range []struct {
		r              resource.Resource
		owner          string
		values         map[string]attr.Value
		method, suffix string
	}{
		{onpager.NewTeamAlertFilterResource(), "team_id", routingFilterValues("team_id"), "POST", "/alert-filter/disable"},
		{onpager.NewServiceAlertFilterResource(), "service_id", routingFilterValues("service_id"), "POST", "/alert-filter/disable"},
		{onpager.NewEscalationRuleResource(), "team_id", routingRuleValues(), "POST", "/escalation-rules/" + fService + "/disable"},
		{onpager.NewTeamAlertChannelResource(), "team_id", routingBindingValues("team_id"), "DELETE", "/alert-channels/" + fService},
		{onpager.NewServiceAlertChannelResource(), "service_id", routingBindingValues("service_id"), "DELETE", "/alert-channels/" + fService},
		{onpager.NewTeamOncallChannelResource(), "team_id", routingBindingValues("team_id"), "DELETE", "/oncall-channels/" + fService},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			s := configuredResource(t, tc.r, func(w http.ResponseWriter, q *http.Request) {
				scope := "teams"
				if tc.owner == "service_id" {
					scope = "services"
				}
				if q.Method != tc.method || q.URL.Path != "/v1/"+scope+"/"+fTeam+tc.suffix {
					t.Errorf("cleanup route %s %s", q.Method, q.URL.Path)
				}
				w.WriteHeader(403)
				io.WriteString(w, `{"error":{"code":"forbidden"}}`)
			})
			state := valueState(t, s, tc.values)
			var d resource.DeleteResponse
			tc.r.Delete(t.Context(), resource.DeleteRequest{State: state}, &d)
			if !d.Diagnostics.HasError() {
				t.Fatal("failed cleanup must prevent forgetting")
			}
			id := fTeam + "/" + fService
			if tc.method == "POST" && tc.suffix == "/alert-filter/disable" {
				id = fTeam
			}
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
			tc.r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var got types.String
			resp.State.GetAttribute(t.Context(), path.Root(tc.owner), &got)
			if got.ValueString() != fTeam {
				t.Fatal("import lost owner scope")
			}
			bad := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
			tc.r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: "invalid"}, &bad)
			if !bad.Diagnostics.HasError() {
				t.Fatal("invalid scoped import accepted")
			}
		})
	}
}

func TestRoutingAlertBindingUpdateUsesPutAndRetainsStateOnFailure(t *testing.T) {
	for _, status := range []int{200, 403, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamAlertChannelResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				b, _ := io.ReadAll(q.Body)
				if q.Method != "PUT" || q.URL.Path != "/v1/teams/"+fTeam+"/alert-channels/"+fService || q.Header.Get("If-Match") != "" || string(b) != `{"channel_id":"`+fOtherTeam+`","target_id":"`+fOtherTeam+`"}` {
					t.Errorf("binding update: %s %s %s", q.Method, q.URL.Path, b)
				}
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"target_id":%q}`, fService, fOtherTeam, fOtherTeam)
				} else {
					io.WriteString(w, `{"error":{"code":"conflict"}}`)
				}
			})
			old := routingBindingValues("team_id")
			state := valueState(t, s, old)
			newValues := routingBindingValues("team_id")
			newValues["target_id"] = types.StringValue(fOtherTeam)
			plan := valueState(t, s, newValues)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			var got types.String
			resp.State.GetAttribute(t.Context(), path.Root("target_id"), &got)
			want := fTeam
			if status == 200 {
				want = fOtherTeam
			}
			if got.ValueString() != want {
				t.Fatalf("state lost: target %s want %s", got.ValueString(), want)
			}
			if resp.Diagnostics.HasError() != (status != 200) {
				t.Fatal(resp.Diagnostics)
			}
			if calls != 1 {
				t.Fatal("mutation retried")
			}
		})
	}
}
func TestRoutingRuleCreateAndUpdatePublishInactiveAtomically(t *testing.T) {
	r := onpager.NewEscalationRuleResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		b, _ := io.ReadAll(q.Body)
		want := "/v1/teams/" + fTeam + "/escalation-rules"
		if calls == 2 {
			want += "/" + fService + "/revisions"
		}
		if q.Method != "POST" || q.URL.Path != want || string(b) != `{"name":"Passive","cel_source":"true","chain_id":"`+fOtherTeam+`","active":false}` {
			t.Errorf("atomic publication: %s %s %s", q.Method, q.URL.Path, b)
		}
		fmt.Fprintf(w, `{"rule_id":%q,"team_id":%q,"name":"Passive","cel_source":"true","chain_id":%q,"active":false,"revision":%d}`, fService, fTeam, fOtherTeam, calls)
	})
	v := routingRuleValues()
	v["active"] = types.BoolValue(false)
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	plan := valueState(t, s, v)
	create := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &create)
	if create.Diagnostics.HasError() {
		t.Fatal(create.Diagnostics)
	}
	update := resource.UpdateResponse{State: create.State}
	r.Update(t.Context(), resource.UpdateRequest{State: create.State, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &update)
	if update.Diagnostics.HasError() {
		t.Fatal(update.Diagnostics)
	}
	var rev types.Int64
	update.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
	if rev.ValueInt64() != 2 {
		t.Fatal("published revision not stored")
	}
	if calls != 2 {
		t.Fatal("extra mutation")
	}
}

func TestRoutingBindingPlansReplaceOnlyImmutablePairs(t *testing.T) {
	for _, tc := range []struct {
		r       resource.Resource
		replace bool
	}{
		{onpager.NewTeamAlertChannelResource(), false},
		{onpager.NewServiceAlertChannelResource(), false},
		{onpager.NewTeamOncallChannelResource(), true},
	} {
		var sr resource.SchemaResponse
		tc.r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		owner := "team_id"
		if _, ok := sr.Schema.Attributes["service_id"]; ok {
			owner = "service_id"
		}
		state := valueState(t, sr.Schema, routingBindingValues(owner))
		for _, field := range []string{owner, "delivery_channel_id", "target_id"} {
			want := tc.replace || field == owner
			attribute := sr.Schema.Attributes[field].(schema.StringAttribute)
			replace := false
			for _, modifier := range attribute.PlanModifiers {
				var resp planmodifier.StringResponse
				modifier.PlanModifyString(t.Context(), planmodifier.StringRequest{State: state, Plan: tfsdk.Plan{Schema: sr.Schema, Raw: state.Raw}, StateValue: types.StringValue(fTeam), PlanValue: types.StringValue(fOtherTeam)}, &resp)
				if resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
				replace = replace || resp.RequiresReplace
			}
			if replace != want {
				t.Errorf("%s replacement %t want %t", field, replace, want)
			}
		}
	}
}
func TestRoutingBindingCreateStoresBindingUUIDAndExplicitPair(t *testing.T) {
	r := onpager.NewServiceAlertChannelResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		body, _ := io.ReadAll(q.Body)
		if q.Method != "POST" || q.URL.Path != "/v1/services/"+fTeam+"/alert-channels" || string(body) != `{"channel_id":"`+fOtherTeam+`","target_id":"`+fTeam+`"}` {
			t.Errorf("binding create %s %s %s", q.Method, q.URL.Path, body)
		}
		fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"target_id":%q}`, fService, fOtherTeam, fTeam)
	})
	values := routingBindingValues("service_id")
	values["id"] = types.StringUnknown()
	values["unavailable"] = types.BoolUnknown()
	plan := valueState(t, s, values)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if id.ValueString() != fService {
		t.Fatal("delivery channel UUID used as resource ID")
	}
}

func TestRoutingRuleCreatePreservesTrustworthyIncompleteSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		keep       bool
		active     bool
		revision   int64
		nameValue  string
	}{
		{"ID receipt", `{"rule_id":"` + fService + `"}`, 201, true, true, 0, "Passive"},
		{"known values", `{"rule_id":"` + fService + `","team_id":"` + fTeam + `","name":"Returned","active":false,"revision":4}`, 201, true, false, 4, "Returned"},
		{"foreign scope", `{"rule_id":"` + fService + `","team_id":"` + fOtherTeam + `"}`, 201, false, false, 0, ""},
		{"invalid ID", `{"rule_id":"bad-id"}`, 201, false, false, 0, ""},
		{"no ID", `{}`, 201, false, false, 0, ""},
		{"HTTP error IDs are untrusted", `{"rule_id":"` + fService + `","team_id":"` + fTeam + `","error":{"code":"forbidden"}}`, 403, false, false, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := onpager.NewEscalationRuleResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			values := routingRuleValues()
			values["id"] = types.StringUnknown()
			values["revision"] = types.Int64Unknown()
			planned := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(planned.Raw.Type(), nil)}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planned.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("incomplete response must remain an error")
			}
			if calls != 1 {
				t.Fatal("create was retried or reconstructed")
			}
			if !tc.keep {
				if !resp.State.Raw.IsNull() {
					t.Fatal("untrustworthy identity created state")
				}
				return
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("successful create lost known rule UUID")
			}
			var id, name, team, source, chain types.String
			var active types.Bool
			var rev types.Int64
			for field, value := range map[string]*types.String{"id": &id, "team_id": &team, "name": &name, "cel_source": &source, "chain_id": &chain} {
				if d := resp.State.GetAttribute(t.Context(), path.Root(field), value); d.HasError() {
					t.Fatal(d)
				}
			}
			resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
			resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
			if id.ValueString() != fService || team.ValueString() != fTeam || name.ValueString() != tc.nameValue || source.ValueString() != "true" || chain.ValueString() != fOtherTeam || active.ValueBool() != tc.active {
				t.Fatalf("partial state lost known/planned values: %s", resp.State.Raw)
			}
			if tc.revision == 0 {
				if !rev.IsNull() {
					t.Fatal("missing revision must be null")
				}
			} else if rev.ValueInt64() != tc.revision {
				t.Fatal("returned revision lost")
			}
		})
	}
}

func TestRoutingBindingCreatePreservesTrustworthyIncompleteSuccess(t *testing.T) {
	for _, constructor := range []func() resource.Resource{onpager.NewTeamAlertChannelResource, onpager.NewServiceAlertChannelResource, onpager.NewTeamOncallChannelResource} {
		for _, tc := range []struct {
			name, body                           string
			status                               int
			keep, knownAvailability, unavailable bool
			channel                              string
		}{
			{"ID receipt", `{"id":"` + fService + `"}`, 201, true, false, false, fOtherTeam},
			{"known values", `{"id":"` + fService + `","channel_id":"` + fService + `","unavailable":false}`, 201, true, true, false, fService},
			{"unavailable value", `{"id":"` + fService + `","unavailable":true}`, 201, true, true, true, fOtherTeam},
			{"no ID", `{}`, 201, false, false, false, ""},
			{"invalid ID", `{"id":"bad-id"}`, 201, false, false, false, ""},
			{"HTTP error IDs are untrusted", `{"id":"` + fService + `","error":{"code":"forbidden"}}`, 403, false, false, false, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				calls := 0
				r := constructor()
				s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
					calls++
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				})
				owner := "team_id"
				if _, ok := s.Attributes["service_id"]; ok {
					owner = "service_id"
				}
				values := routingBindingValues(owner)
				values["id"] = types.StringUnknown()
				values["unavailable"] = types.BoolUnknown()
				planned := valueState(t, s, values)
				resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(planned.Raw.Type(), nil)}}
				r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planned.Raw}}, &resp)
				if !resp.Diagnostics.HasError() {
					t.Fatal("incomplete response must remain an error")
				}
				if calls != 1 {
					t.Fatal("create was retried or reconstructed")
				}
				if !tc.keep {
					if !resp.State.Raw.IsNull() {
						t.Fatal("untrustworthy identity created state")
					}
					return
				}
				if resp.State.Raw.IsNull() {
					t.Fatal("successful create lost known binding UUID")
				}
				var id, ownerID, channel, target types.String
				var unavailable types.Bool
				for field, value := range map[string]*types.String{"id": &id, owner: &ownerID, "delivery_channel_id": &channel, "target_id": &target} {
					if d := resp.State.GetAttribute(t.Context(), path.Root(field), value); d.HasError() {
						t.Fatal(d)
					}
				}
				resp.State.GetAttribute(t.Context(), path.Root("unavailable"), &unavailable)
				if id.ValueString() != fService || ownerID.ValueString() != fTeam || channel.ValueString() != tc.channel || target.ValueString() != fTeam {
					t.Fatalf("partial state lost known/planned values: %s", resp.State.Raw)
				}
				if !tc.knownAvailability {
					if !unavailable.IsNull() {
						t.Fatal("missing availability must be null")
					}
				} else if unavailable.IsNull() || unavailable.ValueBool() != tc.unavailable {
					t.Fatal("returned availability lost")
				}
			})
		}
	}
}
