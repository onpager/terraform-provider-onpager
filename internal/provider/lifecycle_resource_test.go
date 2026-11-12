package provider_test

import (
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

func TestActiveOnlyUpdateDoesNotRename(t *testing.T) {
	for _, kind := range []string{"teams", "services"} {
		t.Run(kind, func(t *testing.T) {
			r := onpager.NewTeamResource()
			id := fTeam
			if kind == "services" {
				r = onpager.NewServiceResource()
				id = fService
			}
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if calls == 1 {
					if req.Method != "POST" || req.URL.Path != "/v1/"+kind+"/"+id+"/reactivate" {
						t.Error("active-only update must use reactivation, not rename")
					}
					w.WriteHeader(204)
				} else {
					if req.Method != "GET" {
						t.Error("reactivation must refresh the server state")
					}
					io.WriteString(w, `{"id":"`+id+`","team_id":"`+fTeam+`","name":"Ops","active":true}`)
				}
			})
			old, desired := teamValues(id, "Ops", false), teamValues(id, "Ops", true)
			if kind == "services" {
				old["team_id"] = types.StringValue(fTeam)
				desired["team_id"] = types.StringValue(fTeam)
			}
			state, plan := valueState(t, s, old), valueState(t, s, desired)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var active types.Bool
			resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
			if !active.ValueBool() || calls != 2 {
				t.Fatal("reactivation not reflected in state")
			}
		})
	}
}

func TestInactiveCreateRetainsAllocatedIdentityWhenDisableFails(t *testing.T) {
	r := onpager.NewTeamResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"`+fTeam+`","name":"Ops","active":true}`)
		} else {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":"forbidden"}}`)
		}
	})
	v := teamValues(fTeam, "Ops", false)
	v["id"] = types.StringUnknown()
	state := valueState(t, s, v)
	var resp resource.CreateResponse
	resp.State.Schema = s
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed disable must be reported")
	}
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if id.ValueString() != fTeam {
		t.Fatal("newly allocated UUID was lost")
	}
}

func TestMembershipUsesDetailReadAndCompositeImport(t *testing.T) {
	const user = "33333333-3333-4333-8333-333333333333"
	r := onpager.NewTeamMembershipResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/teams/"+fTeam+"/members/"+user || req.Method != "GET" {
			t.Error("expected direct membership detail read")
		}
		io.WriteString(w, `{"team_id":"`+fTeam+`","user_id":"`+user+`","lead":true}`)
	})
	values := map[string]attr.Value{"id": types.StringValue(fTeam + "/" + user), "team_id": types.StringValue(fTeam), "user_id": types.StringValue(user), "lead": types.BoolValue(false)}
	state := valueState(t, s, values)
	resp := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var lead types.Bool
	resp.State.GetAttribute(t.Context(), path.Root("lead"), &lead)
	if !lead.ValueBool() {
		t.Fatal("membership lead drift was not read")
	}
	imported := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
	r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: fTeam + "/" + user}, &imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
}
