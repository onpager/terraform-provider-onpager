package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func TestLocalUserReplacementRequiresInitialPasswordBeforeDestroy(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprint(create), func(t *testing.T) {
			r := onpager.NewUserResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("plan must not call API") })
			modifier, ok := r.(resource.ResourceWithModifyPlan)
			if !ok {
				t.Fatal("resource must guard creation/replacement before destroying the old user")
			}
			state := valueState(t, s, localUserValues())
			values := localUserValues()
			values["login"] = types.StringValue("new-login")
			planned := valueState(t, s, values)
			if create {
				state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
			}
			resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: planned.Raw}}
			modifier.ModifyPlan(t.Context(), resource.ModifyPlanRequest{State: state, Plan: resp.Plan, Config: tfsdk.Config{Schema: s, Raw: planned.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("missing initial password accepted before creating/replacing a user")
			}
		})
	}
}

func TestLocalUserInitialPasswordValidationPreservesBytesAndCountsRunes(t *testing.T) {
	r := onpager.NewUserResource()
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	a := sr.Schema.Attributes["initial_password"].(schema.StringAttribute)
	for _, tc := range []struct {
		value   string
		invalid bool
	}{
		{"short-password", true}, {strings.Repeat("я", 14), true}, {strings.Repeat("я", 15), false},
		{strings.Repeat("x", 1024), false}, {strings.Repeat("x", 1025), true}, {"  long-password  ", false},
		{string([]byte{0xff}) + strings.Repeat("a", 16), true},
	} {
		var resp validator.StringResponse
		for _, v := range a.Validators {
			v.ValidateString(t.Context(), validator.StringRequest{Path: path.Root("initial_password"), ConfigValue: types.StringValue(tc.value)}, &resp)
		}
		if resp.Diagnostics.HasError() != tc.invalid {
			t.Fatal("password length/UTF-8 validation mismatch")
		}
		for _, d := range resp.Diagnostics {
			if strings.Contains(d.Detail(), tc.value) {
				t.Fatal("password leaked in diagnostic")
			}
		}
	}
}

func localUserValues() map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(fTeam), "display_name": types.StringValue("Example user"),
		"login": types.StringValue("Example"), "login_normalized": types.StringValue("example"),
		"email": types.StringValue("User@Example.TEST"), "email_normalized": types.StringValue("user@example.test"),
		"active": types.BoolValue(true), "initial_password": types.StringNull(), "initial_password_change_required": types.BoolNull(),
	}
}

func localUserView(active bool) string {
	return fmt.Sprintf(`{"id":%q,"display_name":"Example user","email_normalized":"user@example.test","active":%t,"revision":1,"created_at":"2026-11-16T00:00:00Z","display":{"local_login":"example","auth_methods":["local_password"],"protected":false,"can_update":false,"can_deactivate":false,"can_reactivate":false,"can_reset_password":false}}`, fTeam, active)
}

func TestLocalUserProtocolSchemaUsesCreationOnlyWriteOnlyPassword(t *testing.T) {
	server := providerserver.NewProtocol6(onpager.New("test")())()
	resp, err := server.GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	s := resp.ResourceSchemas["onpager_user"]
	if s == nil {
		t.Fatal("local user resource is not registered")
	}
	found := 0
	for _, a := range s.Block.Attributes {
		if a.Name == "initial_password" || a.Name == "initial_password_change_required" {
			found++
			if !a.WriteOnly || !a.Optional || a.Computed || a.Name == "initial_password" && !a.Sensitive {
				t.Fatal("creation credentials must be optional write-only and password sensitive")
			}
		}
		if a.Name == "revision" || a.Name == "password" {
			t.Fatal("unsupported revision/password management exposed")
		}
	}
	if found != 2 {
		t.Fatal("creation-only attributes missing")
	}
}

func TestLocalUserReadRetainsInactiveAndFailedReads(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewUserResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.URL.RequestURI() != "/v1/users/"+fTeam {
					t.Error("refresh must only direct-read")
				}
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, localUserView(false))
				} else {
					fmt.Fprint(w, `{"error":{"code":"not_found","message":"secret-password"}}`)
				}
			})
			state := valueState(t, s, localUserValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() != (status == 404) {
				t.Fatal("state removed without confirmed absence")
			}
			if resp.Diagnostics.HasError() != (status != 200 && status != 404) {
				t.Fatal(resp.Diagnostics)
			}
			if status == 200 {
				var login, email types.String
				var active types.Bool
				resp.State.GetAttribute(t.Context(), path.Root("login"), &login)
				resp.State.GetAttribute(t.Context(), path.Root("email"), &email)
				resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
				if login.ValueString() != "Example" || email.ValueString() != "User@Example.TEST" || active.ValueBool() {
					t.Fatal("normalization binding/inactive identity lost")
				}
			}
		})
	}
}

func TestLocalUserReadRejectsExternalProtectedAndMalformedMetadata(t *testing.T) {
	for _, body := range []string{
		strings.Replace(localUserView(true), `"local_login":"example"`, `"local_login":null`, 1),
		strings.Replace(localUserView(true), `"protected":false`, `"protected":true`, 1),
		`{"id":"` + fTeam + `","display_name":"Example user","active":true}`,
	} {
		r := onpager.NewUserResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
		state := valueState(t, s, localUserValues())
		resp := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
		if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
			t.Fatal("unmanageable user adopted or state lost")
		}
	}
}

func TestLocalUserCreateReadsPasswordOnlyFromConfigAndSavesBeforeFailure(t *testing.T) {
	for _, failure := range []string{"read", "deactivate", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			r := onpager.NewUserResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if calls == 1 {
					var in map[string]any
					json.NewDecoder(q.Body).Decode(&in)
					if q.Method != "POST" || q.URL.Path != "/v1/users" || in["password"] != "initial-password-only" || in["password_change_required"] != false {
						t.Error("creation did not use exact config password/policy")
					}
					w.WriteHeader(201)
					if failure == "receipt" {
						fmt.Fprintf(w, `{"id":%q}`, fTeam)
					} else {
						fmt.Fprint(w, localUserView(true))
					}
					return
				}
				if failure == "deactivate" && q.URL.Path != "/v1/users/"+fTeam+"/deactivate" {
					t.Error("missing explicit deactivation")
				}
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
			})
			values := localUserValues()
			values["id"] = types.StringUnknown()
			if failure == "deactivate" {
				values["active"] = types.BoolValue(false)
			}
			plan := valueState(t, s, values)
			values["initial_password"] = types.StringValue("initial-password-only")
			values["initial_password_change_required"] = types.BoolValue(false)
			config := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}, Config: tfsdk.Config{Schema: s, Raw: config.Raw}}, &resp)
			var id, password types.String
			var active types.Bool
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("initial_password"), &password)
			resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
			if !resp.Diagnostics.HasError() || id.ValueString() != fTeam || !password.IsNull() || !active.ValueBool() {
				t.Fatal("partial create lost or password saved", resp.Diagnostics)
			}
		})
	}
}

func TestLocalUserUpdateKeepsCommittedIntentAndNeverResetsPassword(t *testing.T) {
	for _, failure := range []string{"patch", "receipt", "deactivate", "read"} {
		t.Run(failure, func(t *testing.T) {
			r := onpager.NewUserResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if strings.Contains(q.URL.Path, "password") || q.Header.Get("If-Match") != "" {
					t.Error("unsupported password/revision operation")
				}
				if calls == 1 {
					if q.Method != "PATCH" {
						t.Error("must PATCH metadata first")
					}
					var in map[string]any
					json.NewDecoder(q.Body).Decode(&in)
					if len(in) != 2 || in["display_name"] != "Changed" || in["email"] != nil {
						t.Error("incorrect sparse metadata patch")
					}
					if failure == "patch" {
						w.WriteHeader(403)
						fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
						return
					}
					if failure == "receipt" {
						fmt.Fprint(w, `{}`)
						return
					}
					fmt.Fprintf(w, `{"id":%q}`, fTeam)
					return
				}
				if q.Method == "POST" && failure != "deactivate" {
					w.WriteHeader(204)
					return
				}
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
			})
			state := valueState(t, s, localUserValues())
			values := localUserValues()
			values["display_name"], values["email"], values["active"] = types.StringValue("Changed"), types.StringNull(), types.BoolValue(false)
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			var name, email types.String
			var active types.Bool
			resp.State.GetAttribute(t.Context(), path.Root("display_name"), &name)
			resp.State.GetAttribute(t.Context(), path.Root("email"), &email)
			resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
			if !resp.Diagnostics.HasError() {
				t.Fatal("failure swallowed")
			}
			if failure == "patch" {
				if name.ValueString() != "Example user" || !active.ValueBool() {
					t.Fatal("failed PATCH changed state")
				}
			} else if name.ValueString() != "Changed" || !email.IsNull() || active.ValueBool() != (failure != "read") {
				t.Fatal("committed partial intent lost")
			}
		})
	}
}

func TestLocalUserDestroyUsesDeactivationAndPreservesFailures(t *testing.T) {
	for _, active := range []bool{true, false} {
		for _, status := range []int{204, 403, 404, 409, 500} {
			t.Run(fmt.Sprintf("%t/%d", active, status), func(t *testing.T) {
				r := onpager.NewUserResource()
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
					calls++
					if calls == 1 {
						fmt.Fprint(w, localUserView(active))
						return
					}
					if q.Method != "POST" || q.URL.Path != "/v1/users/"+fTeam+"/deactivate" {
						t.Error("destroy must deactivate")
					}
					w.WriteHeader(status)
					if status != 204 {
						fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
					}
				})
				state := valueState(t, s, localUserValues())
				resp := resource.DeleteResponse{State: state}
				r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
				wantErr := active && status != 204 && status != 404
				if resp.Diagnostics.HasError() != wantErr || wantErr && resp.State.Raw.IsNull() || !active && calls != 1 {
					t.Fatal("incorrect destroy outcome", resp.Diagnostics)
				}
			})
		}
	}
}

func TestLocalUserFollowupDoesNotBindUnwrittenEmail(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			r := onpager.NewUserResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method == "PATCH" {
					fmt.Fprintf(w, `{"id":%q}`, fTeam)
					return
				}
				if q.Method == "POST" {
					w.WriteHeader(204)
					return
				}
				view := strings.Replace(localUserView(metadata), `user@example.test`, `outside@example.test`, 1)
				if metadata {
					view = strings.Replace(view, `Example user`, `Changed`, 1)
				}
				fmt.Fprint(w, view)
			})
			state := valueState(t, s, localUserValues())
			values := localUserValues()
			if metadata {
				values["display_name"] = types.StringValue("Changed")
			} else {
				values["active"] = types.BoolValue(false)
			}
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			var email types.String
			resp.State.GetAttribute(t.Context(), path.Root("email"), &email)
			if resp.Diagnostics.HasError() || calls != 2 || email.ValueString() != "outside@example.test" {
				t.Fatal("follow-up hid concurrent drift in email that was not written", resp.Diagnostics)
			}
		})
	}
}

func TestLocalUserRefreshAfterPartialCreatePreservesImmutableLogin(t *testing.T) {
	r := onpager.NewUserResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			t.Error("refresh must not mutate")
		}
		fmt.Fprint(w, localUserView(true))
	})
	values := localUserValues()
	values["login_normalized"] = types.StringNull()
	values["email_normalized"] = types.StringNull()
	state := valueState(t, s, values)
	resp := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
	var login types.String
	resp.State.GetAttribute(t.Context(), path.Root("login"), &login)
	if resp.Diagnostics.HasError() || login.ValueString() != "Example" {
		t.Fatal("partial create refresh would force login replacement", resp.Diagnostics)
	}
}

func TestLocalUserCommittedLifecycleMalformedResponseKeepsActive(t *testing.T) {
	r := onpager.NewUserResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "POST" || q.URL.Path != "/v1/users/"+fTeam+"/deactivate" {
			t.Error("wrong lifecycle operation")
		}
		fmt.Fprint(w, `{}`) // HTTP success committed, but lifecycle contract requires 204.
	})
	state := valueState(t, s, localUserValues())
	values := localUserValues()
	values["active"] = types.BoolValue(false)
	plan := valueState(t, s, values)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	var active types.Bool
	resp.State.GetAttribute(t.Context(), path.Root("active"), &active)
	if !resp.Diagnostics.HasError() || active.ValueBool() {
		t.Fatal("committed lifecycle state was forgotten")
	}
}
