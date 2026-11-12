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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

var inhibitionMatcherType = types.ObjectType{AttrTypes: map[string]attr.Type{"source": types.StringType, "target": types.StringType}}

func inhibitionMatchers(source, target string) types.Set {
	return types.SetValueMust(inhibitionMatcherType, []attr.Value{types.ObjectValueMust(inhibitionMatcherType.AttrTypes, map[string]attr.Value{"source": types.StringValue(source), "target": types.StringValue(target)})})
}
func inhibitionValues() map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(fTeam), "revision": types.Int64Value(7), "name": types.StringValue("Passive"), "source_expression": types.StringValue("false"), "target_expression": types.StringValue("false"), "matchers": inhibitionMatchers(" app/name ", "target.name"), "resolved_target_action": types.StringValue("keep"), "enabled": types.BoolValue(false), "index_state": types.StringValue("ready"),
	}
}
func inhibitionBody(revision int64, enabled bool) string {
	return fmt.Sprintf(`{"id":%q,"revision":%d,"name":"Passive","source_expression":"false","target_expression":"false","matchers":[{"source":" app/name ","target":"target.name"}],"resolved_target_action":"keep","enabled":%t,"index_state":"ready","author":{"user_id":null,"service_account_id":%q},"created_at":"2026-11-04T00:00:00Z","updated_at":"2026-11-04T00:00:00Z"}`, fTeam, revision, enabled, fService)
}

func TestInhibitionResourceAtomicDisabledCreateAndOwnRevisionUpdate(t *testing.T) {
	r := onpager.NewInhibitionRuleResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		switch calls {
		case 1:
			if q.Method != "POST" || q.URL.Path != "/v1/inhibition-rules" || q.Header.Get("If-Match") != "" {
				t.Fatal("create was not atomic POST")
			}
			var b map[string]json.RawMessage
			json.NewDecoder(q.Body).Decode(&b)
			if string(b["enabled"]) != "false" || len(b) != 6 || string(b["matchers"]) != `[{"source":" app/name ","target":"target.name"}]` {
				t.Fatal("lost false/literal keys")
			}
			w.WriteHeader(201)
			io.WriteString(w, inhibitionBody(7, false))
		case 2:
			if q.Method != "PUT" || q.URL.Path != "/v1/inhibition-rules/"+fTeam || q.Header.Get("If-Match") != `"7"` {
				t.Fatal("wrong direct update CAS")
			}
			var b map[string]json.RawMessage
			json.NewDecoder(q.Body).Decode(&b)
			if string(b["enabled"]) != "true" {
				t.Fatal("enable not in replacement")
			}
			fmt.Fprintf(w, `{"id":%q,"revision":8}`, fTeam)
		case 3:
			if q.Method != "GET" || q.URL.Path != "/v1/inhibition-rules/"+fTeam || q.Header.Get("If-Match") != "" {
				t.Fatal("receipt was not hydrated by direct read")
			}
			io.WriteString(w, inhibitionBody(8, true))
		default:
			t.Fatal("unexpected call")
		}
	})
	v := inhibitionValues()
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	v["index_state"] = types.StringUnknown()
	plan := valueState(t, s, v)
	created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &created)
	if created.Diagnostics.HasError() || calls != 1 {
		t.Fatalf("atomic create %v calls=%d", created.Diagnostics, calls)
	}
	v = inhibitionValues()
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	v["index_state"] = types.StringUnknown()
	v["enabled"] = types.BoolValue(true)
	plan = valueState(t, s, v)
	updated := resource.UpdateResponse{State: created.State}
	r.Update(t.Context(), resource.UpdateRequest{State: created.State, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &updated)
	if updated.Diagnostics.HasError() || calls != 3 {
		t.Fatal(updated.Diagnostics)
	}
	var rev types.Int64
	var enabled types.Bool
	updated.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
	updated.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)
	if rev.ValueInt64() != 8 || !enabled.ValueBool() {
		t.Fatal("new own revision/active intent lost")
	}
}

func TestInhibitionReadOnlyAbsenceAndErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		absent bool
		failed bool
	}{
		{200, inhibitionBody(7, false), false, false}, {200, strings.Replace(inhibitionBody(7, false), `"enabled":false,`, "", 1), false, true}, {200, strings.Replace(inhibitionBody(7, false), fTeam, fOtherTeam, 1), false, true},
		{401, `{"error":{"code":"unauthorized"}}`, false, true}, {403, `{"error":{"code":"forbidden"}}`, false, true}, {404, `{"error":{"code":"not_found"}}`, true, false}, {404, `{"error":{"code":"forbidden"}}`, false, true}, {500, `{}`, false, true},
	} {
		t.Run(fmt.Sprint(tc.status)+tc.body, func(t *testing.T) {
			r := onpager.NewInhibitionRuleResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.URL.Path != "/v1/inhibition-rules/"+fTeam || q.URL.RawQuery != "" {
					t.Fatal("refresh scanned/mutated")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			state := valueState(t, s, inhibitionValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.failed || resp.State.Raw.IsNull() != tc.absent {
				t.Fatalf("read: %v", resp.Diagnostics)
			}
			if tc.failed && !resp.State.Raw.Equal(state.Raw) {
				t.Fatal("failed read changed state")
			}
		})
	}
}

func TestInhibitionCommittedUpdateKeepsIntentAndTrustedCASOnReadFailure(t *testing.T) {
	for _, body := range []string{fmt.Sprintf(`{"id":%q,"revision":8}`, fTeam), fmt.Sprintf(`{"id":%q}`, fTeam), fmt.Sprintf(`{"id":%q,"revision":8}`, fOtherTeam), `{`} {
		t.Run(body, func(t *testing.T) {
			r := onpager.NewInhibitionRuleResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if calls == 1 {
					if q.Method != "PUT" || q.Header.Get("If-Match") != `"7"` {
						t.Fatal("lost saved CAS")
					}
					io.WriteString(w, body)
				} else {
					if q.Method != "GET" {
						t.Fatal("unexpected followup mutation")
					}
					w.WriteHeader(403)
					io.WriteString(w, `{"error":{"code":"forbidden"}}`)
				}
			})
			state := valueState(t, s, inhibitionValues())
			v := inhibitionValues()
			v["enabled"] = types.BoolValue(true)
			v["index_state"] = types.StringUnknown()
			plan := valueState(t, s, v)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
				t.Fatal("lost committed update")
			}
			var id types.String
			var rev types.Int64
			var enabled types.Bool
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
			resp.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)
			if id.ValueString() != fTeam || !enabled.ValueBool() {
				t.Fatal("substituted identity or lost committed intent")
			}
			if body == fmt.Sprintf(`{"id":%q,"revision":8}`, fTeam) {
				if calls != 2 || rev.ValueInt64() != 8 {
					t.Fatal("lost trusted committed receipt")
				}
			} else if calls != 1 || !rev.IsNull() {
				t.Fatalf("untrusted CAS retained %v calls=%d", rev, calls)
			}
		})
	}
}

func TestInhibitionCreateMalformedCommittedResponseKeepsKnownID(t *testing.T) {
	r := onpager.NewInhibitionRuleResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%q,"revision":1}`, fTeam)
	})
	v := inhibitionValues()
	v["id"] = types.StringUnknown()
	v["revision"] = types.Int64Unknown()
	v["index_state"] = types.StringUnknown()
	plan := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	var id types.String
	var rev types.Int64
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	resp.State.GetAttribute(t.Context(), path.Root("revision"), &rev)
	if !resp.Diagnostics.HasError() || id.ValueString() != fTeam || rev.ValueInt64() != 1 {
		t.Fatal("lost committed create identity/CAS")
	}
}

func TestInhibitionWriteConflictsRetainStateWithoutReplay(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 412, 422, 500} {
		r := onpager.NewInhibitionRuleResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			if q.Method != "PUT" {
				t.Fatal("automatic re-read/retry")
			}
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"revision_mismatch"}}`)
		})
		state := valueState(t, s, inhibitionValues())
		v := inhibitionValues()
		v["enabled"] = types.BoolValue(true)
		plan := valueState(t, s, v)
		resp := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
		if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) || calls != 1 {
			t.Fatalf("HTTP %d changed state/replayed", status)
		}
	}
}

func TestInhibitionDeleteUsesExactSavedCASAndOnlyConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		failed bool
	}{{204, "", false}, {404, `{"error":{"code":"not_found"}}`, false}, {403, `{"error":{"code":"forbidden"}}`, true}, {412, `{"error":{"code":"revision_mismatch"}}`, true}, {200, `{}`, true}} {
		r := onpager.NewInhibitionRuleResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			if q.Method != "DELETE" || q.URL.Path != "/v1/inhibition-rules/"+fTeam || q.Header.Get("If-Match") != `"7"` {
				t.Fatal("destroy read/disabled/adopted CAS")
			}
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		state := valueState(t, s, inhibitionValues())
		resp := resource.DeleteResponse{State: state}
		r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
		if resp.Diagnostics.HasError() != tc.failed || calls != 1 {
			t.Fatal("retirement failure swallowed/retried")
		}
		if tc.failed && !resp.State.Raw.Equal(state.Raw) {
			t.Fatal("failed destroy lost state")
		}
	}
}

func TestInhibitionInvalidKnownInputsNeverWrite(t *testing.T) {
	bad := []struct {
		field string
		value attr.Value
	}{
		{"name", types.StringValue(" Passive")}, {"name", types.StringValue(strings.Repeat("я", 65))}, {"source_expression", types.StringValue("false ")}, {"source_expression", types.StringValue(strings.Repeat("x", 4097))}, {"target_expression", types.StringNull()}, {"enabled", types.BoolUnknown()}, {"enabled", types.BoolNull()}, {"resolved_target_action", types.StringValue("discard")}, {"matchers", types.SetValueMust(inhibitionMatcherType, []attr.Value{})}, {"matchers", inhibitionMatchers("", "target")}, {"matchers", inhibitionMatchers(strings.Repeat("я", 129), "target")}, {"matchers", inhibitionMatchers("a\x00b", "target")}, {"matchers", types.SetUnknown(inhibitionMatcherType)},
	}
	for _, tc := range bad {
		t.Run(tc.field+tc.value.String(), func(t *testing.T) {
			r := onpager.NewInhibitionRuleResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid configuration wrote") })
			v := inhibitionValues()
			v[tc.field] = tc.value
			plan := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestInhibitionStrictUUIDImportDoesNotWrite(t *testing.T) {
	r := onpager.NewInhibitionRuleResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("import mutated/read before refresh") })
	for _, id := range []string{fTeam, "bad", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "00000000-0000-0000-0000-000000000000", fTeam + "/extra"} {
		importState := valueState(t, s, inhibitionValues())
		importState.RemoveResource(t.Context())
		resp := resource.ImportStateResponse{State: importState}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		valid := id == fTeam
		if resp.Diagnostics.HasError() == valid {
			t.Fatalf("invalid import outcome %q %v", id, resp.Diagnostics)
		}
	}
}

func TestInhibitionFollowupReadCannotReplaceCommittedRevisionOrIntent(t *testing.T) {
	for _, revision := range []int64{7, 9} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			r := onpager.NewInhibitionRuleResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if calls == 1 {
					if q.Method != "PUT" {
						t.Fatal("expected one update")
					}
					fmt.Fprintf(w, `{"id":%q,"revision":8}`, fTeam)
					return
				}
				if q.Method != "GET" {
					t.Fatal("must hydrate exact committed object")
				}
				io.WriteString(w, inhibitionBody(revision, false))
			})
			state := valueState(t, s, inhibitionValues())
			v := inhibitionValues()
			v["enabled"] = types.BoolValue(true)
			plan := valueState(t, s, v)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			var gotRev types.Int64
			var enabled types.Bool
			var id types.String
			resp.State.GetAttribute(t.Context(), path.Root("revision"), &gotRev)
			resp.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			if !resp.Diagnostics.HasError() || calls != 2 || gotRev.ValueInt64() != 8 || !enabled.ValueBool() || id.ValueString() != fTeam {
				t.Fatal("follow-up projection replaced committed configuration")
			}
		})
	}
}
