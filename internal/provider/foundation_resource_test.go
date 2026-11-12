package provider_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

const (
	fTeam      = "11111111-1111-4111-8111-111111111111"
	fService   = "22222222-2222-4222-8222-222222222222"
	fOtherTeam = "44444444-4444-4444-8444-444444444444"
)

func configuredResource(t *testing.T, r resource.Resource, handler http.HandlerFunc) schema.Schema {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := client.New(s.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	var cr resource.ConfigureResponse
	r.(resource.ResourceWithConfigure).Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &cr)
	if cr.Diagnostics.HasError() {
		t.Fatal(cr.Diagnostics)
	}
	return sr.Schema
}

func valueState(t *testing.T, s schema.Schema, values map[string]attr.Value) tfsdk.State {
	t.Helper()
	attributeTypes := make(map[string]attr.Type)
	for name, value := range values {
		attributeTypes[name] = value.Type(t.Context())
	}
	obj := types.ObjectValueMust(attributeTypes, values)
	raw, err := obj.ToTerraformValue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return tfsdk.State{Schema: s, Raw: raw}
}

func teamValues(id, name string, active bool) map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(id), "name": types.StringValue(name), "active": types.BoolValue(active)}
}

func TestFoundationResourcesAreRegistered(t *testing.T) {
	p := onpager.New("test")()
	registered := map[string]bool{}
	for _, constructor := range p.Resources(t.Context()) {
		var metadata resource.MetadataResponse
		constructor().Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "onpager"}, &metadata)
		registered[metadata.TypeName] = true
	}
	for _, name := range []string{"onpager_team", "onpager_service"} {
		if !registered[name] {
			t.Errorf("resource missing from provider schema: %s", name)
		}
	}
}

func TestTeamReadRetainsDisabledObjectAndAuthorizationFailures(t *testing.T) {
	for _, status := range []int{200, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r := onpager.NewTeamResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, `{"id":"`+fTeam+`","name":"Ops","active":false}`)
				} else {
					io.WriteString(w, `{"error":{"code":"forbidden"}}`)
				}
			})
			state := valueState(t, s, teamValues(fTeam, "Ops", true))
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() {
				t.Fatal("disabled or inaccessible object was removed from state")
			}
			if status != 200 && !resp.Diagnostics.HasError() {
				t.Fatal("HTTP failure was swallowed")
			}
			if status == 200 {
				var active types.Bool
				resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
				if active.ValueBool() {
					t.Fatal("inactive status was not refreshed")
				}
			}
		})
	}
}

func TestServiceUpdatePreservesSuccessfulTransferWhenRenameFails(t *testing.T) {
	r := onpager.NewServiceResource()
	requests := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		requests++
		if requests == 1 {
			if req.Method != "POST" || req.URL.Path != "/v1/services/"+fService+"/transfer" || req.Header.Get("If-Match") != "" {
				t.Error("transfer must run first without If-Match")
			}
			io.WriteString(w, `{"id":"`+fService+`"}`)
		} else {
			if req.Method != "PATCH" || req.Header.Get("If-Match") != "" {
				t.Error("rename must not send If-Match after transfer")
			}
			w.WriteHeader(409)
			io.WriteString(w, `{"error":{"code":"name_conflict"}}`)
		}
	})
	values := teamValues(fService, "Checkout", true)
	values["team_id"] = types.StringValue(fTeam)
	state := valueState(t, s, values)
	planned := teamValues(fService, "Payments", true)
	planned["team_id"] = types.StringValue(fOtherTeam)
	planState := valueState(t, s, planned)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: planState.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("rename conflict must be reported")
	}
	var got types.String
	resp.State.GetAttribute(t.Context(), path.Root("team_id"), &got)
	if got.ValueString() != fOtherTeam {
		t.Fatal("successful transfer was not preserved in state")
	}
	resp.State.GetAttribute(t.Context(), path.Root("name"), &got)
	if got.ValueString() != "Checkout" {
		t.Fatal("failed rename must not be saved as applied")
	}
	if requests != 2 {
		t.Fatalf("unexpected retries/requests: %d", requests)
	}
}

func TestDestroyReturnsFailureInsteadOfForgetting(t *testing.T) {
	r := onpager.NewTeamResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" || req.URL.Path != "/v1/teams/"+fTeam+"/disable" {
			t.Error("destroy must disable through API")
		}
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"code":"forbidden"}}`)
	})
	state := valueState(t, s, teamValues(fTeam, "Ops", true))
	var resp resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed disable must prevent Terraform from forgetting the object")
	}
}

func TestImportRejectsInvalidIDs(t *testing.T) {
	for _, r := range []resource.Resource{onpager.NewTeamResource(), onpager.NewServiceResource()} {
		var sr resource.SchemaResponse
		r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		var resp resource.ImportStateResponse
		resp.State.Schema = sr.Schema
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: "not-an-id"}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("invalid import ID accepted")
		}
	}
}
