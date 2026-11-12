package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

const silenceUUID = "77777777-7777-4777-8777-777777777777"

func silenceValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(silenceUUID), "revision": types.Int64Value(7), "scope": types.StringValue("team"), "team_ids": types.SetValueMust(types.StringType, []attr.Value{types.StringValue(fTeam), types.StringValue(fOtherTeam)}), "expression": types.StringValue("false"), "comment": types.StringValue(""), "starts_at": types.StringValue("2027-01-01T05:00:00.123456+05:00"), "ends_at": types.StringValue("2027-01-02T05:00:00.654321+05:00"), "status": types.StringValue("scheduled"), "request_timezone": types.StringNull()}
}
func silenceBody(revision int, status string) string {
	return fmt.Sprintf(`{"id":%q,"revision":%d,"scope":"team","team_ids":[%q,%q],"expression":"false","comment":"","starts_at":"2027-01-01T00:00:00.123456Z","ends_at":"2027-01-02T00:00:00.654321Z","status":%q,"cancelled_at":null}`, silenceUUID, revision, fTeam, fOtherTeam, status)
}
func TestSilenceReadDirectRetainsExpiredCancelledAndErrors(t *testing.T) {
	for _, tc := range []struct {
		status   int
		observed string
	}{{200, "expired"}, {200, "cancelled"}, {403, ""}, {404, ""}, {500, ""}} {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.observed), func(t *testing.T) {
			r := onpager.NewSilenceResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "GET" || q.URL.RequestURI() != "/v1/silences/"+silenceUUID {
					t.Error("read must use canonical direct UUID")
				}
				w.Header().Set("ETag", `"7"`)
				w.WriteHeader(tc.status)
				if tc.status == 200 {
					io.WriteString(w, silenceBody(7, tc.observed))
				} else {
					code := "forbidden"
					if tc.status == 404 {
						code = "not_found"
					}
					fmt.Fprintf(w, `{"error":{"code":%q}}`, code)
				}
			})
			state := valueState(t, s, silenceValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if tc.status == 404 {
				if !resp.State.Raw.IsNull() {
					t.Fatal("confirmed missing state retained")
				}
				return
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("existing/inaccessible object forgotten")
			}
			if tc.status != 200 {
				if !resp.Diagnostics.HasError() {
					t.Fatal("error swallowed")
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			for _, key := range []string{"starts_at", "ends_at"} {
				var v types.String
				resp.State.GetAttribute(t.Context(), path.Root(key), &v)
				if !v.Equal(silenceValues()[key]) {
					t.Fatal("instant precision/offset lost", v)
				}
			}
			var observed types.String
			resp.State.GetAttribute(t.Context(), path.Root("status"), &observed)
			if observed.ValueString() != tc.observed || calls != 1 {
				t.Fatal("passive status read", observed, calls)
			}
		})
	}
}
func TestSilenceCreatePreservesCommittedIdentityBeforeDeniedRead(t *testing.T) {
	for _, failure := range []string{"denied", "concurrent", "malformed_create"} {
		t.Run(failure, func(t *testing.T) {
			r := onpager.NewSilenceResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if calls == 1 {
					if q.Method != "POST" || q.URL.Path != "/v1/silences" {
						t.Error("create route")
					}
					w.Header().Set("ETag", `"7"`)
					w.WriteHeader(201)
					if failure == "malformed_create" {
						fmt.Fprintf(w, `{"id":%q,"revision":7}`, silenceUUID)
					} else {
						io.WriteString(w, silenceBody(7, "scheduled"))
					}
					return
				}
				if q.Method != "GET" {
					t.Error("post-write must authorized read")
				}
				if failure == "denied" {
					w.WriteHeader(403)
					io.WriteString(w, `{"error":{"code":"forbidden"}}`)
				} else {
					w.Header().Set("ETag", `"8"`)
					io.WriteString(w, silenceBody(8, "scheduled"))
				}
			})
			values := silenceValues()
			values["id"] = types.StringUnknown()
			values["revision"] = types.Int64Unknown()
			values["status"] = types.StringUnknown()
			plan := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
			temporalTestPrivate(&resp)
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("failed/concurrent read accepted")
			}
			var id types.String
			var rev types.Int64
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
			if id.ValueString() != silenceUUID || rev.ValueInt64() != 7 {
				t.Fatal("committed create orphaned", id, rev, resp.Diagnostics)
			}
		})
	}
}
func TestSilenceUpdateSavesReceiptAndInvalidatesBindings(t *testing.T) {
	for _, failure := range []string{"denied", "concurrent", "malformed_receipt", "success"} {
		t.Run(failure, func(t *testing.T) {
			r := onpager.NewSilenceResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if calls == 1 {
					if q.Method != "PATCH" || q.Header.Get("If-Match") != `"7"` {
						t.Error("own revision CAS")
					}
					var body map[string]json.RawMessage
					_ = json.NewDecoder(q.Body).Decode(&body)
					if len(body) != 5 || string(body["comment"]) != `"updated"` || !strings.Contains(string(body["team_ids"]), fOtherTeam) {
						t.Error("not whole mutable config", body)
					}
					w.Header().Set("ETag", `"8"`)
					if failure == "malformed_receipt" {
						fmt.Fprintf(w, `{"id":%q}`, silenceUUID)
					} else {
						fmt.Fprintf(w, `{"id":%q,"revision":8}`, silenceUUID)
					}
					return
				}
				if q.Method != "GET" {
					t.Error("authorized source read")
				}
				if failure == "denied" {
					w.WriteHeader(403)
					io.WriteString(w, `{"error":{"code":"forbidden"}}`)
				} else {
					rev := 8
					if failure == "concurrent" {
						rev = 9
					}
					w.Header().Set("ETag", fmt.Sprintf(`"%d"`, rev))
					io.WriteString(w, strings.Replace(silenceBody(rev, "scheduled"), `"comment":""`, `"comment":"updated"`, 1))
				}
			})
			prior := silenceValues()
			prior["starts_at"] = types.StringValue("2027-01-01T00:00:00.123456")
			state := valueState(t, s, prior)
			values := silenceValues()
			values["starts_at"] = prior["starts_at"]
			values["comment"] = types.StringValue("updated")
			values["revision"] = types.Int64Unknown()
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			temporalTestPrivate(&resp)
			resp.Private.SetKey(t.Context(), "temporal_bindings", []byte(`{"StartsAt":{"input":"2027-01-01T00:00:00.123456","response":"2027-01-01T00:00:00.123456Z"}}`))
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}, Private: resp.Private}, &resp)
			if (failure != "success") != resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var rev types.Int64
			var comment types.String
			resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
			resp.State.GetAttribute(t.Context(), path.Root("comment"), &comment)
			if comment.ValueString() != "updated" {
				t.Fatal("committed fields discarded")
			}
			if failure == "malformed_receipt" {
				if !rev.IsNull() {
					t.Fatal("untrustworthy receipt revision retained", rev)
				}
			} else if rev.ValueInt64() != 8 {
				t.Fatal("committed own revision lost", rev)
			}
			raw, d := resp.Private.GetKey(t.Context(), "temporal_bindings")
			if d.HasError() {
				t.Fatal(d)
			}
			if failure != "success" && string(raw) != "{}" {
				t.Fatal("old binding survived committed write", string(raw))
			}
		})
	}
}
func TestSilenceDeleteUsesOnlyExactDeleteAndRetainsFailures(t *testing.T) {
	for _, status := range []int{204, 403, 404, 412, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewSilenceResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "DELETE" || q.URL.Path != "/v1/silences/"+silenceUUID || q.Header.Get("If-Match") != `"7"` {
					t.Error("cancel/scan/unversioned destroy")
				}
				w.WriteHeader(status)
				if status != 204 {
					code := "forbidden"
					if status == 404 {
						code = "not_found"
					}
					fmt.Fprintf(w, `{"error":{"code":%q}}`, code)
				}
			})
			state := valueState(t, s, silenceValues())
			var resp resource.DeleteResponse
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
			if (status != 204 && status != 404) != resp.Diagnostics.HasError() || calls != 1 {
				t.Fatal("delete failure swallowed or replayed", resp.Diagnostics, calls)
			}
		})
	}
}
func TestSilenceCancelledUpdateRequiresManualReplacement(t *testing.T) {
	r := onpager.NewSilenceResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) { t.Error("cancelled object mutated implicitly") })
	values := silenceValues()
	values["status"] = types.StringValue("cancelled")
	state := valueState(t, s, values)
	plan := silenceValues()
	plan["comment"] = types.StringValue("updated")
	p := valueState(t, s, plan)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(strings.ToLower(resp.Diagnostics.Errors()[0].Detail()), "replace") {
		t.Fatal("cancel terminal guidance", resp.Diagnostics)
	}
}
func TestSilenceCanonicalValidation(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value attr.Value
	}{{"expression", types.StringValue(" false")}, {"expression", types.StringValue(strings.Repeat("я", 2049))}, {"comment", types.StringValue("trim ")}, {"comment", types.StringValue("bad\x00")}, {"starts_at", types.StringValue("2027-01-01T00:00:00.1234567Z")}, {"ends_at", types.StringValue("2026-01-01T00:00:00Z")}, {"scope", types.StringValue("tenant")}, {"team_ids", types.SetValueMust(types.StringType, []attr.Value{types.StringValue("invalid")})}} {
		t.Run(tc.key+tc.value.String(), func(t *testing.T) {
			r := onpager.NewSilenceResource()
			var sr resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
			values := silenceValues()
			values[tc.key] = tc.value
			s := valueState(t, sr.Schema, values)
			var resp resource.ValidateConfigResponse
			r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: s.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
func TestSilenceLocalTimezoneBindingsAndPlan(t *testing.T) {
	r := onpager.NewSilenceResource()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		requests++
		if q.Header.Get("Onpager-Timezone") != "Asia/Yekaterinburg" {
			t.Error("missing request timezone")
		}
		w.Header().Set("ETag", `"7"`)
		io.WriteString(w, strings.ReplaceAll(silenceBody(7, "scheduled"), "T00:00:00", "T05:00:00"))
	}))
	defer server.Close()
	c, e := client.New(server.URL, "token", client.WithTimezone("Asia/Yekaterinburg"))
	if e != nil {
		t.Fatal(e)
	}
	var configured resource.ConfigureResponse
	r.(resource.ResourceWithConfigure).Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &configured)
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	values := silenceValues()
	values["starts_at"] = types.StringValue("2027-01-01T05:00:00.123456")
	values["ends_at"] = types.StringValue("2027-01-02T05:00:00.654321")
	state := valueState(t, sr.Schema, values)
	mod := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: state.Raw}}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{State: state, Plan: mod.Plan}, &mod)
	var zone types.String
	mod.Plan.GetAttribute(t.Context(), path.Root("request_timezone"), &zone)
	if zone.ValueString() != "Asia/Yekaterinburg" {
		t.Fatal(zone, mod.Diagnostics)
	}
	var rev types.Int64
	mod.Plan.GetAttribute(t.Context(), path.Root("revision"), &rev)
	if !rev.IsUnknown() {
		t.Fatal("timezone change retained revision")
	}
	var observedStatus types.String
	mod.Plan.GetAttribute(t.Context(), path.Root("status"), &observedStatus)
	if !observedStatus.IsUnknown() {
		t.Fatal("timezone reinterpretation can change status, so status must be unknown", observedStatus)
	}
	if requests != 0 {
		t.Fatal("planning made HTTP requests")
	}
}
func TestSilenceImportUsesOnlyCanonicalUUID(t *testing.T) {
	r := onpager.NewSilenceResource()
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	state := valueState(t, sr.Schema, silenceValues())
	for _, id := range []string{silenceUUID, "team/" + silenceUUID, "invalid"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if (id != silenceUUID) != resp.Diagnostics.HasError() {
			t.Fatal("import identity", id, resp.Diagnostics)
		}
	}
}

func TestSilenceKnownInvalidTimestampNotHiddenByUnknownTeam(t *testing.T) {
	r := onpager.NewSilenceResource()
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	values := silenceValues()
	values["team_ids"] = types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()})
	values["starts_at"] = types.StringValue("2027-01-01T00:00:00.1234567Z")
	state := valueState(t, sr.Schema, values)
	var resp resource.ValidateConfigResponse
	r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("known invalid timestamp was hidden by an unrelated unknown team")
	}
}

func TestSilenceUncommittedUpdateRetainsStateAndPrivateBindings(t *testing.T) {
	for _, status := range []int{403, 412, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewSilenceResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "PATCH" || q.Header.Get("If-Match") != `"7"` {
					t.Error("incorrect failed update request")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
			})
			state := valueState(t, s, silenceValues())
			values := silenceValues()
			values["comment"] = types.StringValue("updated")
			values["revision"] = types.Int64Unknown()
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			temporalTestPrivate(&resp)
			const saved = `{"StartsAt":{"input":"2027-01-01T00:00:00.123456","response":"2027-01-01T00:00:00.123456Z"}}`
			resp.Private.SetKey(t.Context(), "temporal_bindings", []byte(saved))
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}, Private: resp.Private}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) || calls != 1 {
				t.Fatal("uncommitted failure changed or forgot state", resp.Diagnostics, calls)
			}
			raw, d := resp.Private.GetKey(t.Context(), "temporal_bindings")
			if d.HasError() || string(raw) != saved {
				t.Fatal("uncommitted failure invalidated bindings", string(raw), d)
			}
		})
	}
}
