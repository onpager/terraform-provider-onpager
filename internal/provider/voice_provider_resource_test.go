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
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

var voiceConfigurationTypes = map[string]attr.Type{"call_campaign_id": types.StringType, "verification_campaign_id": types.StringType, "max_text_runes": types.Int64Type}
var voiceSecretObject = types.ObjectType{AttrTypes: map[string]attr.Type{"action": types.StringType, "value": types.StringType}}

func voiceValues() map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(fService), "kind": types.StringValue("zvonok"), "display_name": types.StringValue("Passive voice"), "enabled": types.BoolValue(false), "visibility": types.StringValue("global"),
		"team_ids": types.SetNull(types.StringType), "configuration": types.ObjectValueMust(voiceConfigurationTypes, map[string]attr.Value{"call_campaign_id": types.StringValue("calls"), "verification_campaign_id": types.StringValue("verify"), "max_text_runes": types.Int64Value(500)}),
		"secrets":     types.MapValueMust(voiceSecretObject, map[string]attr.Value{"public_key": types.ObjectValueMust(voiceSecretObject.AttrTypes, map[string]attr.Value{"action": types.StringValue("replace"), "value": types.StringValue("private-voice-marker")})}),
		"secret_keys": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("public_key")}), "callback_url": types.StringValue("https://ingress.invalid/callback/private-callback-marker"), "runtime_status": types.StringValue("disabled"), "runtime_error_code": types.StringNull(), "runtime_observed_at": types.StringNull(),
	}
}
func providerVoiceView(w http.ResponseWriter, name string) {
	fmt.Fprintf(w, `{"id":%q,"kind":"zvonok","display_name":%q,"enabled":false,"visibility":"global","configuration":{"call_campaign_id":"calls","verification_campaign_id":"verify","max_text_runes":500},"team_ids":[],"secret_keys":["public_key"],"runtime":{"state":"disabled"}}`, fService, name)
}

func TestVoiceProviderResourceDirectLifecyclePreservesSecret(t *testing.T) {
	r := onpager.NewVoiceProviderResource()
	var methods []string
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		methods = append(methods, q.Method)
		want := "/v1/voice-providers/" + fService
		if q.Method == "POST" {
			want = "/v1/voice-providers"
		}
		if q.URL.Path != want || q.Header.Get("If-Match") != "" {
			t.Error("wrong route or invented precondition")
		}
		if q.Method == "PUT" {
			var body map[string]json.RawMessage
			_ = json.NewDecoder(q.Body).Decode(&body)
			if body["secrets"] != nil || body["kind"] != nil {
				t.Error("unchanged key replayed or immutable kind sent")
			}
			w.WriteHeader(204)
			return
		}
		if q.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		if q.Method == "POST" {
			w.WriteHeader(201)
		}
		providerVoiceView(w, "Passive voice")
	})
	p := valueState(t, s, voiceValues())
	created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	refreshed := resource.ReadResponse{State: created.State}
	r.Read(t.Context(), resource.ReadRequest{State: created.State}, &refreshed)
	if refreshed.Diagnostics.HasError() {
		t.Fatal(refreshed.Diagnostics)
	}
	var value types.String
	refreshed.State.GetAttribute(t.Context(), path.Root("secrets").AtMapKey("public_key").AtName("value"), &value)
	if value.ValueString() != "private-voice-marker" {
		t.Fatal("refresh erased write-only secret")
	}
	updated := resource.UpdateResponse{State: refreshed.State}
	r.Update(t.Context(), resource.UpdateRequest{State: refreshed.State, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	var deleted resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: updated.State}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
	if strings.Join(methods, ",") != "POST,GET,PUT,GET,DELETE" {
		t.Fatal("unexpected requests", methods)
	}
}

func TestVoiceProviderResourceCommittedUpdateSavesDesiredStateBeforeFailedRead(t *testing.T) {
	for _, status := range []int{403, 404, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method == "PUT" {
					w.WriteHeader(204)
					return
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"forbidden","message":"private-voice-marker"}}`)
			})
			state := valueState(t, s, voiceValues())
			planned := voiceValues()
			planned["display_name"] = types.StringValue("Committed rename")
			planned["secrets"] = types.MapNull(voiceSecretObject)
			p := valueState(t, s, planned)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			var name, id types.String
			resp.State.GetAttribute(t.Context(), path.Root("display_name"), &name)
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			if !resp.Diagnostics.HasError() || name.ValueString() != "Committed rename" || id.ValueString() != fService || calls != 2 || strings.Contains(fmt.Sprint(resp.Diagnostics), "private-voice-marker") {
				t.Fatalf("committed state lost: %v", resp.Diagnostics)
			}
		})
	}
}

func TestVoiceProviderResourceRejectsFollowupDifferentFromCommittedIntent(t *testing.T) {
	for _, mode := range []string{"display_name", "call_campaign", "verification_campaign", "text_limit", "enabled", "visibility", "team_set", "kind", "empty_secrets"} {
		t.Run(mode, func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method == "PUT" {
					w.WriteHeader(204)
					return
				}
				v := client.VoiceProvider{ID: fService, Kind: "zvonok", DisplayName: "Committed name", Enabled: true, Visibility: "team", Configuration: client.VoiceProviderConfiguration{CallCampaignID: "committed-calls", VerificationCampaignID: "committed-verify", MaxTextRunes: 350}, TeamIDs: []string{fOtherTeam, fTeam}, SecretKeys: []string{"public_key"}, CallbackURL: "https://ingress.invalid/foreign-callback-marker", Runtime: client.VoiceProviderRuntime{State: "running", ObservedAt: "2026-11-10T10:00:00.123456Z"}}
				switch mode {
				case "display_name":
					v.DisplayName = "Foreign name"
				case "call_campaign":
					v.Configuration.CallCampaignID = "foreign-calls"
				case "verification_campaign":
					v.Configuration.VerificationCampaignID = "foreign-verify"
				case "text_limit":
					v.Configuration.MaxTextRunes = 500
				case "enabled":
					v.Enabled = false
				case "visibility":
					v.Visibility = "global"
					v.TeamIDs = []string{}
				case "team_set":
					v.TeamIDs = []string{fTeam}
				case "kind":
					v.Kind = "other"
				case "empty_secrets":
					v.SecretKeys = []string{}
				}
				_ = json.NewEncoder(w).Encode(v)
			})
			state := valueState(t, s, voiceValues())
			planned := voiceValues()
			planned["display_name"], planned["enabled"], planned["visibility"] = types.StringValue("Committed name"), types.BoolValue(true), types.StringValue("team")
			planned["configuration"] = types.ObjectValueMust(voiceConfigurationTypes, map[string]attr.Value{"call_campaign_id": types.StringValue("committed-calls"), "verification_campaign_id": types.StringValue("committed-verify"), "max_text_runes": types.Int64Value(350)})
			planned["team_ids"] = types.SetValueMust(types.StringType, []attr.Value{types.StringValue(fTeam), types.StringValue(fOtherTeam)})
			planned["secrets"] = types.MapValueMust(voiceSecretObject, map[string]attr.Value{"public_key": types.ObjectValueMust(voiceSecretObject.AttrTypes, map[string]attr.Value{"action": types.StringValue("replace"), "value": types.StringValue("committed-secret-marker")})})
			p := valueState(t, s, planned)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || calls != 2 {
				t.Fatalf("mismatched follow-up was adopted: %v", resp.Diagnostics)
			}
			for _, field := range []string{"id", "kind", "display_name", "enabled", "visibility", "configuration", "team_ids", "secrets", "secret_keys", "callback_url"} {
				var got attr.Value
				resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root(field), &got)...)
				if got == nil || !got.Equal(planned[field]) {
					t.Fatalf("committed %s lost after %s mismatch", field, mode)
				}
			}
			var runtime, observed types.String
			resp.State.GetAttribute(t.Context(), path.Root("runtime_status"), &runtime)
			resp.State.GetAttribute(t.Context(), path.Root("runtime_observed_at"), &observed)
			if !runtime.IsNull() || !observed.IsNull() {
				t.Fatal("foreign computed metadata bound to committed configuration")
			}
		})
	}
}

func TestVoiceProviderResourceSubsequentMetadataUpdateUsesPreservedCommittedSettings(t *testing.T) {
	r := onpager.NewVoiceProviderResource()
	reads := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "PUT" {
			var in client.VoiceProviderInput
			if err := json.NewDecoder(q.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Configuration.CallCampaignID != "committed-calls" {
				t.Error("later metadata update replayed stale campaign configuration")
			}
			w.WriteHeader(204)
			return
		}
		reads++
		name, campaign := "Stale name", "calls"
		if reads > 1 {
			name, campaign = "Next metadata", "committed-calls"
		}
		_ = json.NewEncoder(w).Encode(client.VoiceProvider{ID: fService, Kind: "zvonok", DisplayName: name, Enabled: false, Visibility: "global", Configuration: client.VoiceProviderConfiguration{CallCampaignID: campaign, VerificationCampaignID: "verify", MaxTextRunes: 500}, TeamIDs: []string{}, SecretKeys: []string{"public_key"}, CallbackURL: "https://ingress.invalid/new-callback-marker", Runtime: client.VoiceProviderRuntime{State: "disabled"}})
	})
	state := valueState(t, s, voiceValues())
	planned := voiceValues()
	planned["display_name"] = types.StringValue("Committed name")
	cfg := planned["configuration"].(types.Object).Attributes()
	cfg["call_campaign_id"] = types.StringValue("committed-calls")
	planned["configuration"] = types.ObjectValueMust(voiceConfigurationTypes, cfg)
	p := valueState(t, s, planned)
	first := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &first)
	if !first.Diagnostics.HasError() {
		t.Fatal("stale read accepted")
	}
	var preserved types.Object
	first.State.GetAttribute(t.Context(), path.Root("configuration"), &preserved)
	planned["configuration"] = preserved
	planned["display_name"] = types.StringValue("Next metadata")
	p = valueState(t, s, planned)
	second := resource.UpdateResponse{State: first.State}
	r.Update(t.Context(), resource.UpdateRequest{State: first.State, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &second)
	if second.Diagnostics.HasError() || reads != 2 {
		t.Fatalf("matching follow-up rejected: %v", second.Diagnostics)
	}
	var callback types.String
	second.State.GetAttribute(t.Context(), path.Root("callback_url"), &callback)
	if callback.ValueString() != "https://ingress.invalid/new-callback-marker" {
		t.Fatal("matching follow-up did not populate computed metadata")
	}
}

func TestVoiceProviderResourceReadAbsenceAuthorityAndDestroyConflict(t *testing.T) {
	for _, status := range []int{403, 404, 409, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				w.WriteHeader(status)
				code := "forbidden"
				if status == 404 {
					code = "not_found"
				}
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"private-callback-marker"}}`, code)
			})
			state := valueState(t, s, voiceValues())
			read := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &read)
			if status == 404 {
				if read.Diagnostics.HasError() || !read.State.Raw.IsNull() {
					t.Fatal("confirmed absence retained")
				}
			} else if !read.Diagnostics.HasError() || !read.State.Raw.Equal(state.Raw) {
				t.Fatal("read failure discarded state")
			}
			var deleted resource.DeleteResponse
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &deleted)
			if deleted.Diagnostics.HasError() != (status != 404) {
				t.Fatal("failed retirement forgotten", deleted.Diagnostics)
			}
		})
	}
}

func TestVoiceProviderResourcePartialCreateUsesOnlyKnownCommittedIdentity(t *testing.T) {
	for _, mode := range []string{"partial", "wrong_type", "foreign_kind", "error"} {
		t.Run(mode, func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if mode == "error" {
					w.WriteHeader(403)
				} else {
					w.WriteHeader(201)
				}
				if mode == "foreign_kind" {
					fmt.Fprintf(w, `{"id":%q,"kind":"other"}`, fService)
				} else if mode == "wrong_type" {
					fmt.Fprintf(w, `{"id":%q,"enabled":"false"}`, fService)
				} else {
					fmt.Fprintf(w, `{"id":%q}`, fService)
				}
			})
			v := voiceValues()
			v["id"] = types.StringUnknown()
			p := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("incomplete create accepted")
			}
			if mode == "partial" || mode == "wrong_type" {
				var id types.String
				resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
				if id.ValueString() != fService {
					t.Fatal("committed UUID lost")
				}
			} else if !resp.State.Raw.IsNull() {
				t.Fatal("untrusted identity adopted")
			}
		})
	}
}

func TestVoiceProviderResourceFailedUpdateRetainsStateAndDoesNotRead(t *testing.T) {
	for _, status := range []int{403, 409, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "PUT" {
					t.Error("failed update caused another operation")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"forbidden"}}`)
			})
			state := valueState(t, s, voiceValues())
			v := voiceValues()
			v["display_name"] = types.StringValue("Rejected name")
			p := valueState(t, s, v)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) || calls != 1 {
				t.Fatal("failed update lost prior state or retried", resp.Diagnostics)
			}
		})
	}
}

func TestVoiceProviderProtocolSchemaProtectsWriteOnlyKeyAndCallback(t *testing.T) {
	server := providerserver.NewProtocol6(onpager.New("test")())()
	resp, err := server.GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatal(d.Summary, d.Detail)
		}
	}
	s := resp.ResourceSchemas["onpager_voice_provider"]
	if s == nil || s.Block == nil {
		t.Fatal("voice provider not registered")
	}
	callback, key := false, false
	for _, a := range s.Block.Attributes {
		if a.Name == "callback_url" {
			callback = a.Sensitive
		}
		if a.Name == "secrets" && a.NestedType != nil {
			for _, n := range a.NestedType.Attributes {
				if n.Name == "value" {
					key = n.Sensitive
				}
			}
		}
	}
	if !callback || !key {
		t.Fatal("callback or write-only key exposed without protocol sensitivity")
	}
}

func TestVoiceProviderResourceImportIsReadOnlyAndCannotRecoverSecrets(t *testing.T) {
	r := onpager.NewVoiceProviderResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" {
			t.Error("import mutated")
		}
		providerVoiceView(w, "Passive voice")
	})
	null := valueState(t, s, voiceValues())
	null.RemoveResource(t.Context())
	resp := resource.ImportStateResponse{State: null}
	r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: fService}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	read := resource.ReadResponse{State: resp.State}
	r.Read(t.Context(), resource.ReadRequest{State: resp.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var secrets types.Map
	var callback types.String
	read.State.GetAttribute(t.Context(), path.Root("secrets"), &secrets)
	read.State.GetAttribute(t.Context(), path.Root("callback_url"), &callback)
	if !secrets.IsNull() || !callback.IsNull() || calls != 1 {
		t.Fatal("import fabricated secrets/callback or made extra requests")
	}
	bad := resource.ImportStateResponse{State: null}
	r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: "not-a-uuid"}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Fatal("invalid import accepted")
	}
}

func TestVoiceProviderResourceValidationDefaultsUnknownAndInvalidValues(t *testing.T) {
	for _, mode := range []string{"defaults", "unknown", "zero", "campaign", "name", "visibility", "teams", "kind", "secret_key", "delete", "empty_secret", "missing_secret_value"} {
		t.Run(mode, func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("validation made a request") })
			v := voiceValues()
			cfg := v["configuration"].(types.Object).Attributes()
			switch mode {
			case "defaults":
				v["enabled"] = types.BoolNull()
				v["visibility"] = types.StringNull()
				cfg["max_text_runes"] = types.Int64Null()
				v["secrets"] = types.MapNull(voiceSecretObject)
			case "unknown":
				cfg["call_campaign_id"] = types.StringUnknown()
				v["secrets"] = types.MapUnknown(voiceSecretObject)
			case "zero":
				cfg["max_text_runes"] = types.Int64Value(0)
			case "campaign":
				cfg["call_campaign_id"] = types.StringValue(" calls ")
			case "name":
				v["display_name"] = types.StringValue(strings.Repeat("я", 101))
			case "visibility":
				v["visibility"] = types.StringValue("restricted")
			case "teams":
				v["team_ids"] = types.SetValueMust(types.StringType, []attr.Value{types.StringValue(fTeam)})
			case "kind":
				v["kind"] = types.StringValue("other")
			default:
				key, action, value := "public_key", "replace", types.StringValue("private-voice-marker")
				if mode == "secret_key" {
					key = "other"
				}
				if mode == "delete" {
					action = "delete"
					value = types.StringNull()
				}
				if mode == "empty_secret" {
					value = types.StringValue("")
				}
				if mode == "missing_secret_value" {
					value = types.StringNull()
				}
				v["secrets"] = types.MapValueMust(voiceSecretObject, map[string]attr.Value{key: types.ObjectValueMust(voiceSecretObject.AttrTypes, map[string]attr.Value{"action": types.StringValue(action), "value": value})})
			}
			v["configuration"] = types.ObjectValueMust(voiceConfigurationTypes, cfg)
			state := valueState(t, s, v)
			var resp resource.ValidateConfigResponse
			r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, &resp)
			if resp.Diagnostics.HasError() != (mode != "defaults" && mode != "unknown") {
				t.Fatalf("validation %s: %v", mode, resp.Diagnostics)
			}
			if strings.Contains(fmt.Sprint(resp.Diagnostics), "private-voice-marker") {
				t.Fatal("secret in diagnostics")
			}
			if mode == "unknown" {
				var created resource.CreateResponse
				created.State.Schema = s
				r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &created)
				if !created.Diagnostics.HasError() {
					t.Fatal("unknown apply allowed")
				}
			}
		})
	}
}

func TestVoiceProviderResourceMetadataUpdateOmitsUnchangedReplaceAndChangedKeyIsSent(t *testing.T) {
	for _, mode := range []string{"unchanged", "changed", "imported", "missing", "omitted", "keep"} {
		t.Run(mode, func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "GET" {
					providerVoiceView(w, "Changed label")
					return
				}
				var body struct {
					Secrets map[string]struct{ Action, Value string }
				}
				_ = json.NewDecoder(q.Body).Decode(&body)
				want := mode == "changed" || mode == "imported" || mode == "missing" || mode == "keep"
				if (len(body.Secrets) > 0) != want {
					t.Errorf("secret emission for %s: got keys=%d", mode, len(body.Secrets))
				}
				if mode == "changed" && body.Secrets["public_key"].Value != "new-key" {
					t.Error("changed replacement lost")
				}
				w.WriteHeader(204)
			})
			old, next := voiceValues(), voiceValues()
			next["display_name"] = types.StringValue("Changed label")
			switch mode {
			case "imported":
				old["secrets"] = types.MapNull(voiceSecretObject)
			case "missing":
				old["secret_keys"] = types.SetValueMust(types.StringType, []attr.Value{})
			case "omitted":
				next["secrets"] = types.MapNull(voiceSecretObject)
			case "changed", "keep":
				action, value := "replace", types.StringValue("new-key")
				if mode == "keep" {
					action = "keep"
					value = types.StringNull()
				}
				next["secrets"] = types.MapValueMust(voiceSecretObject, map[string]attr.Value{"public_key": types.ObjectValueMust(voiceSecretObject.AttrTypes, map[string]attr.Value{"action": types.StringValue(action), "value": value})})
			}
			state, plan := valueState(t, s, old), valueState(t, s, next)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
		})
	}
}

func TestVoiceProviderResourceCreateRequiresReplacementAndUnknownsNeverReachAPI(t *testing.T) {
	for _, mode := range []string{"omitted", "keep", "unknown_enabled", "unknown_configuration"} {
		t.Run(mode, func(t *testing.T) {
			r := onpager.NewVoiceProviderResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				t.Error("invalid create reached API")
				io.WriteString(w, "{}")
			})
			v := voiceValues()
			switch mode {
			case "omitted":
				v["secrets"] = types.MapNull(voiceSecretObject)
			case "keep":
				v["secrets"] = types.MapValueMust(voiceSecretObject, map[string]attr.Value{"public_key": types.ObjectValueMust(voiceSecretObject.AttrTypes, map[string]attr.Value{"action": types.StringValue("keep"), "value": types.StringNull()})})
			case "unknown_enabled":
				v["enabled"] = types.BoolUnknown()
			case "unknown_configuration":
				v["configuration"] = types.ObjectUnknown(voiceConfigurationTypes)
			}
			state := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid create accepted")
			}
		})
	}
}
