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

const rotationID = "55555555-5555-4555-8555-555555555555"

var recurrenceTypes = map[string]attr.Type{"every": types.Int64Type, "unit": types.StringType}

func declarationResource(primary bool) resource.Resource {
	if primary {
		return onpager.NewScheduleRotationResource()
	}
	return onpager.NewScheduleOverrideResource()
}
func declarationKind(primary bool) string {
	if primary {
		return "primary"
	}
	return "override"
}
func declarationValues(primary bool) map[string]attr.Value {
	v := map[string]attr.Value{"id": types.StringValue(rotationID), "schedule_id": types.StringValue(scheduleID), "revision": types.Int64Value(7), "request_timezone": types.StringNull(), "starts_at": types.StringValue("2027-01-01T05:00:00+05:00"), "ends_at": types.StringValue("2027-01-02T05:00:00+05:00")}
	if primary {
		v["effective_from"] = types.StringValue("2027-01-01T17:00:00+05:00")
		v["user_ids"] = types.ListValueMust(types.StringType, []attr.Value{types.StringValue(fTeam), types.StringValue(fService)})
		v["recurrence"] = types.ObjectValueMust(recurrenceTypes, map[string]attr.Value{"every": types.Int64Value(1), "unit": types.StringValue("days")})
		v["weekdays"] = types.SetNull(types.StringType)
		v["max_shift_duration_seconds"] = types.Int64Null()
	} else {
		v["user_id"] = types.StringValue(fTeam)
	}
	return v
}
func declarationJSON(primary bool) string {
	config := fmt.Sprintf(`{"starts_at":"2027-01-01T00:00:00Z","ends_at":"2027-01-02T00:00:00Z","user_id":%q}`, fTeam)
	if primary {
		config = fmt.Sprintf(`{"starts_at":"2027-01-01T00:00:00Z","ends_at":"2027-01-02T00:00:00Z","user_ids":[%q,%q],"recurrence":{"every":1,"unit":"days"},"weekdays":null,"max_shift_duration_seconds":null}`, fTeam, fService)
	}
	effective := "2027-01-01T00:00:00Z"
	if primary {
		effective = "2027-01-01T12:00:00Z"
	}
	return fmt.Sprintf(`{"id":%q,"schedule_id":%q,"kind":%q,"revision":8,"aggregate_revision":456,"effective_from":%q,"configuration":%s}`, rotationID, scheduleID, declarationKind(primary), effective, config)
}
func TestScheduleDeclarationReadUsesStableScopedReadAndPreservesEquivalentTimestamps(t *testing.T) {
	for _, primary := range []bool{true, false} {
		t.Run(declarationKind(primary), func(t *testing.T) {
			r := declarationResource(primary)
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "GET" || req.URL.RequestURI() != "/v1/schedules/"+scheduleID+"/"+declarationKind(primary)+"-rotations/"+rotationID+"/declaration" {
					t.Error("refresh accessed parent, sibling, or occurrence API")
				}
				io.WriteString(w, declarationJSON(primary))
			})
			values := declarationValues(primary)
			state := valueState(t, s, values)
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			for _, key := range []string{"starts_at", "ends_at"} {
				var got types.String
				resp.State.GetAttribute(t.Context(), path.Root(key), &got)
				if !got.Equal(values[key]) {
					t.Fatalf("%s lost configured offset: %v", key, got)
				}
			}
			if primary {
				var got types.String
				resp.State.GetAttribute(t.Context(), path.Root("effective_from"), &got)
				if !got.Equal(values["effective_from"]) {
					t.Fatal("effective boundary lost configured offset")
				}
			}
			var revision types.Int64
			resp.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
			if revision.ValueInt64() != 8 || calls != 1 {
				t.Fatal("child revision not retained independently")
			}
		})
	}
}
func TestScheduleDeclarationReadFailureRetainsState(t *testing.T) {
	for _, primary := range []bool{true, false} {
		for _, tc := range []struct {
			name   string
			status int
			body   string
			absent bool
		}{
			{"forbidden", 403, `{"error":{"code":"forbidden"}}`, false}, {"server", 500, `{"error":{"code":"internal_error"}}`, false}, {"confirmed_absence", 404, `{"error":{"code":"not_found"}}`, true}, {"ambiguous_404", 404, `{"error":{"code":"forbidden"}}`, false}, {"malformed", 200, `{"id":"bad"}`, false}, {"foreign_owner", 200, strings.Replace(declarationJSON(primary), scheduleID, fOtherTeam, 1), false}, {"foreign_id", 200, strings.Replace(declarationJSON(primary), rotationID, fOtherTeam, 1), false},
		} {
			t.Run(declarationKind(primary)+"/"+tc.name, func(t *testing.T) {
				r := declarationResource(primary)
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) })
				state := valueState(t, s, declarationValues(primary))
				resp := resource.ReadResponse{State: state}
				r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
				if tc.absent {
					if !resp.State.Raw.IsNull() || resp.Diagnostics.HasError() {
						t.Fatal("confirmed absence not removed")
					}
				} else if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
					t.Fatalf("failed read changed state: %v", resp.Diagnostics)
				}
			})
		}
	}
}
func TestScheduleDeclarationCreateSavesIdentityBeforeFailedRead(t *testing.T) {
	for _, primary := range []bool{true, false} {
		for _, status := range []int{403, 200} {
			t.Run(fmt.Sprintf("%s/%d", declarationKind(primary), status), func(t *testing.T) {
				r := declarationResource(primary)
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					calls++
					if calls == 1 {
						if req.Method != "POST" || req.URL.Path != "/v1/schedules/"+scheduleID+"/"+declarationKind(primary)+"-rotations" {
							t.Error("wrong create API")
						}
						fmt.Fprintf(w, `{"rotation_id":%q,"revision":3,"aggregate_revision":99}`, rotationID)
					} else {
						w.WriteHeader(status)
						io.WriteString(w, `{"error":{"code":"forbidden"}}`)
					}
				})
				values := declarationValues(primary)
				values["id"] = types.StringUnknown()
				values["revision"] = types.Int64Unknown()
				plan := valueState(t, s, values)
				resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
				r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
				var id types.String
				var rev types.Int64
				resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
				resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
				if !resp.Diagnostics.HasError() || id.ValueString() != rotationID || rev.ValueInt64() != 3 || calls != 2 {
					t.Fatalf("committed creation orphaned: %v %v %v calls=%d", resp.Diagnostics, id, rev, calls)
				}
			})
		}
	}
}
func TestScheduleDeclarationCreateRejectsUntrustedReceipts(t *testing.T) {
	for _, primary := range []bool{true, false} {
		for _, tc := range []struct {
			name   string
			status int
			body   string
			retain bool
		}{
			{"missing_revision", 201, fmt.Sprintf(`{"rotation_id":%q,"aggregate_revision":99}`, rotationID), true},
			{"bad_id", 201, `{"rotation_id":"bad","revision":3,"aggregate_revision":99}`, false},
			{"error_body", 403, fmt.Sprintf(`{"rotation_id":%q,"revision":3,"aggregate_revision":99,"error":{"code":"forbidden"}}`, rotationID), false},
		} {
			t.Run(declarationKind(primary)+"/"+tc.name, func(t *testing.T) {
				r := declarationResource(primary)
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					calls++
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				})
				values := declarationValues(primary)
				values["id"] = types.StringUnknown()
				values["revision"] = types.Int64Unknown()
				plan := valueState(t, s, values)
				resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
				r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
				if !resp.Diagnostics.HasError() || calls != 1 {
					t.Fatal("invalid receipt ignored or retried")
				}
				if !tc.retain {
					if !resp.State.Raw.IsNull() {
						t.Fatal("untrusted identity adopted")
					}
					return
				}
				var id types.String
				var rev types.Int64
				resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
				resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
				if id.ValueString() != rotationID || !rev.IsNull() {
					t.Fatal("known identity lost or unknown revision invented")
				}
			})
		}
	}
}
func TestScheduleDeclarationUpdateUsesOwnRevisionAndSavesCommittedPlan(t *testing.T) {
	for _, primary := range []bool{true, false} {
		for _, status := range []int{403, 412, 200} {
			t.Run(fmt.Sprintf("%s/%d", declarationKind(primary), status), func(t *testing.T) {
				r := declarationResource(primary)
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					calls++
					if calls == 1 {
						if req.Method != "PUT" || !strings.HasSuffix(req.URL.Path, "/"+rotationID+"/declaration") || req.Header.Get("If-Match") != `"7"` {
							t.Error("update did not use declaration and own CAS")
						}
						var body map[string]any
						if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						config, ok := body["configuration"].(map[string]any)
						if !ok || config["ends_at"] != "2027-01-03T05:00:00+05:00" {
							t.Error("complete desired rule omitted")
						}
						if primary && body["effective_from"] != "2027-01-01T17:00:00+05:00" {
							t.Error("publication boundary changed")
						}
						w.WriteHeader(status)
						if status == 200 {
							fmt.Fprintf(w, `{"rotation_id":%q,"revision":8,"aggregate_revision":888}`, rotationID)
						} else {
							io.WriteString(w, `{"error":{"code":"revision_mismatch"}}`)
						}
					} else {
						w.WriteHeader(403)
						io.WriteString(w, `{"error":{"code":"forbidden"}}`)
					}
				})
				state := valueState(t, s, declarationValues(primary))
				values := declarationValues(primary)
				values["ends_at"] = types.StringValue("2027-01-03T05:00:00+05:00")
				values["revision"] = types.Int64Unknown()
				plan := valueState(t, s, values)
				resp := resource.UpdateResponse{State: state}
				r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
				if !resp.Diagnostics.HasError() {
					t.Fatal("error not reported")
				}
				if status != 200 {
					if calls != 1 || !resp.State.Raw.Equal(state.Raw) {
						t.Fatal("failed update changed state or retried")
					}
					return
				}
				var rev types.Int64
				var ends types.String
				resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
				resp.State.GetAttribute(t.Context(), path.Root("ends_at"), &ends)
				if calls != 2 || rev.ValueInt64() != 8 || ends.ValueString() != "2027-01-03T05:00:00+05:00" {
					t.Fatal("committed desired configuration not persisted")
				}
			})
		}
	}
}
func TestScheduleDeclarationDeleteUsesAllCoverageAndOwnCAS(t *testing.T) {
	for _, primary := range []bool{true, false} {
		for _, status := range []int{200, 204, 403, 412, 404} {
			t.Run(fmt.Sprintf("%s/%d", declarationKind(primary), status), func(t *testing.T) {
				r := declarationResource(primary)
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					calls++
					if req.Method != "DELETE" || req.URL.RequestURI() != "/v1/schedules/"+scheduleID+"/"+declarationKind(primary)+"-rotations/"+rotationID+"?mode=all" || req.Header.Get("If-Match") != `"7"` {
						t.Error("destroy touched wrong coverage or revision")
					}
					w.WriteHeader(status)
					if status == 200 {
						fmt.Fprintf(w, `{"rotation_id":%q,"revision":8,"aggregate_revision":999}`, rotationID)
					} else if status != 204 {
						io.WriteString(w, `{"error":{"code":"not_found"}}`)
					}
				})
				state := valueState(t, s, declarationValues(primary))
				resp := resource.DeleteResponse{State: state}
				r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
				if (status == 204 || status == 403 || status == 412) != resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
				if calls != 1 || (resp.Diagnostics.HasError() && !resp.State.Raw.Equal(state.Raw)) {
					t.Fatal("failed retirement lost state or retried")
				}
			})
		}
	}
}
func TestScheduleDeclarationCompositeImportDoesNotAccessAPI(t *testing.T) {
	for _, primary := range []bool{true, false} {
		for _, id := range []string{scheduleID + "/" + rotationID, rotationID, scheduleID + "/bad"} {
			t.Run(declarationKind(primary)+"/"+id, func(t *testing.T) {
				r := declarationResource(primary)
				s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("import made API request") })
				resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)}}
				r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
				if id != scheduleID+"/"+rotationID {
					if !resp.Diagnostics.HasError() {
						t.Fatal("invalid composite import accepted")
					}
					return
				}
				var owner, child types.String
				resp.State.GetAttribute(t.Context(), path.Root("schedule_id"), &owner)
				resp.State.GetAttribute(t.Context(), path.Root("id"), &child)
				if resp.Diagnostics.HasError() || owner.ValueString() != scheduleID || child.ValueString() != rotationID {
					t.Fatal("wrong imported identities")
				}
			})
		}
	}
}
func TestScheduleDeclarationValidationBeforeWrites(t *testing.T) {
	cases := []struct {
		name    string
		primary bool
		key     string
		value   attr.Value
		valid   bool
	}{
		{"year_zero", true, "effective_from", types.StringValue("0000-01-01T00:00:00Z"), false},
		{"utc_year_underflow", true, "starts_at", types.StringValue("0001-01-01T00:00:00+05:00"), false},
		{"utc_year_overflow", true, "effective_from", types.StringValue("9999-12-31T23:59:59-05:00"), false},
		{"fractional_start", true, "starts_at", types.StringValue("2027-01-01T00:00:00.1Z"), false},
		{"zero_fraction", true, "starts_at", types.StringValue("2027-01-01T00:00:00.000Z"), true},
		{"local_clock", false, "starts_at", types.StringValue("2027-01-01T00:00:00"), true},
		{"local_effective_boundary", true, "effective_from", types.StringValue("2027-01-01T12:00:00"), true},
		{"local_zero_fraction", true, "starts_at", types.StringValue("2027-01-01T00:00:00.000000000000"), true},
		{"local_fractional_precision", false, "starts_at", types.StringValue("2027-01-01T00:00:00.1"), false},
		{"local_year_zero", true, "effective_from", types.StringValue("0000-01-01T00:00:00"), false},
		{"backwards", false, "ends_at", types.StringValue("2026-12-31T00:00:00Z"), false},
		{"unbounded", true, "ends_at", types.StringNull(), true},
		{"empty_end", true, "ends_at", types.StringValue(""), false},
		{"empty_users", true, "user_ids", types.ListValueMust(types.StringType, []attr.Value{}), false},
		{"duplicate_users", true, "user_ids", types.ListValueMust(types.StringType, []attr.Value{types.StringValue(fTeam), types.StringValue(fTeam)}), false},
		{"bad_user", false, "user_id", types.StringValue("bad"), false},
		{"empty_weekdays", true, "weekdays", types.SetValueMust(types.StringType, []attr.Value{}), false},
		{"bad_weekday", true, "weekdays", types.SetValueMust(types.StringType, []attr.Value{types.StringValue("Monday")}), false},
		{"zero_max", true, "max_shift_duration_seconds", types.Int64Value(0), false},
		{"zero_recurrence", true, "recurrence", types.ObjectValueMust(recurrenceTypes, map[string]attr.Value{"every": types.Int64Value(0), "unit": types.StringValue("days")}), false},
		{"large_recurrence", true, "recurrence", types.ObjectValueMust(recurrenceTypes, map[string]attr.Value{"every": types.Int64Value(2147483648), "unit": types.StringValue("days")}), false},
		{"bad_unit", true, "recurrence", types.ObjectValueMust(recurrenceTypes, map[string]attr.Value{"every": types.Int64Value(1), "unit": types.StringValue("years")}), false},
		{"independent_boundary", true, "effective_from", types.StringValue("2020-01-01T00:00:00Z"), true},
		{"fractional_boundary", true, "effective_from", types.StringValue("2027-01-01T00:00:00.1Z"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := declarationResource(tc.primary)
			writes := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { writes++; w.WriteHeader(500) })
			values := declarationValues(tc.primary)
			values[tc.key] = tc.value
			state := valueState(t, s, values)
			resp := resource.ValidateConfigResponse{}
			r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, &resp)
			if tc.valid == resp.Diagnostics.HasError() {
				t.Fatalf("valid=%t diagnostics=%v", tc.valid, resp.Diagnostics)
			}
			if tc.valid {
				return
			}
			create := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &create)
			update := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &update)
			if !create.Diagnostics.HasError() || !update.Diagnostics.HasError() || writes != 0 {
				t.Fatal("invalid configuration reached API")
			}
		})
	}
}

func TestScheduleDeclarationSuccessfulMutationsPreserveConfiguredValues(t *testing.T) {
	for _, primary := range []bool{true, false} {
		t.Run(declarationKind(primary), func(t *testing.T) {
			r := declarationResource(primary)
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method == "GET" {
					body := declarationJSON(primary)
					if primary {
						body = strings.Replace(body, `"ends_at":"2027-01-02T00:00:00Z"`, `"ends_at":null`, 1)
						body = strings.Replace(body, `"weekdays":null`, `"weekdays":["MO","FR"]`, 1)
						body = strings.Replace(body, `"max_shift_duration_seconds":null`, `"max_shift_duration_seconds":3600`, 1)
					}
					io.WriteString(w, body)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				config := body
				if req.Method == "PUT" {
					if req.Header.Get("If-Match") != `"8"` {
						t.Error("successful refresh revision not used for next write")
					}
					var ok bool
					config, ok = body["configuration"].(map[string]any)
					if !ok {
						t.Fatal("missing complete declaration")
					}
				}
				if primary {
					users, ok := config["user_ids"].([]any)
					if !ok || len(users) != 2 || users[0] != fTeam || users[1] != fService {
						t.Error("ordered participants changed")
					}
					if ends, present := config["ends_at"]; !present || ends != nil {
						t.Error("unbounded end must be explicitly null")
					}
					if body["effective_from"] != "2027-01-01T17:00:00+05:00" {
						t.Error("publication boundary conflated with recurrence anchor")
					}
				}
				fmt.Fprintf(w, `{"rotation_id":%q,"revision":8,"aggregate_revision":1000}`, rotationID)
			})
			values := declarationValues(primary)
			if primary {
				values["ends_at"] = types.StringNull()
				values["weekdays"] = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("FR"), types.StringValue("MO")})
				values["max_shift_duration_seconds"] = types.Int64Value(3600)
			}
			desired := valueState(t, s, values)
			values["id"] = types.StringUnknown()
			values["revision"] = types.Int64Unknown()
			plan := valueState(t, s, values)
			create := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &create)
			if create.Diagnostics.HasError() {
				t.Fatal(create.Diagnostics)
			}
			values["id"] = types.StringValue(rotationID)
			values["revision"] = types.Int64Value(8)
			expected := valueState(t, s, values)
			if !create.State.Raw.Equal(expected.Raw) {
				t.Fatal("configured timestamps or optional fields changed on create")
			}
			update := resource.UpdateResponse{State: create.State}
			r.Update(t.Context(), resource.UpdateRequest{State: create.State, Plan: tfsdk.Plan{Schema: s, Raw: desired.Raw}}, &update)
			if update.Diagnostics.HasError() || !update.State.Raw.Equal(expected.Raw) || calls != 4 {
				t.Fatalf("round trip changed desired values: calls=%d diagnostics=%v", calls, update.Diagnostics)
			}
		})
	}
}

func TestScheduleDeclarationMutationReadRejectsConcurrentRevision(t *testing.T) {
	for _, primary := range []bool{true, false} {
		for _, create := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/create=%t", declarationKind(primary), create), func(t *testing.T) {
				r := declarationResource(primary)
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					calls++
					if req.Method == "GET" {
						io.WriteString(w, strings.Replace(declarationJSON(primary), `"revision":8`, `"revision":9`, 1))
						return
					}
					fmt.Fprintf(w, `{"rotation_id":%q,"revision":8,"aggregate_revision":99}`, rotationID)
				})
				state := valueState(t, s, declarationValues(primary))
				var result tfsdk.State
				var failed bool
				if create {
					response := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
					r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &response)
					result = response.State
					failed = response.Diagnostics.HasError()
				} else {
					response := resource.UpdateResponse{State: state}
					temporalTestPrivate(&response)
					response.Private.SetKey(t.Context(), "temporal_bindings", []byte(`{"StartsAt":{"input":"2027-01-01T00:00:00","response":"2027-01-01T00:00:00Z"}}`))
					r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &response)
					result = response.State
					failed = response.Diagnostics.HasError()
					raw, d := response.Private.GetKey(t.Context(), "temporal_bindings")
					var bindings map[string]any
					if d.HasError() || json.Unmarshal(raw, &bindings) != nil || len(bindings) != 0 {
						t.Fatal("committed update retained old timestamp bindings after concurrent GET", string(raw))
					}
				}
				var revision types.Int64
				result.GetAttribute(t.Context(), path.Root("revision"), &revision)
				if !failed || calls != 2 || revision.ValueInt64() != 8 {
					t.Fatalf("concurrent read adopted after write: failed=%t calls=%d revision=%v", failed, calls, revision)
				}
			})
		}
	}
}

func TestScheduleLocalInputLifecycleTimezoneAndDrift(t *testing.T) {
	for _, primary := range []bool{true, false} {
		t.Run(declarationKind(primary), func(t *testing.T) {
			r := declarationResource(primary)
			zone := "Europe/Moscow"
			output := strings.ReplaceAll(declarationJSON(primary), "2027-01-01T00:00:00Z", "2027-01-01T03:00:00+03:00")
			output = strings.ReplaceAll(output, "2027-01-02T00:00:00Z", "2027-01-02T03:00:00+03:00")
			output = strings.ReplaceAll(output, "2027-01-01T12:00:00Z", "2027-01-01T15:00:00+03:00")
			mutations, reads := 0, 0
			reject := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if len(req.Header.Values("Onpager-Timezone")) != 1 || req.Header.Get("Onpager-Timezone") != zone {
					t.Error("request did not use selected timezone")
				}
				if req.Method == "GET" {
					reads++
					io.WriteString(w, output)
					return
				}
				mutations++
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				config := body
				if req.Method == "PUT" {
					config = body["configuration"].(map[string]any)
					if req.Header.Get("If-Match") != `"8"` && !reject {
						t.Error("update lost child revision")
					}
				}
				if config["starts_at"] != "2027-01-01T03:00:00" || config["ends_at"] != "2027-01-02T03:00:00" {
					t.Error("local timestamp payload converted", config)
				}
				if primary && body["effective_from"] != "2027-01-01T15:00:00" {
					t.Error("effective boundary converted or inferred")
				}
				if reject {
					w.WriteHeader(422)
					io.WriteString(w, `{"error":{"code":"ambiguous_local_time","field":"starts_at","details":{"time_zone":"Europe/Berlin","candidates":["2026-10-25T02:30:00+02:00","2026-10-25T02:30:00+01:00"]}}}`)
					return
				}
				revision := 8
				if req.Method == "PUT" {
					revision = 9
					output = strings.ReplaceAll(declarationJSON(primary), `"revision":8`, `"revision":9`)
					output = strings.ReplaceAll(output, "2027-01-01T00:00:00Z", "2027-01-01T03:00:00Z")
					output = strings.ReplaceAll(output, "2027-01-02T00:00:00Z", "2027-01-02T03:00:00Z")
					output = strings.ReplaceAll(output, "2027-01-01T12:00:00Z", "2027-01-01T15:00:00Z")
				}
				fmt.Fprintf(w, `{"rotation_id":%q,"revision":%d,"aggregate_revision":99}`, rotationID, revision)
			}))
			defer server.Close()
			configureZone := func() {
				c, err := client.New(server.URL, "fixture-token", client.WithTimezone(zone))
				if err != nil {
					t.Fatal(err)
				}
				var response resource.ConfigureResponse
				r.(resource.ResourceWithConfigure).Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &response)
				if response.Diagnostics.HasError() {
					t.Fatal(response.Diagnostics)
				}
			}
			configureZone()
			var schema resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
			values := declarationValues(primary)
			values["starts_at"] = types.StringValue("2027-01-01T03:00:00")
			values["ends_at"] = types.StringValue("2027-01-02T03:00:00")
			values["request_timezone"] = types.StringValue("Europe/Moscow")
			if primary {
				values["effective_from"] = types.StringValue("2027-01-01T15:00:00")
			}
			plan := valueState(t, schema.Schema, values)
			create := resource.CreateResponse{State: tfsdk.State{Schema: schema.Schema, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
			temporalTestPrivate(&create)
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema.Schema, Raw: plan.Raw}}, &create)
			if create.Diagnostics.HasError() {
				t.Fatal(create.Diagnostics)
			}
			assertLocal := func(state tfsdk.State) {
				for _, key := range []string{"starts_at", "ends_at"} {
					var got types.String
					state.GetAttribute(t.Context(), path.Root(key), &got)
					if !got.Equal(values[key]) {
						t.Fatalf("%s local value lost: %v", key, got)
					}
				}
				if primary {
					var got types.String
					state.GetAttribute(t.Context(), path.Root("effective_from"), &got)
					if !got.Equal(values["effective_from"]) {
						t.Fatal("local effective_from lost", got)
					}
				}
			}
			assertLocal(create.State)
			zone = "UTC"
			configureZone()
			output = declarationJSON(primary)
			read := resource.ReadResponse{State: create.State, Private: create.Private}
			r.Read(t.Context(), resource.ReadRequest{State: create.State, Private: create.Private}, &read)
			if read.Diagnostics.HasError() {
				t.Fatal(read.Diagnostics)
			}
			assertLocal(read.State)
			modify := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: schema.Schema, Raw: read.State.Raw}}
			r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{State: read.State, Plan: modify.Plan}, &modify)
			var planned types.String
			modify.Plan.GetAttribute(t.Context(), path.Root("request_timezone"), &planned)
			if modify.Diagnostics.HasError() || planned.ValueString() != "UTC" || mutations != 1 {
				t.Fatal("timezone change did not produce a mutation-free local plan", modify.Diagnostics, planned)
			}
			update := resource.UpdateResponse{State: read.State, Private: read.Private}
			r.Update(t.Context(), resource.UpdateRequest{State: read.State, Plan: modify.Plan, Private: read.Private}, &update)
			if update.Diagnostics.HasError() {
				t.Fatal(update.Diagnostics)
			}
			assertLocal(update.State)
			// Import adopts explicit server values; it has no configured local binding.
			importValues := declarationValues(primary)
			importValues["starts_at"] = types.StringNull()
			importValues["ends_at"] = types.StringNull()
			if primary {
				importValues["effective_from"] = types.StringNull()
			}
			importedState := valueState(t, schema.Schema, importValues)
			imported := resource.ReadResponse{State: importedState}
			r.Read(t.Context(), resource.ReadRequest{State: importedState}, &imported)
			var importedStart types.String
			imported.State.GetAttribute(t.Context(), path.Root("starts_at"), &importedStart)
			if imported.Diagnostics.HasError() || importedStart.ValueString() != "2027-01-01T03:00:00Z" {
				t.Fatal("import did not adopt authoritative offset", imported.Diagnostics, importedStart)
			}
			reject = true
			failed := resource.UpdateResponse{State: update.State, Private: update.Private}
			r.Update(t.Context(), resource.UpdateRequest{State: update.State, Plan: modify.Plan, Private: update.Private}, &failed)
			if !failed.Diagnostics.HasError() || !failed.State.Raw.Equal(update.State.Raw) || !strings.Contains(failed.Diagnostics.Errors()[0].Detail(), "ambiguous_local_time") {
				t.Fatal("backend DST rejection was lost or changed state", failed.Diagnostics)
			}
			output = strings.ReplaceAll(output, "2027-01-01T03:00:00Z", "2027-01-01T04:00:00Z")
			drift := resource.ReadResponse{State: update.State, Private: update.Private}
			r.Read(t.Context(), resource.ReadRequest{State: update.State, Private: update.Private}, &drift)
			var start types.String
			drift.State.GetAttribute(t.Context(), path.Root("starts_at"), &start)
			if drift.Diagnostics.HasError() || start.ValueString() != "2027-01-01T04:00:00Z" || mutations != 3 || reads != 5 {
				t.Fatalf("real drift hidden or mutation retried: %v start=%v mutations=%d reads=%d", drift.Diagnostics, start, mutations, reads)
			}
		})
	}
}
