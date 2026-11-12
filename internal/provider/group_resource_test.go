package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func groupValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "key": types.StringValue("ops"), "name": types.StringValue("Operations"), "description": types.StringValue("Before"), "revision": types.Int64Value(7)}
}
func groupMappingValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fService), "group_id": types.StringValue(fTeam), "provider_slug": types.StringValue("primary"), "issuer": types.StringValue("https://idp.test"), "client_id": types.StringValue("onpager"), "external_key": types.StringValue(" Ops "), "revision": types.Int64Value(3)}
}
func groupMembershipValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam + "/" + fService), "group_id": types.StringValue(fTeam), "user_id": types.StringValue(fService)}
}
func TestGroupMetadataPatchIsSparseAndUnversioned(t *testing.T) {
	r := onpager.NewGroupResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "PATCH" || req.URL.Path != "/v1/groups/"+fTeam || req.Header.Get("If-Match") != "" {
			t.Fatal("wrong group metadata request")
		}
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		if len(body) != 1 || body["description"] != "" {
			t.Fatalf("not sparse clear: %#v", body)
		}
		fmt.Fprintf(w, `{"id":%q,"key":"ops","name":"Operations","description":"","system":false,"revision":8}`, fTeam)
	})
	before := groupValues()
	after := groupValues()
	after["description"] = types.StringValue("")
	after["revision"] = types.Int64Unknown()
	state := valueState(t, s, before)
	plan := valueState(t, s, after)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var rev types.Int64
	resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
	if rev.ValueInt64() != 8 {
		t.Fatal("revision not saved")
	}
}
func TestGroupReadRemovesOnlyConfirmedAbsenceAndRejectsSystemGroup(t *testing.T) {
	for _, kind := range []string{"missing", "forbidden", "malformed", "system", "ok"} {
		t.Run(kind, func(t *testing.T) {
			r := onpager.NewGroupResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "GET" || req.URL.Path != "/v1/groups/"+fTeam {
					t.Fatal("unexpected mutation or collection scan")
				}
				switch kind {
				case "missing":
					w.WriteHeader(404)
					fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
				case "forbidden":
					w.WriteHeader(403)
					fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
				case "malformed":
					fmt.Fprint(w, `{}`)
				case "system":
					fmt.Fprintf(w, `{"id":%q,"key":"all_users","name":"All Users","description":"","system":true,"revision":1}`, fTeam)
				default:
					fmt.Fprintf(w, `{"id":%q,"key":"ops","name":"Drift","description":"","system":false,"revision":8}`, fTeam)
				}
			})
			state := valueState(t, s, groupValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if (kind == "missing") != resp.State.Raw.IsNull() {
				t.Fatal("wrong absence handling")
			}
			if (kind != "missing" && kind != "ok") != resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
		})
	}
}
func TestGroupDeleteConflictRetainsStateAndOwnRevision(t *testing.T) {
	for _, status := range []int{403, 409, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewGroupResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "DELETE" || req.Header.Get("If-Match") != `"7"` {
					t.Fatal("wrong delete CAS")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"group_in_use"}}`)
			})
			state := valueState(t, s, groupValues())
			resp := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || calls != 1 {
				t.Fatal("failure lost state or retried")
			}
		})
	}
}
func TestGroupCreatePreservesIdentityFromIncompleteSuccess(t *testing.T) {
	for _, body := range []string{`{"id":"` + fTeam + `","revision":1}`, `{"id":"` + fTeam + `","revision":"invalid"}`} {
		t.Run(body, func(t *testing.T) {
			r := onpager.NewGroupResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { fmt.Fprint(w, body) })
			values := groupValues()
			values["id"] = types.StringUnknown()
			values["revision"] = types.Int64Unknown()
			plan := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
				t.Fatal("incomplete create must retain identity with diagnostic")
			}
			var id types.String
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			if id.ValueString() != fTeam {
				t.Fatal(id)
			}
		})
	}
}
func TestGroupMembershipCreateAndRefreshUseExplicitManualSource(t *testing.T) {
	r := onpager.NewGroupMembershipResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Path != "/v1/groups/"+fTeam+"/manual-members/"+fService || req.Header.Get("If-Match") != "" {
			t.Fatal("wrong manual route")
		}
		if req.Method == "PUT" {
			w.WriteHeader(204)
			return
		}
		if req.Method != "GET" {
			t.Fatal("unexpected mutation")
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
	})
	values := groupMembershipValues()
	values["id"] = types.StringUnknown()
	plan := valueState(t, s, values)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatal(resp.Diagnostics)
	}
	read := resource.ReadResponse{State: resp.State}
	r.Read(t.Context(), resource.ReadRequest{State: resp.State}, &read)
	if !read.State.Raw.IsNull() || read.Diagnostics.HasError() {
		t.Fatal("missing manual source must not be replaced by OIDC/effective membership")
	}
	if calls != 2 {
		t.Fatal("unnecessary read or collection fallback")
	}
}
func TestGroupMappingUpdateUsesOwnCASAndRetainsFailedState(t *testing.T) {
	for _, status := range []int{200, 403, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewOIDCGroupMappingResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "PUT" || req.URL.Path != "/v1/groups/"+fTeam+"/oidc-mappings/"+fService || req.Header.Get("If-Match") != `"3"` {
					t.Fatal("wrong mapping scope or CAS")
				}
				var body map[string]any
				json.NewDecoder(req.Body).Decode(&body)
				if len(body) != 4 || body["external_key"] != " Ops2 " {
					t.Fatal(body)
				}
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprintf(w, `{"id":%q,"group_id":%q,"provider_slug":"primary","issuer":"https://idp.test","client_id":"onpager","external_key":" Ops2 ","revision":4}`, fService, fTeam)
				} else {
					fmt.Fprint(w, `{"error":{"code":"revision_mismatch"}}`)
				}
			})
			state := valueState(t, s, groupMappingValues())
			values := groupMappingValues()
			values["external_key"] = types.StringValue(" Ops2 ")
			values["revision"] = types.Int64Unknown()
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if (status != 200) != resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
				t.Fatal(resp.Diagnostics)
			}
			var got types.String
			resp.State.GetAttribute(t.Context(), path.Root("external_key"), &got)
			want := " Ops "
			if status == 200 {
				want = " Ops2 "
			}
			if got.ValueString() != want {
				t.Fatal("failed update changed state")
			}
		})
	}
}
func TestGroupResourcesImportExactIdentities(t *testing.T) {
	for _, tc := range []struct {
		r    resource.Resource
		id   string
		attr string
		want string
	}{{onpager.NewGroupResource(), fTeam, "id", fTeam}, {onpager.NewGroupMembershipResource(), fTeam + "/" + fService, "id", fTeam + "/" + fService}, {onpager.NewOIDCGroupMappingResource(), fTeam + "/" + fService, "id", fService}} {
		var sr resource.SchemaResponse
		tc.r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(t.Context()), nil)}}
		tc.r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: tc.id}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		var got types.String
		resp.State.GetAttribute(t.Context(), path.Root(tc.attr), &got)
		if got.ValueString() != tc.want {
			t.Fatal(got)
		}
		invalid := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema}}
		tc.r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: "invalid"}, &invalid)
		if !invalid.Diagnostics.HasError() {
			t.Fatal("invalid import accepted")
		}
	}
}

func TestGroupChildReadAndDeleteRetainFailures(t *testing.T) {
	for _, mapping := range []bool{false, true} {
		for _, status := range []int{200, 403, 404, 409, 412} {
			t.Run(fmt.Sprintf("mapping=%t/status=%d", mapping, status), func(t *testing.T) {
				var r resource.Resource = onpager.NewGroupMembershipResource()
				values := groupMembershipValues()
				wantPath := "/v1/groups/" + fTeam + "/manual-members/" + fService
				if mapping {
					r = onpager.NewOIDCGroupMappingResource()
					values = groupMappingValues()
					wantPath = "/v1/groups/" + fTeam + "/oidc-mappings/" + fService
				}
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					calls++
					if req.URL.Path != wantPath {
						t.Fatal("collection or wrong source requested")
					}
					if req.Method == "DELETE" {
						wantTag := ""
						if mapping {
							wantTag = `"3"`
						}
						if req.Header.Get("If-Match") != wantTag {
							t.Fatal("wrong source revision")
						}
					} else if req.Method != "GET" || req.Header.Get("If-Match") != "" {
						t.Fatal("refresh mutated")
					}
					w.WriteHeader(status)
					if status != 200 {
						fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
						return
					}
					if mapping {
						fmt.Fprintf(w, `{"id":%q,"group_id":%q,"provider_slug":"primary","issuer":"https://idp.test","client_id":"onpager","external_key":" Ops ","revision":3}`, fService, fTeam)
					} else {
						fmt.Fprintf(w, `{"group_id":%q,"user_id":%q}`, fTeam, fService)
					}
				})
				state := valueState(t, s, values)
				read := resource.ReadResponse{State: state}
				r.Read(t.Context(), resource.ReadRequest{State: state}, &read)
				if (status == 404) != read.State.Raw.IsNull() || (status != 200 && status != 404) != read.Diagnostics.HasError() {
					t.Fatal("wrong child absence/failure behavior")
				}
				del := resource.DeleteResponse{State: state}
				r.Delete(t.Context(), resource.DeleteRequest{State: state}, &del)
				if (status != 200 && status != 404) != del.Diagnostics.HasError() || del.State.Raw.IsNull() || calls != 2 {
					t.Fatal("delete failure lost state or retried")
				}
			})
		}
	}
}

func TestGroupSchemasValidateImmutableKeysAndLiteralMappingValues(t *testing.T) {
	for _, tc := range []struct {
		r       resource.Resource
		key     string
		value   types.String
		invalid bool
	}{
		{onpager.NewGroupResource(), "key", types.StringValue("all_users"), true},
		{onpager.NewGroupResource(), "key", types.StringValue("Uppercase"), true},
		{onpager.NewGroupResource(), "key", types.StringValue("ops_1-test"), false},
		{onpager.NewGroupResource(), "key", types.StringUnknown(), false},
		{onpager.NewGroupResource(), "description", types.StringNull(), false},
		{onpager.NewGroupResource(), "description", types.StringValue(""), false},
		{onpager.NewGroupResource(), "name", types.StringValue(" Padded "), true},
		{onpager.NewGroupResource(), "description", types.StringValue("nul\x00"), true},
		{onpager.NewOIDCGroupMappingResource(), "external_key", types.StringValue(" Ops "), false},
		{onpager.NewOIDCGroupMappingResource(), "external_key", types.StringValue(""), true},
		{onpager.NewOIDCGroupMappingResource(), "issuer", types.StringValue("   "), true},
		{onpager.NewOIDCGroupMappingResource(), "provider_slug", types.StringValue("invalid/path"), true},
	} {
		var sr resource.SchemaResponse
		tc.r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		a := sr.Schema.Attributes[tc.key].(schema.StringAttribute)
		var vr validator.StringResponse
		for _, v := range a.Validators {
			v.ValidateString(t.Context(), validator.StringRequest{ConfigValue: tc.value, Path: path.Root(tc.key)}, &vr)
		}
		if vr.Diagnostics.HasError() != tc.invalid {
			t.Fatalf("%s value %v diagnostics=%v", tc.key, tc.value, vr.Diagnostics)
		}
	}
	for _, tc := range []struct {
		r    resource.Resource
		keys []string
	}{
		{onpager.NewGroupResource(), []string{"key"}}, {onpager.NewGroupMembershipResource(), []string{"group_id", "user_id"}}, {onpager.NewOIDCGroupMappingResource(), []string{"group_id"}},
	} {
		var sr resource.SchemaResponse
		tc.r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		for _, key := range tc.keys {
			if len(sr.Schema.Attributes[key].(schema.StringAttribute).PlanModifiers) != 1 {
				t.Fatalf("%s must require replacement", key)
			}
		}
	}
}
