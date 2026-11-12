package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func handoverValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam + "/" + fService), "team_id": types.StringValue(fTeam), "schedule_id": types.StringValue(fService), "enabled": types.BoolValue(false), "oncall_channel_ids": types.SetValueMust(types.StringType, []attr.Value{}), "revision": types.Int64Value(7)}
}

func TestHandoverCreateOwnRevisionReadUpdateAndCanonicalRetirement(t *testing.T) {
	r := onpager.NewScheduleHandoverResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.URL.Path != "/v1/teams/"+fTeam+"/schedules/"+fService+"/handover" || q.Header.Get("If-Match") != "" {
			t.Fatalf("wrong handover scope/CAS: %s", q.URL)
		}
		if q.Method == "GET" {
			fmt.Fprint(w, `{"enabled":false,"oncall_channel_ids":[],"revision":7}`)
			return
		}
		if q.Method != "PUT" {
			t.Fatal("retirement must PUT")
		}
		var payload map[string]json.RawMessage
		json.NewDecoder(q.Body).Decode(&payload)
		if len(payload) != 3 || string(payload["revision"]) != "7" || string(payload["enabled"]) != "false" || string(payload["oncall_channel_ids"]) != "[]" {
			t.Fatalf("wrong body CAS/cleanup %v", payload)
		}
		fmt.Fprint(w, `{"enabled":false,"oncall_channel_ids":[],"revision":7}`)
	})
	v := handoverValues()
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	p := valueState(t, s, v)
	created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	read := resource.ReadResponse{State: created.State}
	r.Read(t.Context(), resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() || read.State.Raw.IsNull() {
		t.Fatal(read.Diagnostics)
	}
	updated := resource.UpdateResponse{State: read.State}
	r.Update(t.Context(), resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, State: read.State}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	var deleted resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: updated.State}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
	if calls != 6 {
		t.Fatalf("unexpected CRUD calls %d", calls)
	}
}

func TestHandoverReadExplicitUnwrittenSlotOnlyConfirmsAbsence(t *testing.T) {
	for _, status := range []int{200, 403, 404, 409, 500} {
		r := onpager.NewScheduleHandoverResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			if q.Method != "GET" {
				t.Fatal("read mutated")
			}
			w.WriteHeader(status)
			io.WriteString(w, `{"enabled":false,"oncall_channel_ids":[],"revision":0}`)
		})
		state := valueState(t, s, handoverValues())
		resp := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
		if status == 200 {
			if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
				t.Fatal("unwritten slot remained")
			}
		} else if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
			t.Fatalf("HTTP%d lost state", status)
		}
	}
}

func TestHandoverConflictFailureAndMalformedWritePreserveIdentity(t *testing.T) {
	for _, status := range []int{200, 403, 404, 409, 422} {
		r := onpager.NewScheduleHandoverResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			w.WriteHeader(status)
			io.WriteString(w, `{"revision":8}`)
		})
		state := valueState(t, s, handoverValues())
		resp := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &resp)
		if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || calls != 1 {
			t.Fatal("mutation failure lost state or replayed")
		}
		if status != 200 && !resp.State.Raw.Equal(state.Raw) {
			t.Fatal("HTTP error changed state")
		}
		var id types.String
		resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
		if id.ValueString() != fTeam+"/"+fService {
			t.Fatal("committed malformed write lost identity")
		}
		var deleted resource.DeleteResponse
		r.Delete(t.Context(), resource.DeleteRequest{State: state}, &deleted)
		if !deleted.Diagnostics.HasError() {
			t.Fatal("failed retirement succeeded")
		}
	}
}

func TestHandoverUnknownInputAndImmutableIdentityDoNotWrite(t *testing.T) {
	for _, field := range []string{"enabled", "oncall_channel_ids", "schedule_id"} {
		r := onpager.NewScheduleHandoverResource()
		s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid input made request") })
		v := handoverValues()
		switch field {
		case "enabled":
			v[field] = types.BoolUnknown()
		case "oncall_channel_ids":
			v[field] = types.SetUnknown(types.StringType)
		case "schedule_id":
			v[field] = types.StringValue(fOtherTeam)
		}
		p := valueState(t, s, v)
		state := valueState(t, s, handoverValues())
		resp := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("unknown or changed identity accepted")
		}
	}
}

func TestHandoverStrictCompositeImport(t *testing.T) {
	r := onpager.NewScheduleHandoverResource()
	var s resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &s)
	for _, id := range []string{fTeam + "/" + fService, fService, fTeam + "/bad", fTeam + "/" + fService + "/extra"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s.Schema}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() == (id == fTeam+"/"+fService) {
			t.Fatalf("import %s: %v", id, resp.Diagnostics)
		}
	}
}

func TestHandoverCommittedMalformedCreateKeepsSlotForRefresh(t *testing.T) {
	r := onpager.NewScheduleHandoverResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method == "GET" {
			io.WriteString(w, `{"enabled":false,"oncall_channel_ids":[],"revision":0}`)
			return
		}
		io.WriteString(w, `{"revision":1}`)
	})
	v := handoverValues()
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	p := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
	var id types.String
	var revision types.Int64
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	resp.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
	if !resp.Diagnostics.HasError() || calls != 2 || id.ValueString() != fTeam+"/"+fService || revision.ValueInt64() != 1 {
		t.Fatal("committed slot identity/revision lost")
	}
}

func TestHandoverMalformedReadAndStoredDisabledSettings(t *testing.T) {
	for _, body := range []string{`{"enabled":false,"oncall_channel_ids":[],"revision":7}`, `{"revision":7}`, `{"enabled":false,"oncall_channel_ids":null,"revision":7}`} {
		r := onpager.NewScheduleHandoverResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			if q.Method != "GET" {
				t.Fatal("refresh mutated")
			}
			io.WriteString(w, body)
		})
		state := valueState(t, s, handoverValues())
		resp := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
		valid := body == `{"enabled":false,"oncall_channel_ids":[],"revision":7}`
		if resp.Diagnostics.HasError() == valid || !resp.State.Raw.Equal(state.Raw) {
			t.Fatal("disabled stored settings or malformed state mishandled")
		}
	}
}

func TestHandoverValidationRejectsEmptyEnabledPolicyAndUnknownMembers(t *testing.T) {
	for _, value := range []types.Set{types.SetValueMust(types.StringType, []attr.Value{}), types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()}), types.SetValueMust(types.StringType, []attr.Value{types.StringValue("bad")})} {
		r := onpager.NewScheduleHandoverResource()
		s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid selection made request") })
		v := handoverValues()
		v["enabled"] = types.BoolValue(true)
		v["oncall_channel_ids"] = value
		p := valueState(t, s, v)
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("invalid enabled settings accepted")
		}
	}
}

func TestHandoverDestroyKeepsSavedCASAfterFreshObservation(t *testing.T) {
	r := onpager.NewScheduleHandoverResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method == "GET" {
			io.WriteString(w, `{"enabled":false,"oncall_channel_ids":[],"revision":99}`)
			return
		}
		var v map[string]json.RawMessage
		json.NewDecoder(q.Body).Decode(&v)
		if q.Method != "PUT" || string(v["revision"]) != "7" || string(v["enabled"]) != "false" || string(v["oncall_channel_ids"]) != "[]" {
			t.Fatal("destroy adopted new CAS or omitted explicit cleanup")
		}
		w.WriteHeader(409)
		io.WriteString(w, `{"error":{"code":"conflict"}}`)
	})
	state := valueState(t, s, handoverValues())
	var resp resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || calls != 2 {
		t.Fatal("stale destroy was retried or succeeded")
	}
}
