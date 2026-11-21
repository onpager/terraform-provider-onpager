package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
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

func userBotContactValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "user_id": types.StringValue(fOtherTeam), "bot_id": types.StringValue(fService), "display_name": types.StringValue("Contact"), "recipient_id": types.StringValue("saved"), "verified": types.BoolValue(true)}
}

func TestUserBotContactPatchRetainsKnownCommittedState(t *testing.T) {
	for _, status := range []int{200, 403, 404, 409, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewUserBotContactResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				raw, _ := io.ReadAll(q.Body)
				if q.Method != "PATCH" || q.URL.RequestURI() != "/v1/users/"+fOtherTeam+"/bot-contacts/"+fTeam || q.Header.Get("If-Match") != "" || string(raw) != `{"bot_id":"`+scheduleID+`"}` {
					t.Error("PATCH must change only bot by existing method UUID")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"forbidden","message":"private marker"}}`)
			})
			old := valueState(t, s, userBotContactValues())
			v := userBotContactValues()
			v["bot_id"] = types.StringValue(scheduleID)
			v["recipient_id"] = types.StringUnknown()
			v["verified"] = types.BoolUnknown()
			p := valueState(t, s, v)
			v["recipient_id"] = types.StringNull()
			v["verified"] = types.BoolNull()
			conf := valueState(t, s, v)
			resp := resource.UpdateResponse{State: old}
			r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, Config: tfsdk.Config{Schema: s, Raw: conf.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || calls != 1 || strings.Contains(fmt.Sprint(resp.Diagnostics), "private marker") {
				t.Fatal("bad receipt lost diagnostic or replayed", resp.Diagnostics)
			}
			if status != 200 {
				if !resp.State.Raw.Equal(old.Raw) {
					t.Fatal("HTTP failure lost old state")
				}
				return
			}
			v["verified"] = types.BoolValue(false)
			if !resp.State.Raw.Equal(valueState(t, s, v).Raw) {
				t.Fatal("success status with broken receipt lost known bot change and address reset")
			}
		})
	}
}

func TestUserBotContactPartialUpsertKeepsOnlyCompatibleIdentity(t *testing.T) {
	for _, contradiction := range []bool{false, true} {
		r := onpager.NewUserBotContactResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			w.WriteHeader(200)
			if contradiction {
				fmt.Fprintf(w, `{"id":%q,"bot_id":%q}`, fTeam, scheduleID)
			} else {
				fmt.Fprintf(w, `{"id":%q}`, fTeam)
			}
		})
		v := userBotContactValues()
		v["id"] = types.StringUnknown()
		v["recipient_id"] = types.StringUnknown()
		v["verified"] = types.BoolUnknown()
		p := valueState(t, s, v)
		v["id"] = types.StringNull()
		v["recipient_id"] = types.StringNull()
		v["verified"] = types.BoolNull()
		conf := valueState(t, s, v)
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, Config: tfsdk.Config{Schema: s, Raw: conf.Raw}}, &resp)
		if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() != contradiction {
			t.Fatal("cannot recover safely from partial upsert", resp.Diagnostics)
		}
		if !contradiction {
			v["id"] = types.StringValue(fTeam)
			if !resp.State.Raw.Equal(valueState(t, s, v).Raw) {
				t.Fatal("partial upsert state contained unknown or invented recipient/verification")
			}
		}
	}
}

func TestUserBotContactUnresolvedConfiguredVerificationPreventsWrite(t *testing.T) {
	r := onpager.NewUserBotContactResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("unresolved explicit verification reached API") })
	v := userBotContactValues()
	v["verified"] = types.BoolUnknown()
	p := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, Config: tfsdk.Config{Schema: s, Raw: p.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("unresolved explicit verification treated as omitted")
	}
}

func TestUserBotContactReadHandlesConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		gone   bool
	}{{404, "not_found", true}, {404, "unknown_route", false}, {403, "forbidden", false}, {500, "internal", false}} {
		r := onpager.NewUserBotContactResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			if q.Method != "GET" || q.URL.RequestURI() != "/v1/users/"+fOtherTeam+"/bot-contacts/"+fTeam {
				t.Error("unexpected refresh request")
			}
			w.WriteHeader(tc.status)
			fmt.Fprintf(w, `{"error":{"code":%q}}`, tc.code)
		})
		old := valueState(t, s, userBotContactValues())
		resp := resource.ReadResponse{State: old}
		r.Read(t.Context(), resource.ReadRequest{State: old}, &resp)
		if resp.Diagnostics.HasError() == tc.gone || resp.State.Raw.IsNull() != tc.gone || !tc.gone && !old.Raw.Equal(resp.State.Raw) {
			t.Fatal("refresh lost state or failed to remove confirmed absence", tc)
		}
	}
}

func TestUserBotContactImportUsesScopedMethodIdentity(t *testing.T) {
	r := onpager.NewUserBotContactResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("import unexpectedly issued a request") })
	for _, id := range []string{fOtherTeam + "/" + fTeam, fTeam, "invalid/" + fTeam} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if id != fOtherTeam+"/"+fTeam {
			if !resp.Diagnostics.HasError() {
				t.Error("accepted invalid import", id)
			}
			continue
		}
		var user, method types.String
		resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("user_id"), &user)...)
		resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("id"), &method)...)
		if resp.Diagnostics.HasError() || user.ValueString() != fOtherTeam || method.ValueString() != fTeam {
			t.Fatal("lost import identity", resp.Diagnostics)
		}
	}
}

func TestUserBotContactPatchExplicitVerificationUsesConfiguredBinding(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprint(confirm), func(t *testing.T) {
			r := onpager.NewUserBotContactResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "PATCH" || q.URL.RequestURI() != "/v1/users/"+fOtherTeam+"/bot-contacts/"+fTeam {
					t.Error("unexpected contact update")
				}
				var body map[string]any
				if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				want := map[string]any{"verified": confirm}
				if confirm {
					// Even an unchanged recipient must be part of the assertion:
					// an unversioned PATCH can race another address change.
					want["recipient_id"] = "saved"
					want["bot_id"] = fService
				}
				if !reflect.DeepEqual(body, want) {
					t.Error("verification update sent the wrong binding fields")
				}
				fmt.Fprintf(w, `{"id":%q,"bot_id":%q,"display_name":"Contact","recipient_id":"saved","verified":%t}`, fTeam, fService, confirm)
			})
			v := userBotContactValues()
			v["verified"] = types.BoolValue(!confirm)
			old := valueState(t, s, v)
			v["verified"] = types.BoolValue(confirm)
			next := valueState(t, s, v)
			resp := resource.UpdateResponse{State: old}
			r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: next.Raw}, Config: tfsdk.Config{Schema: s, Raw: next.Raw}}, &resp)
			if resp.Diagnostics.HasError() || !resp.State.Raw.Equal(next.Raw) {
				t.Fatal("explicit verification not saved", resp.Diagnostics)
			}
		})
	}
}
