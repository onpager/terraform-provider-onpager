package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

const phoneContactTarget = "99999999-0000-4000-8000-000000000001"
const otherPhoneTarget = "99999999-0000-4000-8000-000000000002"

func phoneContactValues() map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(fTeam), "user_id": types.StringValue(fOtherTeam), "channel_id": types.StringValue(fService),
		"target_id": types.StringValue(phoneContactTarget), "display_name": types.StringValue("Desk"), "phone": types.StringValue("+12025550123"),
		"masked_phone": types.StringValue("+…0123"), "verified": types.BoolValue(true), "enabled": types.BoolValue(true),
		"draft": types.BoolValue(false), "generation": types.Int64Value(2), "request_id": types.StringValue(fOtherTeam), "template_contact_id": types.StringNull(),
	}
}

func phoneContactCard(target string) map[string]any {
	return map[string]any{"id": fTeam, "user_id": fOtherTeam, "channel_id": fService, "target_id": target, "display_name": "Desk", "masked_phone": "+…0123", "verified": true, "enabled": true, "draft": false, "generation": 2}
}

func TestUserPhoneContactReadDropsAddressOnlyOnKnownTargetDrift(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		status                 int
		target, channel        string
		imported, fails, known bool
	}{
		{"unchanged", 200, phoneContactTarget, fService, false, false, true},
		{"same suffix new binding", 200, otherPhoneTarget, fService, false, false, false},
		{"import leaves phone unavailable", 200, otherPhoneTarget, fService, true, false, false},
		{"immutable channel mismatch", 200, phoneContactTarget, otherPhoneTarget, false, true, true},
		{"denied retains address", 403, "", "", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewUserPhoneContactResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.URL.Path != "/v1/users/"+fOtherTeam+"/phone-contacts/"+fTeam {
					t.Error("unexpected scoped read")
				}
				w.WriteHeader(tc.status)
				v := phoneContactCard(tc.target)
				v["channel_id"] = tc.channel
				json.NewEncoder(w).Encode(v)
			})
			v := phoneContactValues()
			if tc.imported {
				for _, k := range []string{"phone", "request_id", "channel_id", "target_id"} {
					v[k] = types.StringNull()
				}
			}
			old := valueState(t, s, v)
			resp := resource.ReadResponse{State: old}
			r.Read(t.Context(), resource.ReadRequest{State: old}, &resp)
			if resp.Diagnostics.HasError() != tc.fails {
				t.Fatal(resp.Diagnostics)
			}
			if tc.fails {
				if !resp.State.Raw.Equal(old.Raw) {
					t.Fatal("failed read changed state")
				}
				return
			}
			var phone, target types.String
			resp.State.GetAttribute(t.Context(), path.Root("phone"), &phone)
			resp.State.GetAttribute(t.Context(), path.Root("target_id"), &target)
			if phone.IsNull() == tc.known || target.ValueString() != tc.target {
				t.Fatal("address binding did not track target UUID")
			}
		})
	}
}

func TestUserPhoneContactPatchAddressReassertsExplicitConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified types.Bool
		phone    types.String
		oldName  string
		want     map[string]any
	}{
		{"address change keeps explicit confirmation", types.BoolValue(true), types.StringValue("+13035550123"), "Desk", map[string]any{"phone": "+13035550123", "channel_id": fService, "verified": true}},
		{"rename only preserves proof", types.BoolNull(), types.StringNull(), "Old", map[string]any{"display_name": "Desk"}},
		{"false revokes without sending phone", types.BoolValue(false), types.StringNull(), "Desk", map[string]any{"verified": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewUserPhoneContactResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				var body map[string]any
				if q.Method != "PATCH" || q.URL.RequestURI() != "/v1/users/"+fOtherTeam+"/phone-contacts/"+fTeam || q.Header.Get("If-Match") != "" || json.NewDecoder(q.Body).Decode(&body) != nil || !reflect.DeepEqual(body, tc.want) {
					t.Error("PATCH did not contain exact supplied fields", body)
				}
				card := phoneContactCard(phoneContactTarget)
				if !tc.phone.IsNull() {
					card["target_id"] = otherPhoneTarget
				}
				if !tc.verified.IsNull() {
					card["verified"] = tc.verified.ValueBool()
				}
				json.NewEncoder(w).Encode(card)
			})
			v := phoneContactValues()
			v["display_name"] = types.StringValue(tc.oldName)
			old := valueState(t, s, v)
			v["display_name"] = types.StringValue("Desk")
			v["phone"], v["verified"] = tc.phone, tc.verified
			conf := valueState(t, s, v)
			if tc.phone.IsNull() {
				v["phone"] = types.StringUnknown()
			}
			if tc.verified.IsNull() {
				v["verified"] = types.BoolUnknown()
			}
			plan := valueState(t, s, v)
			resp := resource.UpdateResponse{State: old}
			r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}, Config: tfsdk.Config{Schema: s, Raw: conf.Raw}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var phone types.String
			resp.State.GetAttribute(t.Context(), path.Root("phone"), &phone)
			if tc.phone.IsNull() && phone.ValueString() != "+12025550123" || !tc.phone.IsNull() && !tc.phone.Equal(phone) {
				t.Fatal("lost bound number")
			}
		})
	}
}

func TestUserPhoneContactRecoveredCreateBindsOnlyScopedPatchResult(t *testing.T) {
	for _, status := range []int{200, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewUserPhoneContactResource()
			var calls []string
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls = append(calls, q.Method)
				var body map[string]any
				json.NewDecoder(q.Body).Decode(&body)
				switch q.Method {
				case "POST":
					if body["request_id"] != fOtherTeam {
						t.Error("unstable create request ID")
					}
					json.NewEncoder(w).Encode(phoneContactCard(phoneContactTarget))
				case "PATCH":
					if q.URL.RequestURI() != "/v1/users/"+fOtherTeam+"/phone-contacts/"+fTeam || !reflect.DeepEqual(body, map[string]any{"phone": "+12025550123", "channel_id": fService, "verified": true}) {
						t.Error("replay did not reassert scoped binding", body)
					}
					w.WriteHeader(status)
					json.NewEncoder(w).Encode(phoneContactCard(otherPhoneTarget))
				default:
					t.Error("unexpected call")
				}
			})
			p := valueState(t, s, phoneContactValues())
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, Config: tfsdk.Config{Schema: s, Raw: p.Raw}}, &resp)
			var id, phone, target types.String
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("phone"), &phone)
			resp.State.GetAttribute(t.Context(), path.Root("target_id"), &target)
			if !reflect.DeepEqual(calls, []string{"POST", "PATCH"}) || resp.Diagnostics.HasError() != (status != 200) || id.ValueString() != fTeam || phone.IsNull() != (status != 200) || strings.Contains(fmt.Sprint(resp.Diagnostics), "1202555") {
				t.Fatal("incorrect replay recovery", calls, resp.Diagnostics)
			}
			if status == 200 && target.ValueString() != otherPhoneTarget || status != 200 && target.ValueString() != phoneContactTarget {
				t.Fatal("bound the recovered address to an unproven target")
			}
		})
	}
}

func TestUserPhoneContactReadRejectsConflictingMaskForKnownTarget(t *testing.T) {
	r := onpager.NewUserPhoneContactResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		card := phoneContactCard(phoneContactTarget)
		card["masked_phone"] = "+…0999"
		json.NewEncoder(w).Encode(card)
	})
	old := valueState(t, s, phoneContactValues())
	resp := resource.ReadResponse{State: old}
	r.Read(t.Context(), resource.ReadRequest{State: old}, &resp)
	if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(old.Raw) {
		t.Fatal("accepted a different masked suffix for the known phone target")
	}
}

func TestUserPhoneContactUpdateRejectsReusedTargetForDifferentNumber(t *testing.T) {
	r := onpager.NewUserPhoneContactResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		json.NewEncoder(w).Encode(phoneContactCard(phoneContactTarget))
	})
	values := phoneContactValues()
	old := valueState(t, s, values)
	values["phone"] = types.StringValue("+13035550123")
	next := valueState(t, s, values)
	resp := resource.UpdateResponse{State: old}
	r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: next.Raw}, Config: tfsdk.Config{Schema: s, Raw: next.Raw}}, &resp)
	var phone types.String
	resp.State.GetAttribute(t.Context(), path.Root("phone"), &phone)
	if !resp.Diagnostics.HasError() || !phone.IsNull() {
		t.Fatal("associated two distinct known numbers with the same target")
	}
}

func TestUserPhoneContactCreateRequiresKnownInitialInputsWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value attr.Value
	}{
		{"phone", types.StringNull()}, {"phone", types.StringUnknown()}, {"request_id", types.StringNull()},
		{"request_id", types.StringUnknown()}, {"template_contact_id", types.StringUnknown()}, {"verified", types.BoolUnknown()},
	} {
		r := onpager.NewUserPhoneContactResource()
		s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("incomplete create reached Control") })
		values := phoneContactValues()
		values[tc.key] = tc.value
		p := valueState(t, s, values)
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, Config: tfsdk.Config{Schema: s, Raw: p.Raw}}, &resp)
		if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
			t.Fatalf("accepted incomplete %s", tc.key)
		}
	}
}

func TestUserPhoneContactFreshCreateSavesNumberOnlyWithConfirmedTarget(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		target, owner     string
		wantID, wantPhone bool
	}{
		{"fresh", 201, phoneContactTarget, fOtherTeam, true, true},
		{"incomplete card", 201, "", fOtherTeam, true, false},
		{"foreign card", 201, phoneContactTarget, fService, false, false},
		{"rejected key", 409, "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewUserPhoneContactResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				var body map[string]any
				if json.NewDecoder(q.Body).Decode(&body) != nil || body["verified"] != nil || body["request_id"] != fOtherTeam || q.Method != "POST" || q.Header.Get("If-Match") != "" {
					t.Error("wrong initial phone create")
				}
				w.WriteHeader(tc.status)
				if tc.status != 201 {
					fmt.Fprint(w, `{"error":{"code":"conflict","message":"private marker"}}`)
					return
				}
				v := phoneContactCard(tc.target)
				v["user_id"], v["verified"] = tc.owner, false
				json.NewEncoder(w).Encode(v)
			})
			v := phoneContactValues()
			v["verified"] = types.BoolNull()
			p := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, Config: tfsdk.Config{Schema: s, Raw: p.Raw}}, &resp)
			if calls != 1 || resp.Diagnostics.HasError() == tc.wantPhone || resp.State.Raw.IsNull() == tc.wantID || strings.Contains(fmt.Sprint(resp.Diagnostics), "private marker") {
				t.Fatal("lost receipt identity or replayed rejected create", resp.Diagnostics)
			}
			if tc.wantID {
				var id, phone types.String
				resp.State.GetAttribute(t.Context(), path.Root("phone"), &phone)
				resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
				if id.ValueString() != fTeam || phone.IsNull() == tc.wantPhone {
					t.Fatal("invented or lost phone binding on create")
				}
			}
		})
	}
}

func TestUserPhoneContactReturnedHTTPFailureKeepsSavedBinding(t *testing.T) {
	for _, status := range []int{403, 404, 409, 422} {
		r := onpager.NewUserPhoneContactResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"code":"not_found","message":"private marker"}}`)
		})
		v := phoneContactValues()
		old := valueState(t, s, v)
		v["phone"] = types.StringValue("+13035550123")
		p := valueState(t, s, v)
		resp := resource.UpdateResponse{State: old}
		r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, Config: tfsdk.Config{Schema: s, Raw: p.Raw}}, &resp)
		if !resp.Diagnostics.HasError() || calls != 1 || !old.Raw.Equal(resp.State.Raw) || strings.Contains(fmt.Sprint(resp.Diagnostics), "private marker") {
			t.Fatalf("HTTP %d changed state, retried or leaked error", status)
		}
	}
}

func TestUserPhoneContactRenameDetectsConcurrentBindingChange(t *testing.T) {
	r := onpager.NewUserPhoneContactResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, _ *http.Request) {
		v := phoneContactCard(otherPhoneTarget)
		v["display_name"] = "Renamed"
		json.NewEncoder(w).Encode(v)
	})
	v := phoneContactValues()
	old := valueState(t, s, v)
	v["display_name"] = types.StringValue("Renamed")
	plan := valueState(t, s, v)
	v["phone"], v["verified"] = types.StringNull(), types.BoolNull()
	config := valueState(t, s, v)
	resp := resource.UpdateResponse{State: old}
	r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}, Config: tfsdk.Config{Schema: s, Raw: config.Raw}}, &resp)
	var phone, target types.String
	resp.State.GetAttribute(t.Context(), path.Root("phone"), &phone)
	resp.State.GetAttribute(t.Context(), path.Root("target_id"), &target)
	if !resp.Diagnostics.HasError() || !phone.IsNull() || target.ValueString() != otherPhoneTarget {
		t.Fatal("failed to record changed binding on the committed rename")
	}
}
