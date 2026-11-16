package provider_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

var routingSettingsTargetTypes = map[string]attr.Type{"target_kind": types.StringType, "target_id": types.StringType}

func routingSettingsValues(target bool) map[string]attr.Value {
	route := types.ObjectNull(routingSettingsTargetTypes)
	if target {
		route = types.ObjectValueMust(routingSettingsTargetTypes, map[string]attr.Value{"target_kind": types.StringValue("team"), "target_id": types.StringValue(fTeam)})
	}
	return map[string]attr.Value{"id": types.StringValue("tenant"), "default_route": route}
}

func TestRoutingSettingsResourceReadErrorsNeverRemoveSingleton(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewRoutingSettingsResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.URL.RequestURI() != "/v1/settings/routing" {
					t.Error("refresh must only direct-read singleton")
				}
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, `{"default_route":null}`)
				} else {
					fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
				}
			})
			state := valueState(t, s, routingSettingsValues(true))
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() || resp.Diagnostics.HasError() != (status != 200) {
				t.Fatal("singleton absence/error handling", resp.Diagnostics)
			}
		})
	}
}

func TestRoutingSettingsResourceCreateRequiresImportOfNonemptyRoute(t *testing.T) {
	r := onpager.NewRoutingSettingsResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" {
			t.Error("create must not overwrite unowned route")
		}
		fmt.Fprintf(w, `{"default_route":{"target_kind":"team","target_id":%q}}`, fTeam)
	})
	plan := valueState(t, s, routingSettingsValues(true))
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || calls != 1 {
		t.Fatal("unowned route adopted/overwritten")
	}
}

func TestRoutingSettingsResourceCommittedMalformedWritesRetainIntent(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprint(create), func(t *testing.T) {
			r := onpager.NewRoutingSettingsResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method == "GET" {
					fmt.Fprint(w, `{"default_route":null}`)
					return
				}
				if q.Method != "PATCH" || q.Header.Get("If-Match") != "" {
					t.Error("wrong mutation/precondition")
				}
				fmt.Fprint(w, `{}`)
			})
			state := valueState(t, s, routingSettingsValues(false))
			plan := valueState(t, s, routingSettingsValues(true))
			result := state
			failed := false
			if create {
				resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
				r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
				result, failed = resp.State, resp.Diagnostics.HasError()
			} else {
				resp := resource.UpdateResponse{State: state}
				r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
				result, failed = resp.State, resp.Diagnostics.HasError()
			}
			var id types.String
			var route types.Object
			result.GetAttribute(t.Context(), path.Root("id"), &id)
			result.GetAttribute(t.Context(), path.Root("default_route"), &route)
			wantCalls := 1
			if create {
				wantCalls = 2
			}
			if !failed || id.ValueString() != "tenant" || route.IsNull() || calls != wantCalls {
				t.Fatal("committed write was lost/replayed")
			}
		})
	}
}

func TestRoutingSettingsResourceHTTPFailuresKeepPreviousState(t *testing.T) {
	for _, status := range []int{403, 404, 409, 412, 500} {
		r := onpager.NewRoutingSettingsResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
		})
		state := valueState(t, s, routingSettingsValues(true))
		plan := valueState(t, s, routingSettingsValues(false))
		update := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &update)
		if !update.Diagnostics.HasError() || !update.State.Raw.Equal(state.Raw) {
			t.Fatal("failed update lost state")
		}
		retire := resource.DeleteResponse{State: state}
		r.Delete(t.Context(), resource.DeleteRequest{State: state}, &retire)
		if !retire.Diagnostics.HasError() || retire.State.Raw.IsNull() || calls != 2 {
			t.Fatal("failed destroy was retried or forgot state")
		}
	}
}

func TestRoutingSettingsResourceEmptyCreateAndImportDoNotWrite(t *testing.T) {
	r := onpager.NewRoutingSettingsResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			t.Error("empty create/import should not mutate")
		}
		fmt.Fprint(w, `{"default_route":null}`)
	})
	plan := valueState(t, s, routingSettingsValues(false))
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatal("empty singleton was not owned", resp.Diagnostics)
	}
	for _, id := range []string{"tenant", "other", fTeam} {
		imported := resource.ImportStateResponse{State: valueState(t, s, routingSettingsValues(false))}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &imported)
		if imported.Diagnostics.HasError() != (id != "tenant") {
			t.Fatal("invalid singleton import accepted")
		}
	}
}
