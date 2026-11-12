package provider_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

const scheduleID = "33333333-3333-4333-8333-333333333333"

func scheduleValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(scheduleID), "team_id": types.StringValue(fTeam), "name": types.StringValue("Ops"), "enabled": types.BoolValue(true), "work_calendar_id": types.StringNull(), "revision": types.Int64Value(2)}
}

func TestScheduleReadRetainsDisabledAndFailedReads(t *testing.T) {
	for _, status := range []int{200, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r := onpager.NewScheduleResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "GET" || req.URL.Path != "/v1/schedules/"+scheduleID {
					t.Error("refresh must only read direct detail")
				}
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, `{"id":"`+scheduleID+`","team_id":"`+fTeam+`","name":"Ops","enabled":false,"revision":2,"aggregate_revision":100}`)
				} else {
					io.WriteString(w, `{"error":{"code":"forbidden"}}`)
				}
			})
			state := valueState(t, s, scheduleValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() {
				t.Fatal("lost schedule state")
			}
			if status != 200 && !resp.Diagnostics.HasError() {
				t.Fatal("failure ignored")
			}
			if status == 200 {
				var enabled types.Bool
				resp.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)
				if enabled.ValueBool() {
					t.Fatal("disabled state not preserved")
				}
			}
		})
	}
}

func TestScheduleCreatePreservesIdentityWhenDisableFails(t *testing.T) {
	r := onpager.NewScheduleResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"id":"`+scheduleID+`","team_id":"`+fTeam+`","name":"Ops","enabled":true,"revision":1}`)
		} else {
			if req.Method != "POST" || req.URL.Path != "/v1/schedules/"+scheduleID+"/disable" || req.Header.Get("If-Match") != `"1"` {
				t.Error("wrong disable")
			}
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":"forbidden"}}`)
		}
	})
	values := scheduleValues()
	values["id"] = types.StringUnknown()
	values["revision"] = types.Int64Unknown()
	values["enabled"] = types.BoolValue(false)
	state := valueState(t, s, values)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("disable failure ignored")
	}
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if id.ValueString() != scheduleID {
		t.Fatal("created ID lost")
	}
	var enabled types.Bool
	resp.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)
	if !enabled.ValueBool() {
		t.Fatal("failed disable saved as success")
	}
}

func TestScheduleImportIsReadOnlyAndDirect(t *testing.T) {
	r := onpager.NewScheduleResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { t.Fatal("import must not write or read") })
	state := valueState(t, s, scheduleValues())
	resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
	r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: scheduleID}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if id.ValueString() != scheduleID {
		t.Fatal("wrong imported identity")
	}
}

func TestScheduleDisableOnlyUpdateUsesInactiveOwnerCleanup(t *testing.T) {
	r := onpager.NewScheduleResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" || req.URL.Path != "/v1/schedules/"+scheduleID+"/disable" {
			t.Error("disable-only update must use cleanup API")
		}
		io.WriteString(w, `{"id":"`+scheduleID+`","team_id":"`+fTeam+`","name":"Ops","enabled":false,"revision":3}`)
	})
	state := valueState(t, s, scheduleValues())
	values := scheduleValues()
	values["enabled"] = types.BoolValue(false)
	plan := valueState(t, s, values)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}

func TestScheduleUpdateSendsOnlyChangedSettingsAndRetainsConflicts(t *testing.T) {
	for _, status := range []int{200, 403, 412, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			r := onpager.NewScheduleResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "PATCH" || req.URL.Path != "/v1/schedules/"+scheduleID || req.Header.Get("If-Match") != `"2"` {
					t.Error("wrong metadata mutation")
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 1 || body["name"] != "Renamed" {
					t.Errorf("must omit unchanged state and children: %#v", body)
				}
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, `{"id":"`+scheduleID+`","team_id":"`+fTeam+`","name":"Renamed","enabled":false,"work_calendar_id":"`+fService+`","revision":3,"aggregate_revision":99}`)
				} else {
					io.WriteString(w, `{"error":{"code":"revision_mismatch"}}`)
				}
			})
			values := scheduleValues()
			values["enabled"] = types.BoolValue(false)
			values["work_calendar_id"] = types.StringValue(fService)
			state := valueState(t, s, values)
			values["name"] = types.StringValue("Renamed")
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if calls != 1 {
				t.Fatal("mutation retried or child collections accessed")
			}
			if status != 200 {
				if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
					t.Fatal("failed mutation changed state or lost diagnostics")
				}
			} else {
				var revision types.Int64
				resp.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
				if resp.Diagnostics.HasError() || revision.ValueInt64() != 3 {
					t.Fatal("own settings revision not saved")
				}
			}
		})
	}
}

func TestScheduleConfirmedAbsenceAndRepeatedDelete(t *testing.T) {
	r := onpager.NewScheduleResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"code":"not_found"}}`)
	})
	state := valueState(t, s, scheduleValues())
	read := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &read)
	if read.Diagnostics.HasError() || !read.State.Raw.IsNull() {
		t.Fatal("confirmed absence not removed")
	}
	del := resource.DeleteResponse{}
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &del)
	if del.Diagnostics.HasError() {
		t.Fatal(del.Diagnostics)
	}
}

func TestScheduleReadRejectsUnexpectedOwner(t *testing.T) {
	r := onpager.NewScheduleResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, `{"id":"`+scheduleID+`","team_id":"`+fOtherTeam+`","name":"Ops","enabled":true,"revision":2}`)
	})
	state := valueState(t, s, scheduleValues())
	read := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &read)
	if !read.Diagnostics.HasError() || !read.State.Raw.Equal(state.Raw) {
		t.Fatal("unexpected owner silently accepted")
	}
}

func TestScheduleCreateRetainsTrustworthyIdentityOnIncompleteSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantID     bool
		revision   int64
	}{
		{"missing_name", `{"id":"` + scheduleID + `","team_id":"` + fTeam + `","enabled":true,"revision":7}`, 201, true, 7},
		{"missing_enabled", `{"id":"` + scheduleID + `","team_id":"` + fTeam + `","name":"Ops","revision":7}`, 201, true, 7},
		{"missing_revision", `{"id":"` + scheduleID + `","team_id":"` + fTeam + `","enabled":true}`, 201, true, 0},
		{"missing_owner", `{"id":"` + scheduleID + `","enabled":true,"revision":7}`, 201, true, 7},
		{"contradictory_owner", `{"id":"` + scheduleID + `","team_id":"` + fOtherTeam + `","enabled":true,"revision":7}`, 201, false, 0},
		{"invalid_id", `{"id":"bad","team_id":"` + fTeam + `","enabled":true,"revision":7}`, 201, false, 0},
		{"forbidden", `{"id":"` + scheduleID + `","team_id":"` + fTeam + `","enabled":true,"revision":7,"error":{"code":"forbidden"}}`, 403, false, 0},
		{"server_error", `{"id":"` + scheduleID + `","team_id":"` + fTeam + `","enabled":true,"revision":7,"error":{"code":"internal_error"}}`, 500, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := onpager.NewScheduleResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			values := scheduleValues()
			values["id"] = types.StringUnknown()
			values["revision"] = types.Int64Unknown()
			values["enabled"] = types.BoolValue(false)
			values["work_calendar_id"] = types.StringValue(fService)
			plan := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("incomplete response must still return a diagnostic")
			}
			if calls != 1 {
				t.Fatal("must not retry or disable after an incomplete creation response")
			}
			if !tc.wantID {
				if !resp.State.Raw.IsNull() {
					t.Fatal("untrusted creation identity adopted")
				}
				return
			}
			var id, team, name, calendar types.String
			var enabled types.Bool
			var revision types.Int64
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("team_id"), &team)
			resp.State.GetAttribute(t.Context(), path.Root("name"), &name)
			resp.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)
			resp.State.GetAttribute(t.Context(), path.Root("work_calendar_id"), &calendar)
			resp.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
			if id.ValueString() != scheduleID || team.ValueString() != fTeam || name.ValueString() != "Ops" || calendar.ValueString() != fService || !enabled.ValueBool() {
				t.Fatal("known created identity and settings not preserved")
			}
			if tc.revision > 0 {
				if revision.ValueInt64() != tc.revision {
					t.Fatal("known own revision not preserved")
				}
			} else if !revision.IsNull() {
				t.Fatal("unknown revision invented")
			}
		})
	}
}
