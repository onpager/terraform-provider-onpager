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
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

var botCfgTypes = map[string]attr.Type{"mode": types.StringType, "base_url": types.StringType, "allowed_updates": types.SetType{ElemType: types.StringType}}
var botPaceTypes = map[string]attr.Type{"requests": types.Int64Type, "window_ms": types.Int64Type, "search_requests": types.Int64Type}
var botSecTypes = map[string]attr.Type{"action": types.StringType, "value": types.StringType}
var botRunTypes = map[string]attr.Type{"state": types.StringType, "error_code": types.StringType, "last_probe_at": types.StringType, "observed_at": types.StringType}

const botViewJSON = `{"id":"11111111-1111-4111-8111-111111111111","messenger":"telegram","display_name":"Passive","public_handle":"","enrollment_url":"https://t.me/terraform_passive","enabled":false,"visibility":"global","addressing_scope":{"kind":"bot_account","id":""},"configuration":{"mode":"polling","allowed_updates":["callback_query","message","my_chat_member"]},"pacing":{"requests":10,"window_ms":1000,"search_requests":0},"team_ids":[],"secret_keys":["token"],"runtime":{"state":"disabled"}}`

func botValues() map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(fTeam), "messenger": types.StringValue("telegram"), "display_name": types.StringValue("Passive"), "public_handle": types.StringValue(""), "enrollment_url": types.StringValue("https://t.me/terraform_passive"), "enabled": types.BoolValue(false), "visibility": types.StringValue("global"), "team_ids": types.SetValueMust(types.StringType, []attr.Value{}), "addressing_scope_id": types.StringValue(""), "addressing_scope_kind": types.StringValue("bot_account"),
		"configuration": types.ObjectValueMust(botCfgTypes, map[string]attr.Value{"mode": types.StringValue("polling"), "base_url": types.StringValue(""), "allowed_updates": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("callback_query"), types.StringValue("message"), types.StringValue("my_chat_member")})}),
		"pacing":        types.ObjectValueMust(botPaceTypes, map[string]attr.Value{"requests": types.Int64Value(10), "window_ms": types.Int64Value(1000), "search_requests": types.Int64Value(0)}),
		"secrets":       types.MapValueMust(types.ObjectType{AttrTypes: botSecTypes}, map[string]attr.Value{"token": types.ObjectValueMust(botSecTypes, map[string]attr.Value{"action": types.StringValue("replace"), "value": types.StringValue("synthetic-secret")})}), "secret_keys": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("token")}), "callback_url": types.StringValue(""), "runtime": types.ObjectValueMust(botRunTypes, map[string]attr.Value{"state": types.StringValue("disabled"), "error_code": types.StringValue(""), "last_probe_at": types.StringValue(""), "observed_at": types.StringValue("")}),
	}
}

func TestBotReadPreservesSecretsAndOnlyConfirmedAbsenceRemovesState(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewBotResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.URL.Path != "/v1/bots/"+fTeam {
					t.Error("refresh must direct read")
				}
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, botViewJSON)
				} else {
					io.WriteString(w, `{"error":{"code":"not_found","message":"synthetic-secret"}}`)
				}
			})
			state := valueState(t, s, botValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() != (status == 404) || (status != 200 && status != 404) != resp.Diagnostics.HasError() {
				t.Fatalf("unsafe read %v", resp.Diagnostics)
			}
			if status == 200 {
				var secrets types.Map
				resp.State.GetAttribute(t.Context(), path.Root("secrets"), &secrets)
				if !secrets.Equal(botValues()["secrets"]) {
					t.Fatal("GET lost write-only secrets")
				}
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Detail(), "synthetic-secret") {
					t.Fatal("secret leak")
				}
			}
		})
	}
}
func TestBotMetadataUpdateOmitsRetainedReplacementAndSavesCommitted204(t *testing.T) {
	for _, follow := range []int{200, 403, 404, 500} {
		t.Run(fmt.Sprint(follow), func(t *testing.T) {
			calls := 0
			r := onpager.NewBotResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Header.Get("If-Match") != "" {
					t.Error("bot used CAS")
				}
				if calls == 1 {
					if q.Method != "PUT" {
						t.Error("wrong write")
					}
					var b map[string]json.RawMessage
					json.NewDecoder(q.Body).Decode(&b)
					if b["secrets"] != nil || string(b["enabled"]) != "false" {
						t.Fatalf("replayed retained secret or enabled bot: %s", b)
					}
					w.WriteHeader(204)
					return
				}
				w.WriteHeader(follow)
				if follow == 200 {
					io.WriteString(w, strings.Replace(botViewJSON, `"Passive"`, `"Renamed"`, 1))
				} else {
					io.WriteString(w, `{"error":{"code":"forbidden"}}`)
				}
			})
			state := valueState(t, s, botValues())
			values := botValues()
			values["display_name"] = types.StringValue("Renamed")
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			var label types.String
			resp.State.GetAttribute(t.Context(), path.Root("display_name"), &label)
			if label.ValueString() != "Renamed" || calls != 2 || resp.State.Raw.IsNull() || resp.Diagnostics.HasError() != (follow != 200) {
				t.Fatalf("lost committed write %v calls=%d", resp.Diagnostics, calls)
			}
		})
	}
}
func TestBotUpdateRejectsForeignFollowupAndKeepsCommittedIntent(t *testing.T) {
	calls := 0
	r := onpager.NewBotResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method == "PUT" {
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, botViewJSON)
	})
	state := valueState(t, s, botValues())
	v := botValues()
	v["display_name"] = types.StringValue("Renamed")
	plan := valueState(t, s, v)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	var label types.String
	resp.State.GetAttribute(t.Context(), path.Root("display_name"), &label)
	if !resp.Diagnostics.HasError() || label.ValueString() != "Renamed" || calls != 2 {
		t.Fatal("foreign read replaced committed own intent")
	}
}
func TestBotCreateAndDeleteFailuresKeepIdentityAndDoNotDisable(t *testing.T) {
	r := onpager.NewBotResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method == "POST" {
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"id":%q}`, fTeam)
			return
		}
		if q.Method != "DELETE" {
			t.Fatal("unexpected disable/unlink")
		}
		w.WriteHeader(409)
		io.WriteString(w, `{"error":{"code":"bot_in_use"}}`)
	})
	values := botValues()
	values["id"] = types.StringUnknown()
	values["addressing_scope_id"] = types.StringUnknown()
	p := valueState(t, s, values)
	created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &created)
	var id types.String
	created.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if !created.Diagnostics.HasError() || id.ValueString() != fTeam {
		t.Fatal("committed create identity lost")
	}
	deleted := resource.DeleteResponse{State: created.State}
	r.Delete(t.Context(), resource.DeleteRequest{State: created.State}, &deleted)
	if !deleted.Diagnostics.HasError() || deleted.State.Raw.IsNull() || calls != 2 {
		t.Fatal("dependency failure abandoned bot")
	}
}
func TestBotUnknownAndInvalidApplyNeverRequests(t *testing.T) {
	for name, change := range map[string]func(map[string]attr.Value){"unknown messenger": func(v map[string]attr.Value) { v["messenger"] = types.StringUnknown() }, "null name": func(v map[string]attr.Value) { v["display_name"] = types.StringNull() }, "unknown secrets": func(v map[string]attr.Value) {
		v["secrets"] = types.MapUnknown(types.ObjectType{AttrTypes: botSecTypes})
	}, "unknown configuration": func(v map[string]attr.Value) { v["configuration"] = types.ObjectUnknown(botCfgTypes) }, "explicit Telegram scope": func(v map[string]attr.Value) { v["addressing_scope_id"] = types.StringValue("123") }} {
		t.Run(name, func(t *testing.T) {
			r := onpager.NewBotResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid plan made request") })
			values := botValues()
			change(values)
			p := valueState(t, s, values)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}

func TestBotConfigurationRejectsProviderMismatchAndNormalizingEmptyDefaults(t *testing.T) {
	for name, change := range map[string]func(map[string]attr.Value){
		"empty mode": func(v map[string]attr.Value) {
			a := v["configuration"].(types.Object).Attributes()
			a["mode"] = types.StringValue("")
			v["configuration"] = types.ObjectValueMust(botCfgTypes, a)
		},
		"empty updates": func(v map[string]attr.Value) {
			a := v["configuration"].(types.Object).Attributes()
			a["allowed_updates"] = types.SetValueMust(types.StringType, []attr.Value{})
			v["configuration"] = types.ObjectValueMust(botCfgTypes, a)
		},
		"foreign base URL": func(v map[string]attr.Value) {
			a := v["configuration"].(types.Object).Attributes()
			a["base_url"] = types.StringValue("https://chat.invalid")
			v["configuration"] = types.ObjectValueMust(botCfgTypes, a)
		},
		"long public handle": func(v map[string]attr.Value) { v["public_handle"] = types.StringValue(strings.Repeat("x", 201)) },
		"null action": func(v map[string]attr.Value) {
			v["secrets"] = types.MapValueMust(types.ObjectType{AttrTypes: botSecTypes}, map[string]attr.Value{"token": types.ObjectValueMust(botSecTypes, map[string]attr.Value{"action": types.StringNull(), "value": types.StringValue("dummy")})})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := onpager.NewBotResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid plan made request") })
			v := botValues()
			v["addressing_scope_id"] = types.StringNull()
			change(v)
			p := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestBotPlanDefaultsPreserveExplicitUnknowns(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			r := onpager.NewBotResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("plan made request") })
			c := botValues()
			a := c["configuration"].(types.Object).Attributes()
			a["mode"] = types.StringNull()
			a["allowed_updates"] = types.SetNull(types.StringType)
			if unknown {
				a["mode"] = types.StringUnknown()
			}
			c["configuration"] = types.ObjectValueMust(botCfgTypes, a)
			config := valueState(t, s, c)
			p := botValues()
			a = p["configuration"].(types.Object).Attributes()
			a["mode"] = types.StringUnknown()
			a["allowed_updates"] = types.SetUnknown(types.StringType)
			p["configuration"] = types.ObjectValueMust(botCfgTypes, a)
			plan := valueState(t, s, p)
			resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}
			r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: s, Raw: config.Raw}, Plan: resp.Plan}, &resp)
			var cfg types.Object
			resp.Plan.GetAttribute(t.Context(), path.Root("configuration"), &cfg)
			a = cfg.Attributes()
			mode := a["mode"].(types.String)
			if resp.Diagnostics.HasError() || mode.IsUnknown() != unknown || !unknown && mode.ValueString() != "polling" || len(a["allowed_updates"].(types.Set).Elements()) != 3 {
				t.Fatalf("incorrect conditional defaults: %v %v", cfg, resp.Diagnostics)
			}
		})
	}
}
func TestBotUUIDImportReadsPublicSettingsWithoutRecoveringSecretValues(t *testing.T) {
	r := onpager.NewBotResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" || q.URL.Path != "/v1/bots/"+fTeam {
			t.Fatal("import mutated or scanned")
		}
		io.WriteString(w, botViewJSON)
	})
	for _, id := range []string{"telegram", strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), "00000000-0000-0000-0000-000000000000", fTeam} {
		imported := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &imported)
		if (id != fTeam) != imported.Diagnostics.HasError() {
			t.Fatal("invalid import identity accepted")
		}
		if id != fTeam {
			continue
		}
		read := resource.ReadResponse{State: imported.State}
		r.Read(t.Context(), resource.ReadRequest{State: imported.State}, &read)
		var secrets types.Map
		var enabled types.Bool
		read.State.GetAttribute(t.Context(), path.Root("secrets"), &secrets)
		read.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)
		if read.Diagnostics.HasError() || !secrets.IsNull() || enabled.ValueBool() {
			t.Fatalf("import invented secrets or enabled bot %v", read.Diagnostics)
		}
	}
}
func TestBotOptionalSecretsDeletionAndDriftRestore(t *testing.T) {
	for _, action := range []string{"delete", "replace"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			r := onpager.NewBotResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method == "PUT" {
					var in struct {
						Secrets map[string]struct {
							Action string `json:"action"`
							Value  string `json:"value"`
						} `json:"secrets"`
					}
					json.NewDecoder(q.Body).Decode(&in)
					ch, ok := in.Secrets["proxy_url"]
					if !ok || ch.Action != action || len(in.Secrets) != 1 {
						t.Fatalf("incorrect optional secret action: %+v", in)
					}
					w.WriteHeader(204)
					return
				}
				body := botViewJSON
				if action == "replace" {
					body = strings.Replace(body, `"secret_keys":["token"]`, `"secret_keys":["proxy_url","token"]`, 1)
				}
				io.WriteString(w, body)
			})
			sv := botValues()
			old := sv["secrets"].(types.Map).Elements()
			old["proxy_url"] = types.ObjectValueMust(botSecTypes, map[string]attr.Value{"action": types.StringValue("replace"), "value": types.StringValue("https://user:synthetic@proxy.invalid")})
			sv["secrets"] = types.MapValueMust(types.ObjectType{AttrTypes: botSecTypes}, old)
			if action == "delete" {
				sv["secret_keys"] = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("token"), types.StringValue("proxy_url")})
			}
			state := valueState(t, s, sv)
			pv := botValues()
			ps := pv["secrets"].(types.Map).Elements()
			value := types.StringValue("https://user:synthetic@proxy.invalid")
			if action == "delete" {
				value = types.StringNull()
			}
			ps["proxy_url"] = types.ObjectValueMust(botSecTypes, map[string]attr.Value{"action": types.StringValue(action), "value": value})
			pv["secrets"] = types.MapValueMust(types.ObjectType{AttrTypes: botSecTypes}, ps)
			plan := valueState(t, s, pv)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if resp.Diagnostics.HasError() || calls != 2 {
				t.Fatalf("explicit optional action failed %v", resp.Diagnostics)
			}
		})
	}
}

func TestBotDeletedSecretStaleFollowupRetainsCommittedKeys(t *testing.T) {
	r := onpager.NewBotResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "PUT" {
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, strings.Replace(botViewJSON, `"secret_keys":["token"]`, `"secret_keys":["proxy_url","token"]`, 1))
	})
	sv := botValues()
	sv["secret_keys"] = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("proxy_url"), types.StringValue("token")})
	state := valueState(t, s, sv)
	pv := botValues()
	actions := pv["secrets"].(types.Map).Elements()
	actions["proxy_url"] = types.ObjectValueMust(botSecTypes, map[string]attr.Value{"action": types.StringValue("delete"), "value": types.StringNull()})
	pv["secrets"] = types.MapValueMust(types.ObjectType{AttrTypes: botSecTypes}, actions)
	plan := valueState(t, s, pv)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	var keys types.Set
	resp.State.GetAttribute(t.Context(), path.Root("secret_keys"), &keys)
	if !resp.Diagnostics.HasError() || len(keys.Elements()) != 1 {
		t.Fatal("stale followup forgot committed secret deletion")
	}
}
