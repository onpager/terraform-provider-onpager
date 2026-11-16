package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

var responderStepType = types.ObjectType{AttrTypes: map[string]attr.Type{"kind": types.StringType, "wait_seconds": types.Int64Type, "target_user_id": types.StringType, "schedule_id": types.StringType, "importance": types.StringType}}

const responderPolicyZero = `{"team_id":"00000000-0000-0000-0000-000000000000","importance":"","strategy":"","revision":0,"chain_id":"00000000-0000-0000-0000-000000000000","chain_steps":null}`

func responderPolicyValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam + "/important"), "team_id": types.StringValue(fTeam), "importance": types.StringValue("important"), "strategy": types.StringValue("team_fanout"), "chain_id": types.StringNull(), "publication_trigger": types.StringValue("snapshot-v1"), "revision": types.Int64Value(7), "chain_steps": types.ListValueMust(responderStepType, nil)}
}
func responderPolicyJSON(strategy string, revision int) string {
	if strategy == "team_fanout" {
		return fmt.Sprintf(`{"team_id":%q,"importance":"important","strategy":"team_fanout","revision":%d,"chain_id":"00000000-0000-0000-0000-000000000000","chain_steps":[]}`, fTeam, revision)
	}
	return fmt.Sprintf(`{"team_id":%q,"importance":"important","strategy":"team_chain","revision":%d,"chain_id":%q,"chain_steps":[{"kind":"notify_user","target_user_id":%q},{"kind":"wait","wait_seconds":60}]}`, fTeam, revision, fService, fOtherTeam)
}
func responderPolicyAssert(t *testing.T, state tfsdk.State, strategy string, revision int64) {
	t.Helper()
	var id, got, trigger types.String
	var rev types.Int64
	var steps types.List
	state.GetAttribute(t.Context(), path.Root("id"), &id)
	state.GetAttribute(t.Context(), path.Root("strategy"), &got)
	state.GetAttribute(t.Context(), path.Root("revision"), &rev)
	state.GetAttribute(t.Context(), path.Root("chain_steps"), &steps)
	state.GetAttribute(t.Context(), path.Root("publication_trigger"), &trigger)
	if state.Raw.IsNull() || id.ValueString() != fTeam+"/important" || got.ValueString() != strategy || revision == 0 && (!rev.IsNull() || !steps.IsNull()) || revision > 0 && rev.ValueInt64() != revision || trigger.ValueString() != "snapshot-v1" {
		t.Fatalf("lost policy intent or config-only trigger: id=%s strategy=%s revision=%s steps=%s trigger=%s", id, got, rev, steps, trigger)
	}
}

// An already configured slot remains unowned even when the configured policy matches.
func TestTeamResponderPolicyCreateGuardsUnownedExactSlot(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			r := onpager.NewTeamResponderPolicyResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.URL.Path != "/v1/teams/"+fTeam+"/responder-policies" || q.Header.Get("If-Match") != "" {
					t.Error("incorrect policy route or CAS")
				}
				if calls == 1 {
					if q.Method != "GET" || q.URL.RawQuery != "importance=important" {
						t.Error("create did not guard exact slot")
					}
					if existing {
						fmt.Fprint(w, responderPolicyJSON("team_fanout", 7))
					} else {
						fmt.Fprint(w, responderPolicyZero)
					}
					return
				}
				if q.Method != "POST" || q.URL.RawQuery != "" {
					t.Error("create did not publish directly")
				}
				w.WriteHeader(201)
				fmt.Fprint(w, responderPolicyJSON("team_fanout", 8))
			})
			values := responderPolicyValues()
			values["id"], values["revision"], values["chain_steps"] = types.StringUnknown(), types.Int64Unknown(), types.ListUnknown(responderStepType)
			plan := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if resp.Diagnostics.HasError() != existing || existing && (calls != 1 || !resp.State.Raw.IsNull()) || !existing && calls != 2 {
				t.Fatal(resp.Diagnostics, calls)
			}
			if !existing {
				responderPolicyAssert(t, resp.State, "team_fanout", 8)
			} else if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "Import") {
				t.Fatal("missing ownership import guidance", resp.Diagnostics)
			}
		})
	}
}

// Missing grants/owner reads never remove state; valid same-slot drift is authoritative.
func TestTeamResponderPolicyReadAbsenceErrorsAndDrift(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		absent, bad bool
	}{{"drift", responderPolicyJSON("team_chain", 9), 200, false, false}, {"absent", responderPolicyZero, 200, true, false}, {"empty", "{}", 200, false, true}, {"foreign", strings.Replace(responderPolicyJSON("team_fanout", 9), fTeam, fOtherTeam, 1), 200, false, true}, {"denied", `{"error":{"code":"forbidden"}}`, 403, false, true}, {"not-found", `{"error":{"code":"not_found"}}`, 404, false, true}, {"auth", `{}`, 401, false, true}, {"server", `{}`, 500, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewTeamResponderPolicyResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.URL.RawQuery != "importance=important" || q.Header.Get("If-Match") != "" {
					t.Error("refresh mutated/fell back")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			before := valueState(t, s, responderPolicyValues())
			resp := resource.ReadResponse{State: before}
			r.Read(t.Context(), resource.ReadRequest{State: before}, &resp)
			if resp.Diagnostics.HasError() != tc.bad || resp.State.Raw.IsNull() != tc.absent {
				t.Fatal(resp.Diagnostics, resp.State.Raw)
			}
			if !tc.absent {
				if tc.bad {
					responderPolicyAssert(t, resp.State, "team_fanout", 7)
				} else {
					responderPolicyAssert(t, resp.State, "team_chain", 9)
				}
			}
		})
	}
}

// Trigger-only changes republish without sending the trigger, old snapshot, or observed revision.
func TestTeamResponderPolicyUpdateDirectAndRetainsHTTPFailures(t *testing.T) {
	for _, status := range []int{201, 401, 403, 404, 409, 412, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamResponderPolicyResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				var body map[string]json.RawMessage
				_ = json.NewDecoder(q.Body).Decode(&body)
				if calls != 1 || q.Method != "POST" || q.URL.RawQuery != "" || q.Header.Get("If-Match") != "" || len(body) != 2 || string(body["importance"]) != `"important"` || string(body["strategy"]) != `"team_fanout"` {
					t.Error("update added pre-read/CAS/snapshot/trigger")
				}
				w.WriteHeader(status)
				if status == 201 {
					fmt.Fprint(w, responderPolicyJSON("team_fanout", 11))
				} else {
					fmt.Fprint(w, `{"error":{"code":"conflict"}}`)
				}
			})
			before := valueState(t, s, responderPolicyValues())
			values := responderPolicyValues()
			values["revision"] = types.Int64Unknown()
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: before}
			r.Update(t.Context(), resource.UpdateRequest{State: before, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if resp.Diagnostics.HasError() != (status != 201) || calls != 1 {
				t.Fatal(resp.Diagnostics, calls)
			}
			rev := int64(7)
			if status == 201 {
				rev = 11
			}
			responderPolicyAssert(t, resp.State, "team_fanout", rev)
		})
	}
}

func TestTeamResponderPolicyMalformedCommittedReceiptRetainsDesiredIntent(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		for _, status := range []int{201, 200, 204} {
			t.Run(fmt.Sprint(operation, status), func(t *testing.T) {
				r := onpager.NewTeamResponderPolicyResource()
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
					calls++
					if operation == "create" && calls == 1 {
						fmt.Fprint(w, responderPolicyZero)
						return
					}
					if q.Method != "POST" {
						t.Error("unexpected follow-up GET")
					}
					w.WriteHeader(status)
					fmt.Fprint(w, `{"revision":99,"chain_steps":"private-malformed-response"}`)
				})
				before := valueState(t, s, responderPolicyValues())
				values := responderPolicyValues()
				values["strategy"], values["chain_id"], values["revision"], values["chain_steps"] = types.StringValue("team_chain"), types.StringValue(fService), types.Int64Unknown(), types.ListUnknown(responderStepType)
				plan := valueState(t, s, values)
				var got tfsdk.State
				if operation == "create" {
					resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
					r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
					if !resp.Diagnostics.HasError() || calls != 2 {
						t.Fatal(resp.Diagnostics, calls)
					}
					got = resp.State
				} else {
					resp := resource.UpdateResponse{State: before}
					r.Update(t.Context(), resource.UpdateRequest{State: before, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
					if !resp.Diagnostics.HasError() || calls != 1 {
						t.Fatal(resp.Diagnostics, calls)
					}
					got = resp.State
				}
				responderPolicyAssert(t, got, "team_chain", 0)
				var chain types.String
				got.GetAttribute(t.Context(), path.Root("chain_id"), &chain)
				if chain.ValueString() != fService {
					t.Fatal("committed desired chain lost", chain)
				}
			})
		}
	}
}

func TestTeamResponderPolicyDestroyOnlyExactSlotAndRetainsErrors(t *testing.T) {
	for _, status := range []int{204, 401, 403, 404, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamResponderPolicyResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				body, _ := io.ReadAll(q.Body)
				if q.Method != "DELETE" || q.URL.Path != "/v1/teams/"+fTeam+"/responder-policies" || q.URL.RawQuery != "importance=important" || len(body) != 0 || q.Header.Get("If-Match") != "" {
					t.Error("destroy changed more than exact slot or invented CAS")
				}
				w.WriteHeader(status)
			})
			before := valueState(t, s, responderPolicyValues())
			resp := resource.DeleteResponse{State: before}
			r.Delete(t.Context(), resource.DeleteRequest{State: before}, &resp)
			if calls != 1 || resp.Diagnostics.HasError() != (status != 204) {
				t.Fatal(resp.Diagnostics, calls)
			}
			if status != 204 {
				responderPolicyAssert(t, resp.State, "team_fanout", 7)
			}
		})
	}
}

func TestTeamResponderPolicyImportValidatesCompositeIdentityReadOnly(t *testing.T) {
	r := onpager.NewTeamResponderPolicyResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("import performed API operation") })
	for _, id := range []string{fTeam + "/default", fTeam + "/important", fTeam, "default", fTeam + "/", fTeam + "/Default", fTeam + "/important/extra", "00000000-0000-0000-0000-000000000000/default"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		valid := id == fTeam+"/default" || id == fTeam+"/important"
		if resp.Diagnostics.HasError() == valid {
			t.Fatal(id, resp.Diagnostics)
		}
		if valid {
			var trigger types.String
			resp.State.GetAttribute(t.Context(), path.Root("publication_trigger"), &trigger)
			if !trigger.IsNull() {
				t.Fatal("import invented trigger", trigger)
			}
		}
	}
}

func TestTeamResponderPolicyInvalidKnownConfigAndUnknownChainReference(t *testing.T) {
	for _, tc := range []struct {
		strategy, chain types.String
		bad             bool
	}{{types.StringValue("team_chain"), types.StringNull(), true}, {types.StringValue("team_chain"), types.StringUnknown(), false}, {types.StringValue("team_chain"), types.StringValue(fService), false}, {types.StringValue("team_fanout"), types.StringValue(fService), true}, {types.StringValue("team_fanout"), types.StringNull(), false}, {types.StringUnknown(), types.StringUnknown(), false}} {
		r := onpager.NewTeamResponderPolicyResource()
		s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("validation performed request") })
		values := responderPolicyValues()
		values["strategy"], values["chain_id"] = tc.strategy, tc.chain
		v := valueState(t, s, values)
		resp := resource.ValidateConfigResponse{}
		r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: v.Raw}}, &resp)
		if resp.Diagnostics.HasError() != tc.bad {
			t.Fatal(tc, resp.Diagnostics)
		}
	}
	for _, field := range []string{"team_id", "importance", "strategy", "chain_id"} {
		for _, bad := range []types.String{types.StringUnknown(), types.StringValue("invalid")} {
			r := onpager.NewTeamResponderPolicyResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("invalid apply sent request") })
			values := responderPolicyValues()
			values[field] = bad
			if field == "chain_id" {
				values["strategy"] = types.StringValue("team_chain")
			}
			v := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: v.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid apply accepted", field, bad)
			}
		}
	}
}
