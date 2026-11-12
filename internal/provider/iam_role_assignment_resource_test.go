package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func assignmentValues() map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(fTeam), "role_id": types.StringValue(fService), "target_team_id": types.StringNull(),
		"recipient": types.ObjectValueMust(map[string]attr.Type{"kind": types.StringType, "id": types.StringType}, map[string]attr.Value{"kind": types.StringValue("service_account"), "id": types.StringValue(scheduleID)}),
	}
}

func TestRoleAssignmentReadAcceptsSameUUIDTupleDrift(t *testing.T) {
	r := onpager.NewRoleAssignmentResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" || q.URL.Path != "/v1/rbac/assignments/"+fTeam {
			t.Error("drift refresh must direct-read only")
		}
		fmt.Fprintf(w, `{"id":%q,"role_id":%q,"recipient":{"kind":"group","id":%q},"target_team_id":%q,"protected":false,"display":{"can_delete":false}}`, fTeam, fOtherTeam, fService, fOtherTeam)
	})
	old := valueState(t, s, assignmentValues())
	resp := resource.ReadResponse{State: old}
	r.Read(t.Context(), resource.ReadRequest{State: old}, &resp)
	var role, target types.String
	var recipient types.Object
	resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("role_id"), &role)...)
	resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("target_team_id"), &target)...)
	resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("recipient"), &recipient)...)
	if resp.Diagnostics.HasError() || role.ValueString() != fOtherTeam || target.ValueString() != fOtherTeam || recipient.Attributes()["kind"].(types.String).ValueString() != "group" || calls != 1 {
		t.Fatalf("same UUID drift did not refresh: %v", resp.Diagnostics)
	}
}

func TestRoleAssignmentUpdateUsesSavedTupleAndSavesCommittedIntent(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		status                 int
		body                   string
		wantError, wantDesired bool
	}{
		{"self-demotion", 200, fmt.Sprintf(`{"id":%q,"role_id":%q,"recipient":{"kind":"group","id":%q},"target_team_id":%q,"protected":false}`, fTeam, fOtherTeam, fService, fOtherTeam), false, true},
		{"malformed committed", 200, `{`, true, true},
		{"incomplete committed", 200, fmt.Sprintf(`{"id":%q}`, fTeam), true, true},
		{"foreign receipt", 200, fmt.Sprintf(`{"id":%q,"role_id":%q,"recipient":{"kind":"group","id":%q},"target_team_id":%q,"protected":false}`, fService, fOtherTeam, fService, fOtherTeam), true, true},
		{"denied", 403, `{"error":{"code":"forbidden","message":"private-marker"}}`, true, false},
		{"missing", 404, `{"error":{"code":"not_found"}}`, true, false},
		{"duplicate", 409, `{"error":{"code":"conflict"}}`, true, false},
		{"stale", 412, `{"error":{"code":"revision_mismatch"}}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewRoleAssignmentResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "PUT" || q.URL.Path != "/v1/rbac/assignments/"+fTeam || q.Header.Get("If-Match") != "" || q.URL.RawQuery != "" {
					t.Error("must not read after self-demotion, replay writes or replace the UUID")
					w.WriteHeader(403)
					return
				}
				var body struct {
					client.RoleAssignmentInput
					Expected client.RoleAssignmentInput `json:"expected"`
				}
				if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				wantOld := client.RoleAssignmentInput{RoleID: fService, Recipient: client.RoleRecipient{Kind: "service_account", ID: scheduleID}}
				if !reflect.DeepEqual(body.Expected, wantOld) || body.RoleID != fOtherTeam || body.Recipient.Kind != "group" || body.Recipient.ID != fService || body.TargetTeamID == nil || *body.TargetTeamID != fOtherTeam {
					t.Error("update did not compare saved old tuple with complete desired tuple")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			old := valueState(t, s, assignmentValues())
			values := assignmentValues()
			values["id"] = types.StringUnknown()
			values["role_id"] = types.StringValue(fOtherTeam)
			values["target_team_id"] = types.StringValue(fOtherTeam)
			values["recipient"] = types.ObjectValueMust(map[string]attr.Type{"kind": types.StringType, "id": types.StringType}, map[string]attr.Value{"kind": types.StringValue("group"), "id": types.StringValue(fService)})
			p := valueState(t, s, values)
			resp := resource.UpdateResponse{State: old}
			r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			var id, role types.String
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("role_id"), &role)
			wantRole := fService
			if tc.wantDesired {
				wantRole = fOtherTeam
			}
			if resp.Diagnostics.HasError() != tc.wantError || id.ValueString() != fTeam || role.ValueString() != wantRole || calls != 1 || strings.Contains(fmt.Sprint(resp.Diagnostics), "private-marker") {
				t.Fatalf("wrong update state id=%s role=%s calls=%d diagnostics=%v", id, role, calls, resp.Diagnostics)
			}
			if !tc.wantDesired && !resp.State.Raw.Equal(old.Raw) {
				t.Fatal("failed update changed prior state")
			}
		})
	}
}

func TestRoleAssignmentReadKeepsForeignAndProtectedReceipts(t *testing.T) {
	for _, body := range []string{
		fmt.Sprintf(`{"id":%q,"role_id":%q,"recipient":{"kind":"service_account","id":%q},"target_team_id":null,"protected":false}`, fOtherTeam, fService, scheduleID),
		fmt.Sprintf(`{"id":%q,"role_id":%q,"recipient":{"kind":"service_account","id":%q},"target_team_id":null,"protected":true}`, fTeam, fService, scheduleID),
	} {
		t.Run(body, func(t *testing.T) {
			r := onpager.NewRoleAssignmentResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) { fmt.Fprint(w, body) })
			old := valueState(t, s, assignmentValues())
			resp := resource.ReadResponse{State: old}
			r.Read(t.Context(), resource.ReadRequest{State: old}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(old.Raw) {
				t.Fatal("unsafe assignment read changed state")
			}
		})
	}
}

func TestRoleAssignmentUpdateRejectsUnknownAndIncompleteTuples(t *testing.T) {
	for _, saved := range []bool{false, true} {
		for _, field := range []string{"role_id", "recipient", "target_team_id"} {
			t.Run(fmt.Sprintf("saved=%t/%s", saved, field), func(t *testing.T) {
				r := onpager.NewRoleAssignmentResource()
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) { calls++ })
				old, planned := assignmentValues(), assignmentValues()
				values := planned
				if saved {
					values = old
				}
				if field == "recipient" {
					values[field] = types.ObjectNull(map[string]attr.Type{"kind": types.StringType, "id": types.StringType})
				} else {
					values[field] = types.StringUnknown()
				}
				state, plan := valueState(t, s, old), valueState(t, s, planned)
				resp := resource.UpdateResponse{State: state}
				r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
				if !resp.Diagnostics.HasError() || calls != 0 || !resp.State.Raw.Equal(state.Raw) {
					t.Fatal("incomplete tuple reached the API or changed state")
				}
			})
		}
	}
}
func TestRoleAssignmentReadAndRetirementErrors(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		for _, destroy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", status, destroy), func(t *testing.T) {
				r := onpager.NewRoleAssignmentResource()
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
					calls++
					method := "GET"
					if destroy {
						method = "DELETE"
					}
					if q.Method != method || q.URL.Path != "/v1/rbac/assignments/"+fTeam || q.Header.Get("If-Match") != "" {
						t.Error("wrong assignment contract")
					}
					w.WriteHeader(status)
					if status == 200 {
						fmt.Fprintf(w, `{"id":%q,"role_id":%q,"recipient":{"kind":"service_account","id":%q},"target_team_id":null,"protected":false}`, fTeam, fService, scheduleID)
					} else {
						fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
					}
				})
				old := valueState(t, s, assignmentValues())
				if destroy {
					response := resource.DeleteResponse{State: old}
					r.Delete(t.Context(), resource.DeleteRequest{State: old}, &response)
					if response.Diagnostics.HasError() != (status == 403) {
						t.Fatal(response.Diagnostics)
					}
					if status == 403 && !response.State.Raw.Equal(old.Raw) {
						t.Fatal("denied deletion lost state")
					}
				} else {
					response := resource.ReadResponse{State: old}
					r.Read(t.Context(), resource.ReadRequest{State: old}, &response)
					if response.Diagnostics.HasError() != (status == 403) || response.State.Raw.IsNull() != (status == 404) {
						t.Fatal("wrong absence/denial handling")
					}
				}
				if calls != 1 {
					t.Fatal("unexpected retry or secondary read")
				}
			})
		}
	}
}
func TestRoleAssignmentCreateSavesPartialIdentity(t *testing.T) {
	for _, body := range []string{`{"id":"` + fTeam + `"}`, `{"id":"` + fTeam + `","role_id":"` + fService + `","recipient":{"kind":"service_account","id":"` + scheduleID + `"},"target_team_id":null,"protected":false}`} {
		t.Run(body, func(t *testing.T) {
			r := onpager.NewRoleAssignmentResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "POST" || q.URL.Path != "/v1/rbac/assignments" {
					t.Error("wrong create")
				}
				w.WriteHeader(201)
				fmt.Fprint(w, body)
			})
			values := assignmentValues()
			values["id"] = types.StringUnknown()
			plan := valueState(t, s, values)
			response := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &response)
			var id types.String
			response.State.GetAttribute(t.Context(), path.Root("id"), &id)
			if id.ValueString() != fTeam {
				t.Fatal("known committed identity lost")
			}
			if response.Diagnostics.HasError() != (body == `{"id":"`+fTeam+`"}`) {
				t.Fatal(response.Diagnostics)
			}
		})
	}
}

func TestRoleAssignmentDoesNotAdoptContradictoryNullScope(t *testing.T) {
	r := onpager.NewRoleAssignmentResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%q,"role_id":%q,"recipient":{"kind":"service_account","id":%q},"target_team_id":null}`, fTeam, fService, scheduleID)
	})
	values := assignmentValues()
	values["id"] = types.StringUnknown()
	values["target_team_id"] = types.StringValue(fService)
	p := valueState(t, s, values)
	response := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &response)
	if !response.Diagnostics.HasError() || !response.State.Raw.IsNull() {
		t.Fatal("explicitly contradictory tenant identity was adopted under a team scope")
	}
}
