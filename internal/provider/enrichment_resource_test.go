package provider_test

import (
	"encoding/json"
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

func enrichmentRowsValue(rows ...map[string]string) types.Set {
	v := make([]attr.Value, len(rows))
	for i, row := range rows {
		m := map[string]attr.Value{}
		for k, s := range row {
			m[k] = types.StringValue(s)
		}
		v[i] = types.MapValueMust(types.StringType, m)
	}
	return types.SetValueMust(types.MapType{ElemType: types.StringType}, v)
}
func enrichmentDatasetValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "name": types.StringValue("inventory"), "description": types.StringValue("before"), "enabled": types.BoolValue(false), "rows": enrichmentRowsValue(map[string]string{"host": "a"}), "configuration_version": types.Int64Value(7), "current_revision": types.Int64Value(3)}
}
func enrichmentMetadata(w http.ResponseWriter, version, revision int, description string, detail bool) {
	fmt.Fprintf(w, `{"id":%q,"name":"inventory","description":%q,"enabled":false,"configuration_version":%d,"current_revision":%d,"has_publisher_token":false`, fTeam, description, version, revision)
	if detail {
		fmt.Fprintf(w, `,"revision":{"dataset_id":%q,"revision":%d,"row_count":1}`, fTeam, revision)
	}
	fmt.Fprint(w, `}`)
}
func TestEnrichmentDatasetAtomicCreateAndAuthorizedRows(t *testing.T) {
	r := onpager.NewEnrichmentDatasetResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		switch calls {
		case 1:
			var b map[string]any
			_ = json.NewDecoder(q.Body).Decode(&b)
			if q.Method != "POST" || q.URL.Path != "/v1/enrichment/datasets" || b["enabled"] != false {
				t.Fatal("disabled creation was not atomic")
			}
			enrichmentMetadata(w, 1, 1, "before", true)
		case 2:
			if q.Method != "GET" || q.URL.Path != "/v1/enrichment/datasets/"+fTeam {
				t.Fatal("expected authorized direct metadata read")
			}
			enrichmentMetadata(w, 1, 1, "before", true)
		case 3:
			if q.Method != "GET" || q.URL.Path != "/v1/enrichment/datasets/"+fTeam+"/revisions/1/rows" {
				t.Fatal("expected immutable row read")
			}
			fmt.Fprint(w, `{"items":[{"row_position":0,"data":{"host":"a"}}],"has_more":false}`)
		default:
			t.Fatal("unexpected operation")
		}
	})
	v := enrichmentDatasetValues()
	v["id"] = types.StringUnknown()
	v["configuration_version"] = types.Int64Unknown()
	v["current_revision"] = types.Int64Unknown()
	p := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Raw: p.Raw, Schema: s}}, &resp)
	if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || calls != 3 {
		t.Fatal(resp.Diagnostics, calls)
	}
}
func TestEnrichmentDatasetMetadataPartialSuccessUsesAdvancedCAS(t *testing.T) {
	for _, second := range []string{"conflict", "malformed", "ok"} {
		t.Run(second, func(t *testing.T) {
			r := onpager.NewEnrichmentDatasetResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if calls <= 2 {
					var b map[string]any
					_ = json.NewDecoder(q.Body).Decode(&b)
					if q.Header.Get("If-Match") != "" || b["expected_version"] != float64(6+calls) {
						t.Fatal("write did not use committed configuration_version")
					}
					if calls == 1 {
						if q.Method != "PATCH" || len(b) != 2 || b["description"] != "after" {
							t.Fatal(b)
						}
						enrichmentMetadata(w, 8, 3, "after", false)
						return
					}
					if q.Method != "POST" || q.URL.Path != "/v1/enrichment/datasets/"+fTeam+"/revisions" {
						t.Fatal("wrong publication")
					}
					if second == "conflict" {
						w.WriteHeader(409)
						fmt.Fprint(w, `{"error":{"code":"conflict"}}`)
						return
					}
					if second == "malformed" {
						fmt.Fprintf(w, `{"id":%q,"configuration_version":9,"current_revision":"bad"}`, fTeam)
						return
					}
					enrichmentMetadata(w, 9, 4, "after", true)
					return
				}
				if calls == 3 {
					enrichmentMetadata(w, 9, 4, "after", true)
					return
				}
				if calls == 4 {
					fmt.Fprint(w, `{"items":[{"row_position":0,"data":{"host":"b"}}],"has_more":false}`)
					return
				}
				t.Fatal("unexpected request")
			})
			before := enrichmentDatasetValues()
			after := enrichmentDatasetValues()
			after["description"] = types.StringValue("after")
			after["rows"] = enrichmentRowsValue(map[string]string{"host": "b"})
			after["configuration_version"] = types.Int64Unknown()
			after["current_revision"] = types.Int64Unknown()
			state, p := valueState(t, s, before), valueState(t, s, after)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Raw: p.Raw, Schema: s}}, &resp)
			if resp.Diagnostics.HasError() != (second != "ok") {
				t.Fatal(resp.Diagnostics)
			}
			var desc types.String
			var version types.Int64
			var rows types.Set
			resp.State.GetAttribute(t.Context(), path.Root("description"), &desc)
			resp.State.GetAttribute(t.Context(), path.Root("configuration_version"), &version)
			resp.State.GetAttribute(t.Context(), path.Root("rows"), &rows)
			wantVersion := int64(9)
			wantRows := after["rows"]
			if second == "conflict" {
				wantVersion = 8
				wantRows = before["rows"]
			}
			if desc.ValueString() != "after" || version.ValueInt64() != wantVersion || !rows.Equal(wantRows) {
				t.Fatalf("committed partial state lost: %s %s %s", desc, version, rows)
			}
		})
	}
}
func TestEnrichmentDatasetFollowupReadDoesNotAdoptConcurrentHead(t *testing.T) {
	r := onpager.NewEnrichmentDatasetResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if calls == 1 {
			enrichmentMetadata(w, 8, 3, "after", false)
		} else if calls == 2 {
			enrichmentMetadata(w, 9, 4, "foreign", true)
		} else {
			t.Fatal("must not read foreign rows")
		}
	})
	b, a := enrichmentDatasetValues(), enrichmentDatasetValues()
	a["description"] = types.StringValue("after")
	a["configuration_version"] = types.Int64Unknown()
	a["current_revision"] = types.Int64Unknown()
	state, p := valueState(t, s, b), valueState(t, s, a)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Raw: p.Raw, Schema: s}}, &resp)
	var version types.Int64
	var desc types.String
	resp.State.GetAttribute(t.Context(), path.Root("configuration_version"), &version)
	resp.State.GetAttribute(t.Context(), path.Root("description"), &desc)
	if !resp.Diagnostics.HasError() || calls != 2 || version.ValueInt64() != 8 || desc.ValueString() != "after" {
		t.Fatal(resp.Diagnostics, version, desc)
	}
}
func TestEnrichmentDatasetReadSeparatesDatasetAbsenceFromRowFailure(t *testing.T) {
	for _, kind := range []string{"missing", "forbidden", "rows_missing", "rows_forbidden", "ok"} {
		t.Run(kind, func(t *testing.T) {
			r := onpager.NewEnrichmentDatasetResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Fatal("read mutated")
				}
				if q.URL.Path == "/v1/enrichment/datasets/"+fTeam {
					if kind == "missing" || kind == "forbidden" {
						status := 404
						if kind == "forbidden" {
							status = 403
						}
						w.WriteHeader(status)
						fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
					} else {
						enrichmentMetadata(w, 7, 3, "before", true)
					}
					return
				}
				if kind == "rows_missing" || kind == "rows_forbidden" {
					status := 404
					if kind == "rows_forbidden" {
						status = 403
					}
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
				} else {
					fmt.Fprint(w, `{"items":[{"row_position":0,"data":{"host":"a"}}],"has_more":false}`)
				}
			})
			state := valueState(t, s, enrichmentDatasetValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() != (kind == "missing") || resp.Diagnostics.HasError() != (kind != "missing" && kind != "ok") {
				t.Fatal(resp.Diagnostics)
			}
		})
	}
}
func TestEnrichmentDatasetDeleteRetainsStateOnDependenciesOrLostGrants(t *testing.T) {
	for _, status := range []int{204, 403, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewEnrichmentDatasetResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				var b map[string]any
				_ = json.NewDecoder(q.Body).Decode(&b)
				if q.Method != "DELETE" || q.Header.Get("If-Match") != "" || b["expected_version"] != float64(7) {
					t.Fatal("wrong delete CAS")
				}
				w.WriteHeader(status)
				if status != 204 {
					fmt.Fprint(w, `{"error":{"code":"dataset_in_use"}}`)
				}
			})
			state := valueState(t, s, enrichmentDatasetValues())
			resp := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != (status != 204) || calls != 1 || status != 204 && resp.State.Raw.IsNull() {
				t.Fatal(resp.Diagnostics, calls)
			}
		})
	}
}

func TestEnrichmentDatasetUnconfirmedDeleteRetainsSavedIdentityAndVersion(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{200, ""}, {200, "{}"}, {202, ""}, {202, "{}"}} {
		t.Run(fmt.Sprintf("%d/%q", tc.status, tc.body), func(t *testing.T) {
			r := onpager.NewEnrichmentDatasetResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "DELETE" {
					t.Fatal("unexpected follow-up request")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			state := valueState(t, s, enrichmentDatasetValues())
			resp := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
			var id types.String
			var version types.Int64
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("configuration_version"), &version)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || id.ValueString() != fTeam || version.ValueInt64() != 7 || calls != 1 {
				t.Fatalf("unconfirmed deletion lost state: diagnostics=%v id=%s version=%s calls=%d", resp.Diagnostics, id, version, calls)
			}
		})
	}
}

func enrichmentRuleList(ids ...string) types.List {
	mt := types.MapType{ElemType: types.StringType}
	ot := types.ObjectType{AttrTypes: map[string]attr.Type{"id": types.StringType, "dataset_id": types.StringType, "lookup": types.ListType{ElemType: mt}, "emit": mt}}
	vals := []attr.Value{}
	for _, id := range ids {
		vals = append(vals, types.ObjectValueMust(ot.AttrTypes, map[string]attr.Value{"id": types.StringValue(id), "dataset_id": types.StringValue(fTeam), "lookup": types.ListValueMust(mt, []attr.Value{types.MapValueMust(types.StringType, map[string]attr.Value{"host": types.StringValue("labels.host")})}), "emit": types.MapValueMust(types.StringType, map[string]attr.Value{"owner": types.StringValue("owner")})}))
	}
	return types.ListValueMust(ot, vals)
}
func enrichmentRulesValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue("tenant"), "revision": types.Int64Value(7), "rules": enrichmentRuleList(fService)}
}
func TestEnrichmentRulesCreateRejectsUnownedSnapshot(t *testing.T) {
	r := onpager.NewEnrichmentRulesResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" || q.URL.Path != "/v1/enrichment/rules" {
			t.Fatal("unowned config overwritten")
		}
		fmt.Fprintf(w, `{"revision":7,"rules":[{"id":%q,"dataset_id":%q,"lookup":[{"host":"labels.host"}],"emit":{"owner":"owner"}}]}`, fService, fTeam)
	})
	v := enrichmentRulesValues()
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	p := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Raw: p.Raw, Schema: s}}, &resp)
	if !resp.Diagnostics.HasError() || calls != 1 || !resp.State.Raw.IsNull() {
		t.Fatal(resp.Diagnostics, calls)
	}
}
func TestEnrichmentRulesCreateFromZeroAndMalformedCommittedUpdate(t *testing.T) {
	r := onpager.NewEnrichmentRulesResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"revision":0,"rules":[]}`)
			return
		}
		var b map[string]json.RawMessage
		_ = json.NewDecoder(q.Body).Decode(&b)
		if string(b["expected_revision"]) != "0" {
			t.Fatal("zero CAS omitted")
		}
		fmt.Fprint(w, `{"revision":1,"rules":null}`)
	})
	v := enrichmentRulesValues()
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	p := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Raw: p.Raw, Schema: s}}, &resp)
	var id types.String
	var revision types.Int64
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	resp.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
	if !resp.Diagnostics.HasError() || id.ValueString() != "tenant" || revision.ValueInt64() != 1 || calls != 2 {
		t.Fatal(resp.Diagnostics, id, revision)
	}
}
func TestEnrichmentRulesDestroyPublishesWholeEmptySnapshotAndRetainsConflict(t *testing.T) {
	for _, status := range []int{201, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewEnrichmentRulesResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				var b map[string]json.RawMessage
				_ = json.NewDecoder(q.Body).Decode(&b)
				if q.Method != "POST" || q.URL.Path != "/v1/enrichment/rules/revisions" || string(b["rules"]) != "[]" || string(b["expected_revision"]) != "7" {
					t.Fatal("not exact aggregate clear")
				}
				w.WriteHeader(status)
				if status == 201 {
					fmt.Fprint(w, `{"revision":8,"rules":[]}`)
				} else {
					fmt.Fprint(w, `{"error":{"code":"conflict"}}`)
				}
			})
			state := valueState(t, s, enrichmentRulesValues())
			resp := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != (status == 409) || status == 409 && resp.State.Raw.IsNull() {
				t.Fatal(resp.Diagnostics)
			}
		})
	}
}
func TestEnrichmentRulesEmptySnapshotRemainsManagedAndImportLiteral(t *testing.T) {
	r := onpager.NewEnrichmentRulesResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			t.Fatal("refresh mutated")
		}
		fmt.Fprint(w, `{"revision":0,"rules":[]}`)
	})
	state := valueState(t, s, enrichmentRulesValues())
	resp := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatal(resp.Diagnostics)
	}
	for _, id := range []string{"tenant", fTeam, ""} {
		v := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &v)
		if v.Diagnostics.HasError() != (id != "tenant") {
			t.Fatal(id, v.Diagnostics)
		}
	}
}

func TestEnrichmentConfigRejectsInvalidRowsAndRulesWithoutRequests(t *testing.T) {
	for _, rows := range []types.Set{types.SetNull(types.MapType{ElemType: types.StringType}), enrichmentRowsValue(map[string]string{"x": strings.Repeat("x", 4097)}), types.SetValueMust(types.MapType{ElemType: types.StringType}, []attr.Value{types.MapNull(types.StringType)})} {
		r := onpager.NewEnrichmentDatasetResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) { t.Error("configuration validation made an HTTP request") })
		v := enrichmentDatasetValues()
		v["rows"] = rows
		state := valueState(t, s, v)
		validator, ok := r.(resource.ResourceWithValidateConfig)
		if !ok {
			t.Fatal("dataset configuration has no row validation")
		}
		var resp resource.ValidateConfigResponse
		validator.ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("invalid rows accepted")
		}
	}
	r := onpager.NewEnrichmentRulesResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) { t.Error("configuration validation made an HTTP request") })
	v := enrichmentRulesValues()
	v["rules"] = enrichmentRuleList(fService, fService)
	state := valueState(t, s, v)
	validator, ok := r.(resource.ResourceWithValidateConfig)
	if !ok {
		t.Fatal("rules configuration has no structural validation")
	}
	var resp resource.ValidateConfigResponse
	validator.ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("duplicate rule IDs accepted")
	}
}
