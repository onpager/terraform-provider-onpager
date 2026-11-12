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

func defaultsSet(ids ...string) types.Set {
	v := make([]attr.Value, len(ids))
	for i, id := range ids {
		v[i] = types.StringValue(id)
	}
	return types.SetValueMust(types.StringType, v)
}
func defaultsValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue("tenant"), "revision": types.Int64Value(7), "members": defaultsSet(fTeam), "leads": defaultsSet()}
}
func defaultsAssertState(t *testing.T, s tfsdk.State, members types.Set, revision int64) {
	t.Helper()
	var id types.String
	var got types.Set
	var rev types.Int64
	s.GetAttribute(t.Context(), path.Root("id"), &id)
	s.GetAttribute(t.Context(), path.Root("members"), &got)
	s.GetAttribute(t.Context(), path.Root("revision"), &rev)
	if s.Raw.IsNull() || id.ValueString() != "tenant" || !got.Equal(members) || revision == 0 && !rev.IsNull() || revision > 0 && rev.ValueInt64() != revision {
		t.Fatalf("policy state lost intent/identity/revision: id=%s members=%s revision=%s", id, got, rev)
	}
}

// Catches accidental adoption/overwrite of release-seeded or UI-owned defaults.
func TestTeamRoleDefaultsCreateRejectsNonemptyUnownedPolicy(t *testing.T) {
	for _, field := range []string{"members", "leads"} {
		t.Run(field, func(t *testing.T) {
			r := onpager.NewTeamRoleDefaultsResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "GET" || q.URL.Path != "/v1/rbac/team-defaults" {
					t.Error("unowned policy mutated or discovered through UI/catalog")
				}
				body := map[string]any{"members": []string{}, "leads": []string{}, "revision": 7}
				body[field] = []string{fTeam}
				w.Header().Set("ETag", `"7"`)
				_ = json.NewEncoder(w).Encode(body)
			})
			v := defaultsValues()
			v["id"], v["revision"] = types.StringUnknown(), types.Int64Unknown()
			p := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() || calls != 1 {
				t.Fatalf("unowned policy adopted: %v calls=%d", resp.Diagnostics, calls)
			}
		})
	}
}

func TestTeamRoleDefaultsCreateUsesGuardRevisionAndAdoptsEmptyReadOnly(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			r := onpager.NewTeamRoleDefaultsResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if calls == 1 {
					if q.Method != "GET" || q.Header.Get("If-Match") != "" {
						t.Error("empty adoption must start with an authorized read")
					}
					w.Header().Set("ETag", `"7"`)
					fmt.Fprint(w, `{"members":[],"leads":[],"revision":7}`)
					return
				}
				if empty || calls != 2 || q.Method != "PUT" || q.Header.Get("If-Match") != `"7"` {
					t.Error("create did not use the saved guard revision exactly once")
				}
				w.Header().Set("ETag", `"8"`)
				fmt.Fprintf(w, `{"members":[%q],"leads":[],"revision":8}`, fTeam)
			})
			v := defaultsValues()
			v["id"], v["revision"] = types.StringUnknown(), types.Int64Unknown()
			wantRevision := int64(8)
			if empty {
				v["members"] = defaultsSet()
				wantRevision = 7
			}
			p := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if resp.Diagnostics.HasError() || (!empty && calls != 2) || (empty && calls != 1) {
				t.Fatalf("create failed: %v calls=%d", resp.Diagnostics, calls)
			}
			defaultsAssertState(t, resp.State, v["members"].(types.Set), wantRevision)
		})
	}
}

// A new GET before PUT would hide a stale plan and clobber concurrent changes.
func TestTeamRoleDefaultsUpdateUsesOnlySavedCASAndRetainsErrors(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 412, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamRoleDefaultsResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				var b map[string]json.RawMessage
				_ = json.NewDecoder(q.Body).Decode(&b)
				if calls != 1 || q.Method != "PUT" || q.URL.Path != "/v1/rbac/team-defaults" || q.Header.Get("If-Match") != `"7"` || len(b) != 2 || string(b["members"]) != "[]" || string(b["leads"]) != "[]" {
					t.Error("update lost whole-policy saved CAS or performed extra read")
				}
				w.Header().Set("ETag", `"8"`)
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, `{"members":[],"leads":[],"revision":8}`)
				} else {
					fmt.Fprint(w, `{"error":{"code":"conflict"}}`)
				}
			})
			before := valueState(t, s, defaultsValues())
			v := defaultsValues()
			v["members"], v["revision"] = defaultsSet(), types.Int64Unknown()
			p := valueState(t, s, v)
			resp := resource.UpdateResponse{State: before}
			r.Update(t.Context(), resource.UpdateRequest{State: before, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if resp.Diagnostics.HasError() != (status != 200) || calls != 1 {
				t.Fatal(resp.Diagnostics, calls)
			}
			if status == 200 {
				defaultsAssertState(t, resp.State, defaultsSet(), 8)
			} else {
				defaultsAssertState(t, resp.State, defaultsSet(fTeam), 7)
			}
		})
	}
}

func TestTeamRoleDefaultsReadPreservesSingletonOnEveryHTTPErrorAndReadsDrift(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamRoleDefaultsResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.Header.Get("If-Match") != "" {
					t.Error("refresh mutated policy")
				}
				w.Header().Set("ETag", `"11"`)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"members":[],"leads":[],"revision":11}`)
			})
			before := valueState(t, s, defaultsValues())
			resp := resource.ReadResponse{State: before}
			r.Read(t.Context(), resource.ReadRequest{State: before}, &resp)
			if resp.Diagnostics.HasError() != (status != 200) {
				t.Fatal(resp.Diagnostics)
			}
			if status == 200 {
				defaultsAssertState(t, resp.State, defaultsSet(), 11)
			} else {
				defaultsAssertState(t, resp.State, defaultsSet(fTeam), 7)
			}
		})
	}
}

func TestTeamRoleDefaultsCommittedMalformedWritesSaveDesiredPolicy(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete"} {
		for _, badTag := range []bool{false, true} {
			t.Run(fmt.Sprint(operation, badTag), func(t *testing.T) {
				r := onpager.NewTeamRoleDefaultsResource()
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
					calls++
					if operation == "create" && calls == 1 {
						w.Header().Set("ETag", `"7"`)
						fmt.Fprint(w, `{"members":[],"leads":[],"revision":7}`)
						return
					}
					if q.Method != "PUT" || q.Header.Get("If-Match") != `"7"` {
						t.Error("malformed write recovery rebased or lost saved revision")
					}
					if !badTag {
						w.Header().Set("ETag", `"8"`)
					}
					fmt.Fprint(w, `{"revision":8,"members":"invalid","leads":null}`)
				})
				beforeValues := defaultsValues()
				if operation == "delete" {
					beforeValues["leads"] = defaultsSet(fService)
				}
				before := valueState(t, s, beforeValues)
				v := defaultsValues()
				v["members"], v["revision"] = defaultsSet(fService), types.Int64Unknown()
				p := valueState(t, s, v)
				var got tfsdk.State
				switch operation {
				case "create":
					resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
					r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
					if !resp.Diagnostics.HasError() || calls != 2 {
						t.Fatal(resp.Diagnostics, calls)
					}
					got = resp.State
				case "update":
					resp := resource.UpdateResponse{State: before}
					r.Update(t.Context(), resource.UpdateRequest{State: before, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
					if !resp.Diagnostics.HasError() || calls != 1 {
						t.Fatal(resp.Diagnostics, calls)
					}
					got = resp.State
				case "delete":
					resp := resource.DeleteResponse{State: before}
					r.Delete(t.Context(), resource.DeleteRequest{State: before}, &resp)
					if !resp.Diagnostics.HasError() || calls != 1 {
						t.Fatal(resp.Diagnostics, calls)
					}
					got = resp.State
				}
				want := defaultsSet(fService)
				if operation == "delete" {
					want = defaultsSet()
				}
				revision := int64(8)
				if badTag {
					revision = 0
				}
				defaultsAssertState(t, got, want, revision)
				var savedLeads types.Set
				got.GetAttribute(t.Context(), path.Root("leads"), &savedLeads)
				if !savedLeads.Equal(defaultsSet()) {
					t.Fatal("committed intent retained preceding lead grants", savedLeads)
				}
			})
		}
	}
}

func TestTeamRoleDefaultsDeleteClearsBothSetsWithSavedCAS(t *testing.T) {
	for _, status := range []int{200, 403, 404, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamRoleDefaultsResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				var b map[string]json.RawMessage
				_ = json.NewDecoder(q.Body).Decode(&b)
				if q.Method != "PUT" || q.Header.Get("If-Match") != `"7"` || len(b) != 2 || string(b["members"]) != "[]" || string(b["leads"]) != "[]" {
					t.Error("destroy did not publish exact empty policy using saved CAS")
				}
				w.Header().Set("ETag", `"8"`)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"revision":8,"members":[],"leads":[]}`)
			})
			before := valueState(t, s, defaultsValues())
			resp := resource.DeleteResponse{State: before}
			r.Delete(t.Context(), resource.DeleteRequest{State: before}, &resp)
			if resp.Diagnostics.HasError() != (status != 200) || calls != 1 {
				t.Fatal(resp.Diagnostics, calls)
			}
			if status != 200 {
				defaultsAssertState(t, resp.State, defaultsSet(fTeam), 7)
			}
		})
	}
}

func TestTeamRoleDefaultsImportOnlyLiteralTenantWithoutRequests(t *testing.T) {
	r := onpager.NewTeamRoleDefaultsResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("import mutated or discovered policy") })
	for _, id := range []string{"tenant", fTeam, "Tenant", "", " tenant"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() != (id != "tenant") {
			t.Fatal(id, resp.Diagnostics)
		}
	}
}

func TestTeamRoleDefaultsNullUnknownAndInvalidApplyInputsNeverRequest(t *testing.T) {
	for _, field := range []string{"members", "leads"} {
		for _, v := range []types.Set{types.SetNull(types.StringType), types.SetUnknown(types.StringType), types.SetValueMust(types.StringType, []attr.Value{types.StringNull()}), types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()}), defaultsSet("invalid")} {
			r := onpager.NewTeamRoleDefaultsResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("invalid/unknown apply input reached API") })
			values := defaultsValues()
			values[field] = v
			p := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid apply input accepted", field, v)
			}
		}
	}
	for _, rev := range []types.Int64{types.Int64Null(), types.Int64Unknown(), types.Int64Value(0), types.Int64Value(-1)} {
		r := onpager.NewTeamRoleDefaultsResource()
		s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("missing current revision reached API") })
		values := defaultsValues()
		values["revision"] = rev
		before := valueState(t, s, values)
		resp := resource.DeleteResponse{State: before}
		r.Delete(t.Context(), resource.DeleteRequest{State: before}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("invalid delete revision accepted", rev)
		}
	}
}

// Known invalid elements must fail during planning, while genuine unknown
// references may be resolved by Terraform before the apply-time guard.
func TestTeamRoleDefaultsUUIDSetValidation(t *testing.T) {
	r := onpager.NewTeamRoleDefaultsResource()
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	for _, field := range []string{"members", "leads"} {
		attribute := schemaResponse.Schema.Attributes[field].(schema.SetAttribute)
		if len(attribute.Validators) == 0 {
			t.Fatal("UUID set has no plan-time validation")
		}
		for _, tc := range []struct {
			value   types.Set
			invalid bool
		}{
			{defaultsSet(), false},
			{defaultsSet(fTeam), false},
			{defaultsSet(fTeam, fService), false},
			{types.SetUnknown(types.StringType), false},
			{types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()}), false},
			{types.SetNull(types.StringType), true},
			{types.SetValueMust(types.StringType, []attr.Value{types.StringNull()}), true},
			{defaultsSet("invalid"), true},
			{defaultsSet("00000000-0000-0000-0000-000000000000"), true},
			{defaultsSet("AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"), true},
		} {
			response := validator.SetResponse{}
			for _, rule := range attribute.Validators {
				rule.ValidateSet(t.Context(), validator.SetRequest{Path: path.Root(field), ConfigValue: tc.value}, &response)
			}
			if response.Diagnostics.HasError() != tc.invalid {
				t.Fatal(field, tc.value, response.Diagnostics)
			}
		}
	}
}
