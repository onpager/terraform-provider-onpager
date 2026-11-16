package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

func responderGuardValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "team_id": types.StringValue(fTeam), "max_open_requests_per_subject": types.Int64Value(4), "revision": types.Int64Value(7)}
}

func responderGuardAssertState(t *testing.T, s tfsdk.State, limit, revision int64) {
	t.Helper()
	var id, team types.String
	var max, rev types.Int64
	s.GetAttribute(t.Context(), path.Root("id"), &id)
	s.GetAttribute(t.Context(), path.Root("team_id"), &team)
	s.GetAttribute(t.Context(), path.Root("max_open_requests_per_subject"), &max)
	s.GetAttribute(t.Context(), path.Root("revision"), &rev)
	if s.Raw.IsNull() || id.ValueString() != fTeam || team.ValueString() != fTeam || max.ValueInt64() != limit || revision == 0 && !rev.IsNull() || revision > 0 && rev.ValueInt64() != revision {
		t.Fatalf("guard policy lost identity/content/revision: %s %s %s %s", id, team, max, rev)
	}
}

func TestTeamResponderGuardPolicyCreateRequiresUnownedDefault(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			r := onpager.NewTeamResponderGuardPolicyResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.URL.Path != "/v1/teams/"+fTeam+"/responder-guard-policies" || q.URL.RawQuery != "" || q.Header.Get("If-Match") != "" {
					t.Error("wrong guard scope or invented CAS")
				}
				if calls == 1 {
					if q.Method != "GET" {
						t.Error("create did not guard unowned override")
					}
					if explicit {
						fmt.Fprint(w, `{"max_open_requests_per_subject":4,"revision":7,"is_default":false}`)
					} else {
						fmt.Fprint(w, `{"max_open_requests_per_subject":357,"revision":0,"is_default":true}`)
					}
					return
				}
				var body map[string]json.RawMessage
				_ = json.NewDecoder(q.Body).Decode(&body)
				if explicit || calls != 2 || q.Method != "POST" || len(body) != 1 || string(body["max_open_requests_per_subject"]) != "4" {
					t.Error("create adopted foreign content or sent wrong publication")
				}
				w.WriteHeader(201)
				fmt.Fprint(w, `{"max_open_requests_per_subject":4,"revision":8,"is_default":false}`)
			})
			values := responderGuardValues()
			values["id"], values["revision"] = types.StringUnknown(), types.Int64Unknown()
			p := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if explicit {
				if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() || calls != 1 || !strings.Contains(fmt.Sprint(resp.Diagnostics), "import") {
					t.Fatal("explicit unowned override silently adopted", resp.Diagnostics, calls)
				}
			} else {
				if resp.Diagnostics.HasError() || calls != 2 {
					t.Fatal(resp.Diagnostics, calls)
				}
				responderGuardAssertState(t, resp.State, 4, 8)
			}
		})
	}
}

func TestTeamResponderGuardPolicyReadOnlyValidatedDefaultIsAbsence(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          int
		body            string
		absent, invalid bool
	}{
		{"explicit drift", 200, `{"max_open_requests_per_subject":9,"revision":11,"is_default":false}`, false, false},
		{"evolving default", 200, `{"max_open_requests_per_subject":357,"revision":0,"is_default":true}`, true, false},
		{"default mismatch", 200, `{"max_open_requests_per_subject":357,"revision":11,"is_default":true}`, false, true},
		{"missing default flag", 200, `{"max_open_requests_per_subject":4,"revision":0}`, false, true},
		{"missing max", 200, `{"revision":0,"is_default":true}`, false, true},
		{"denied", 403, `{"error":{"code":"forbidden"}}`, false, true},
		{"not found", 404, `{"error":{"code":"not_found"}}`, false, true},
		{"unauthorized", 401, ``, false, true},
		{"failure", 500, ``, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewTeamResponderGuardPolicyResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "GET" || q.Header.Get("If-Match") != "" {
					t.Error("refresh mutated")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			before := valueState(t, s, responderGuardValues())
			resp := resource.ReadResponse{State: before}
			r.Read(t.Context(), resource.ReadRequest{State: before}, &resp)
			if resp.Diagnostics.HasError() != tc.invalid || resp.State.Raw.IsNull() != tc.absent || calls != 1 {
				t.Fatal(resp.Diagnostics, calls)
			}
			if tc.invalid && !resp.State.Raw.Equal(before.Raw) {
				t.Fatal("read error erased state")
			}
			if !tc.invalid && !tc.absent {
				responderGuardAssertState(t, resp.State, 9, 11)
			}
		})
	}
}

func TestTeamResponderGuardPolicyUpdateUsesDirectReceiptAndPreservesFailures(t *testing.T) {
	for _, status := range []int{201, 403, 404, 409, 422, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamResponderGuardPolicyResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				var body map[string]json.RawMessage
				_ = json.NewDecoder(q.Body).Decode(&body)
				if q.Method != "POST" || q.Header.Get("If-Match") != "" || len(body) != 1 || string(body["max_open_requests_per_subject"]) != "9" {
					t.Error("update read, rebased, or sent fake CAS")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"max_open_requests_per_subject":9,"revision":15,"is_default":false}`)
			})
			before := valueState(t, s, responderGuardValues())
			values := responderGuardValues()
			values["max_open_requests_per_subject"], values["revision"] = types.Int64Value(9), types.Int64Unknown()
			p := valueState(t, s, values)
			resp := resource.UpdateResponse{State: before}
			r.Update(t.Context(), resource.UpdateRequest{State: before, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if resp.Diagnostics.HasError() != (status != 201) || calls != 1 {
				t.Fatal(resp.Diagnostics, calls)
			}
			if status == 201 {
				responderGuardAssertState(t, resp.State, 9, 15)
			} else if !resp.State.Raw.Equal(before.Raw) {
				t.Fatal("failed write changed state")
			}
		})
	}
}

func TestTeamResponderGuardPolicyCommittedMalformedPublicationKeepsDesiredIntent(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		for _, body := range []string{`{"revision":8}`, `{"max_open_requests_per_subject":9,"revision":8}`, `{"max_open_requests_per_subject":5,"revision":8,"is_default":false}`, `{"max_open_requests_per_subject":9,"revision":0,"is_default":true}`, `{`} {
			t.Run(operation+body, func(t *testing.T) {
				r := onpager.NewTeamResponderGuardPolicyResource()
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
					calls++
					if q.Method == "GET" && operation == "create" && calls == 1 {
						fmt.Fprint(w, `{"max_open_requests_per_subject":357,"revision":0,"is_default":true}`)
						return
					}
					if q.Method != "POST" {
						t.Error("publication recovery read or mutated again")
					}
					w.WriteHeader(201)
					fmt.Fprint(w, body)
				})
				before := valueState(t, s, responderGuardValues())
				values := responderGuardValues()
				values["max_open_requests_per_subject"], values["revision"] = types.Int64Value(9), types.Int64Unknown()
				p := valueState(t, s, values)
				if operation == "create" {
					resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
					r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
					if !resp.Diagnostics.HasError() || calls != 2 {
						t.Fatal(resp.Diagnostics, calls)
					}
					responderGuardAssertState(t, resp.State, 9, 0)
				} else {
					resp := resource.UpdateResponse{State: before}
					r.Update(t.Context(), resource.UpdateRequest{State: before, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
					if !resp.Diagnostics.HasError() || calls != 1 {
						t.Fatal(resp.Diagnostics, calls)
					}
					responderGuardAssertState(t, resp.State, 9, 0)
				}
			})
		}
	}
}

func TestTeamResponderGuardPolicyDestroyDirectResetRetainsFailedState(t *testing.T) {
	for _, status := range []int{204, 403, 404, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewTeamResponderGuardPolicyResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				body, _ := io.ReadAll(q.Body)
				if q.Method != "DELETE" || q.URL.Path != "/v1/teams/"+fTeam+"/responder-guard-policies" || q.URL.RawQuery != "" || q.Header.Get("If-Match") != "" || len(body) != 0 {
					t.Error("destroy guessed a default or performed extra read")
				}
				w.WriteHeader(status)
			})
			values := responderGuardValues()
			values["revision"] = types.Int64Null()
			before := valueState(t, s, values)
			resp := resource.DeleteResponse{State: before}
			r.Delete(t.Context(), resource.DeleteRequest{State: before}, &resp)
			if resp.Diagnostics.HasError() != (status != 204) || calls != 1 || status != 204 && !resp.State.Raw.Equal(before.Raw) {
				t.Fatal(resp.Diagnostics, calls)
			}
		})
	}
}

func TestTeamResponderGuardPolicyInvalidApplyAndChangedIdentityNeverRequest(t *testing.T) {
	for _, field := range []string{"id", "team_id", "max_open_requests_per_subject"} {
		var invalid []attr.Value
		if field == "max_open_requests_per_subject" {
			invalid = []attr.Value{types.Int64Null(), types.Int64Unknown(), types.Int64Value(0), types.Int64Value(-1), types.Int64Value(1001)}
		} else {
			invalid = []attr.Value{types.StringValue(fOtherTeam), types.StringValue("invalid"), types.StringValue("00000000-0000-0000-0000-000000000000")}
		}
		for _, value := range invalid {
			r := onpager.NewTeamResponderGuardPolicyResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("invalid apply input reached HTTP") })
			before := valueState(t, s, responderGuardValues())
			values := responderGuardValues()
			values[field] = value
			p := valueState(t, s, values)
			resp := resource.UpdateResponse{State: before}
			r.Update(t.Context(), resource.UpdateRequest{State: before, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(before.Raw) {
				t.Fatal("invalid/changed identity accepted", field, value, resp.Diagnostics)
			}
		}
	}
	for _, value := range []types.String{types.StringNull(), types.StringUnknown(), types.StringValue("invalid")} {
		r := onpager.NewTeamResponderGuardPolicyResource()
		s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("invalid create owner reached HTTP") })
		values := responderGuardValues()
		values["team_id"] = value
		p := valueState(t, s, values)
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("invalid owner accepted")
		}
	}
}

func TestTeamResponderGuardPolicyLimitPlanValidation(t *testing.T) {
	r := onpager.NewTeamResponderGuardPolicyResource()
	var s resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &s)
	a := s.Schema.Attributes["max_open_requests_per_subject"].(schema.Int64Attribute)
	for _, value := range []int64{-1, 0, 1, 1000, 1001} {
		var resp validator.Int64Response
		for _, rule := range a.Validators {
			rule.ValidateInt64(t.Context(), validator.Int64Request{Path: path.Root("max_open_requests_per_subject"), ConfigValue: types.Int64Value(value)}, &resp)
		}
		if resp.Diagnostics.HasError() != (value < 1 || value > 1000) {
			t.Fatal("bad limit accepted in plan", value, resp.Diagnostics)
		}
	}
}

func TestTeamResponderGuardPolicyImportRequiresExactTeamUUIDAndReadsOnlyLater(t *testing.T) {
	r := onpager.NewTeamResponderGuardPolicyResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("import initialization requested API") })
	for _, id := range []string{fTeam, "tenant", fTeam + "/default", "", "00000000-0000-0000-0000-000000000000", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() != (id != fTeam) {
			t.Fatal(id, resp.Diagnostics)
		}
		if id == fTeam {
			var team, savedID types.String
			resp.State.GetAttribute(t.Context(), path.Root("team_id"), &team)
			resp.State.GetAttribute(t.Context(), path.Root("id"), &savedID)
			if team.ValueString() != fTeam || savedID.ValueString() != fTeam {
				t.Fatal("import lost explicit owner")
			}
		}
	}
}
