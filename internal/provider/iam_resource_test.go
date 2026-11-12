package provider_test

import (
	"fmt"
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

func iamAccountValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "display_name": types.StringValue("Automation"), "owner_team_id": types.StringNull(), "active": types.BoolValue(true)}
}
func iamRoleValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "key": types.StringValue("automation"), "name": types.StringValue("Automation"), "description": types.StringValue(""), "scope_type": types.StringValue("tenant"), "permission_keys": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("teams.read")}), "revision": types.Int64Value(7)}
}
func TestIAMReadsKeepFailuresAndOnlyRemoveConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		constructor func() resource.Resource
		values      func() map[string]attr.Value
		route       string
	}{
		{"account", onpager.NewServiceAccountResource, iamAccountValues, "/v1/service-accounts/" + fTeam},
		{"role", onpager.NewRBACRoleResource, iamRoleValues, "/v1/rbac/roles/" + fTeam},
	} {
		for _, status := range []int{403, 404, 412, 200} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				r := tc.constructor()
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					if req.Method != "GET" || req.URL.Path != tc.route || req.URL.RawQuery != "" {
						t.Error("refresh must only direct-read")
					}
					w.WriteHeader(status)
					if status == 200 {
						fmt.Fprint(w, `{"malformed":true}`)
					} else {
						fmt.Fprint(w, `{"error":{"code":"not_found","message":"test-token-secret"}}`)
					}
				})
				state := valueState(t, s, tc.values())
				resp := resource.ReadResponse{State: state}
				r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
				if (status == 404) != resp.State.Raw.IsNull() {
					t.Fatal("state removal must require confirmed absence")
				}
				if status != 404 && !resp.Diagnostics.HasError() {
					t.Fatal("failure/malformed read swallowed")
				}
				for _, d := range resp.Diagnostics {
					if strings.Contains(d.Detail(), "test-token-secret") {
						t.Fatal("secret diagnostic")
					}
				}
			})
		}
	}
}

func TestIAMAccountCreateSavesIdentityBeforeDeactivateFailure(t *testing.T) {
	r := onpager.NewServiceAccountResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprintf(w, `{"id":%q,"display_name":"Automation","owner_team_id":null,"active":true}`, fTeam)
			return
		}
		if req.Method != "POST" || req.URL.Path != "/v1/service-accounts/"+fTeam+"/deactivate" {
			t.Error("wrong state command")
		}
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
	})
	values := iamAccountValues()
	values["id"] = types.StringUnknown()
	values["active"] = types.BoolValue(false)
	plan := valueState(t, s, values)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || calls != 2 {
		t.Fatal("successful creation forgotten or repeated")
	}
	var id types.String
	var active types.Bool
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
	if id.ValueString() != fTeam || !active.ValueBool() {
		t.Fatal("partial success not persisted")
	}
}

func TestIAMAccountUpdateSavesMetadataBeforeActiveFailure(t *testing.T) {
	r := onpager.NewServiceAccountResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if calls == 1 {
			if req.Method != "PUT" {
				t.Error("metadata must update first")
			}
			fmt.Fprintf(w, `{"id":%q,"display_name":"Renamed","owner_team_id":%q,"active":true}`, fTeam, fService)
			return
		}
		w.WriteHeader(409)
		fmt.Fprint(w, `{"error":{"code":"conflict"}}`)
	})
	state := valueState(t, s, iamAccountValues())
	values := iamAccountValues()
	values["display_name"] = types.StringValue("Renamed")
	values["owner_team_id"] = types.StringValue(fService)
	values["active"] = types.BoolValue(false)
	plan := valueState(t, s, values)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	var name types.String
	resp.State.GetAttribute(t.Context(), path.Root("display_name"), &name)
	if !resp.Diagnostics.HasError() || name.ValueString() != "Renamed" || calls != 2 {
		t.Fatal("committed metadata forgotten")
	}
}

func TestIAMAccountDestroyReadsInactiveAndNeverReactivates(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			r := onpager.NewServiceAccountResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if calls == 1 {
					if req.Method != "GET" {
						t.Error("must confirm inactive state")
					}
					fmt.Fprintf(w, `{"id":%q,"display_name":"Automation","owner_team_id":null,"active":%t}`, fTeam, active)
					return
				}
				if req.Method != "POST" || !strings.HasSuffix(req.URL.Path, "/deactivate") {
					t.Error("wrong retirement")
				}
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
			})
			state := valueState(t, s, iamAccountValues())
			resp := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
			if active != resp.Diagnostics.HasError() || (active && (calls != 2 || resp.State.Raw.IsNull())) || (!active && calls != 1) {
				t.Fatal("incorrect retirement behavior")
			}
		})
	}
}

func TestIAMRoleUpdateAndDestroyUseOwnRevision(t *testing.T) {
	for _, status := range []int{403, 409, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewRBACRoleResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Header.Get("If-Match") != `"7"` {
					t.Error("wrong role revision")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"revision_mismatch"}}`)
			})
			state := valueState(t, s, iamRoleValues())
			values := iamRoleValues()
			values["name"] = types.StringValue("Renamed")
			plan := valueState(t, s, values)
			update := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &update)
			var name types.String
			update.State.GetAttribute(t.Context(), path.Root("name"), &name)
			if !update.Diagnostics.HasError() || name.ValueString() != "Automation" || calls != 1 {
				t.Fatal("failed update modified or retried")
			}
			del := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &del)
			if !del.Diagnostics.HasError() || del.State.Raw.IsNull() || calls != 2 {
				t.Fatal("failed deletion forgot role")
			}
		})
	}
}

func TestIAMImportsAndUnknownApplyInputs(t *testing.T) {
	for _, r := range []resource.Resource{onpager.NewServiceAccountResource(), onpager.NewRBACRoleResource()} {
		var sr resource.SchemaResponse
		r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		for _, id := range []string{fTeam, "automation", "00000000-0000-0000-0000-000000000000", strings.ToUpper(fOtherTeam)} {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(t.Context()), nil)}}
			r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
			// fOtherTeam contains no letters; uppercase remains a valid UUID.
			valid := id == fTeam || id == fOtherTeam
			if valid == resp.Diagnostics.HasError() {
				t.Fatal("incorrect UUID import validation")
			}
		}
	}
	r := onpager.NewServiceAccountResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { calls++; w.WriteHeader(500) })
	values := iamAccountValues()
	values["owner_team_id"] = types.StringUnknown()
	plan := valueState(t, s, values)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || calls != 0 {
		t.Fatal("unknown owner sent as null")
	}
}

func TestIAMRoleCreateReceiptKeepsIdentityAndOwnRevision(t *testing.T) {
	r := onpager.NewRBACRoleResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("ETag", `"11"`)
		fmt.Fprintf(w, `{"id":%q,"revision":11}`, fTeam)
	})
	values := iamRoleValues()
	values["id"] = types.StringUnknown()
	values["revision"] = types.Int64Unknown()
	plan := valueState(t, s, values)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	var id types.String
	var revision types.Int64
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	resp.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
	if !resp.Diagnostics.HasError() || id.ValueString() != fTeam || revision.ValueInt64() != 11 {
		t.Fatal("known created role receipt was lost")
	}
}

func TestIAMSuccessfulReadsAndRoleReplaceState(t *testing.T) {
	t.Run("inactive account", func(t *testing.T) {
		r := onpager.NewServiceAccountResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
			fmt.Fprintf(w, `{"id":%q,"display_name":"Automation","owner_team_id":%q,"active":false}`, fTeam, fService)
		})
		state := valueState(t, s, iamAccountValues())
		resp := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
		var active types.Bool
		resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
		if resp.Diagnostics.HasError() || active.ValueBool() || resp.State.Raw.IsNull() {
			t.Fatal("inactive account not retained")
		}
	})
	t.Run("role empty draft replace", func(t *testing.T) {
		r := onpager.NewRBACRoleResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
			calls++
			if req.Method != "PUT" || req.Header.Get("If-Match") != `"7"` {
				t.Error("wrong role replace")
			}
			w.Header().Set("ETag", `"8"`)
			fmt.Fprintf(w, `{"id":%q,"key":"automation","name":"Renamed","description":"","scope_type":"tenant","permission_keys":[],"builtin":false,"revision":8}`, fTeam)
		})
		state := valueState(t, s, iamRoleValues())
		values := iamRoleValues()
		values["name"] = types.StringValue("Renamed")
		values["permission_keys"] = types.SetValueMust(types.StringType, []attr.Value{})
		plan := valueState(t, s, values)
		resp := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
		var revision types.Int64
		var permissions types.Set
		resp.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
		resp.State.GetAttribute(t.Context(), path.Root("permission_keys"), &permissions)
		if resp.Diagnostics.HasError() || calls != 1 || revision.ValueInt64() != 8 || permissions.IsNull() || len(permissions.Elements()) != 0 {
			t.Fatal("empty draft or role revision lost")
		}
	})
}

func TestIAMRoleIdentityChangesAndUnknownPermissionsRejectBeforeWrite(t *testing.T) {
	for _, field := range []string{"key", "scope_type", "permission_keys"} {
		t.Run(field, func(t *testing.T) {
			r := onpager.NewRBACRoleResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { calls++; w.WriteHeader(500) })
			state := valueState(t, s, iamRoleValues())
			values := iamRoleValues()
			switch field {
			case "key":
				values[field] = types.StringValue("changed")
			case "scope_type":
				values[field] = types.StringValue("team")
			case "permission_keys":
				values[field] = types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()})
			}
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || calls != 0 {
				t.Fatal("invalid update sent to API")
			}
		})
	}
}

func TestIAMAccountCreateReconcilesReturnedActiveState(t *testing.T) {
	for _, desired := range []bool{false, true} {
		t.Run(fmt.Sprint(desired), func(t *testing.T) {
			r := onpager.NewServiceAccountResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if calls == 1 {
					fmt.Fprintf(w, `{"id":%q,"display_name":"Automation","owner_team_id":null,"active":%t}`, fTeam, !desired)
					return
				}
				command := "deactivate"
				if desired {
					command = "reactivate"
				}
				if req.Method != "POST" || req.URL.Path != "/v1/service-accounts/"+fTeam+"/"+command {
					t.Error("wrong active-state command")
				}
				w.WriteHeader(204)
			})
			values := iamAccountValues()
			values["active"] = types.BoolValue(desired)
			plan := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			var active types.Bool
			resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
			if resp.Diagnostics.HasError() || active.ValueBool() != desired || calls != 2 {
				t.Fatal("desired active state not saved")
			}
		})
	}
}
