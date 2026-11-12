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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func templateValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue("alert_direct/telegram/en"), "type": types.StringValue("alert_direct"), "delivery_provider": types.StringValue("telegram"), "language": types.StringValue("en"), "format": types.StringValue("telegram_html"), "body": types.StringValue("Custom"), "etag": types.StringValue(`"` + strings.Repeat("a", 64) + `"`)}
}

func TestNotificationTemplateImportsCurrentLanguageSpecificSlots(t *testing.T) {
	for _, tc := range []struct {
		name string
		team bool
		id   string
	}{
		{"global bot EN", false, "alert_direct/telegram/en"},
		{"global voice RU", false, "responder_request/zvonok/ru"},
		{"team voice EN", true, fTeam + "/alert_direct/zvonok/en"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewNotificationTemplateResource()
			if tc.team {
				r = onpager.NewTeamNotificationTemplateResource()
			}
			var sr resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: tc.id}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("current slot %s rejected: %v", tc.id, resp.Diagnostics)
			}
			var id, deliveryProvider, language types.String
			resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
			resp.State.GetAttribute(t.Context(), path.Root("delivery_provider"), &deliveryProvider)
			resp.State.GetAttribute(t.Context(), path.Root("language"), &language)
			parts := strings.Split(tc.id, "/")
			if id.ValueString() != tc.id || deliveryProvider.ValueString() != parts[len(parts)-2] || language.ValueString() != parts[len(parts)-1] {
				t.Fatal("import did not retain the complete addressed slot")
			}
		})
	}
}

func TestNotificationTemplateRejectsInvalidLanguageSpecificImports(t *testing.T) {
	for _, team := range []bool{false, true} {
		r := onpager.NewNotificationTemplateResource()
		prefix := ""
		if team {
			r = onpager.NewTeamNotificationTemplateResource()
			prefix = fTeam + "/"
		}
		var sr resource.SchemaResponse
		r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		ids := []string{"alert_direct/telegram", "alert_direct/telegram/", "alert_direct/telegram/EN", "alert_direct/telegram/fr", "responder_digest/telegram/en", "alert_channel/zvonok/en", "schedule_handover/zvonok/ru", "alert_direct/zvonok/en/extra"}
		if team {
			ids = append(ids, "incident_call/zvonok/en", "responder_feedback/zvonok/ru")
		}
		for _, id := range ids {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema}}
			r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: prefix + id}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("team=%t invalid slot %q accepted", team, id)
			}
		}
	}
}

func TestNotificationTemplateInvalidDesiredSlotDoesNotRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		field    string
		value    types.String
		kind     string
		provider string
	}{
		{"missing language", "language", types.StringNull(), "alert_direct", "telegram"},
		{"unknown language", "language", types.StringUnknown(), "alert_direct", "telegram"},
		{"unsupported language", "language", types.StringValue("fr"), "alert_direct", "telegram"},
		{"uppercase language", "language", types.StringValue("EN"), "alert_direct", "telegram"},
		{"retired type", "type", types.StringValue("responder_digest"), "alert_direct", "telegram"},
		{"voice channel type", "type", types.StringValue("alert_channel"), "alert_direct", "zvonok"},
		{"voice format", "format", types.StringValue("telegram_html"), "alert_direct", "zvonok"},
		{"blank source", "body", types.StringValue(" \n\t"), "alert_direct", "zvonok"},
		{"NUL source", "body", types.StringValue("hello\x00"), "alert_direct", "zvonok"},
		{"invalid UTF-8 source", "body", types.StringValue(string([]byte{0xff})), "alert_direct", "zvonok"},
		{"over byte limit", "body", types.StringValue(strings.Repeat("я", 32769)), "alert_direct", "zvonok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewNotificationTemplateResource()
			calls := 0
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { calls++ })
			v := templateValues()
			v["id"], v["etag"] = types.StringUnknown(), types.StringUnknown()
			v["type"], v["delivery_provider"], v["format"] = types.StringValue(tc.kind), types.StringValue(tc.provider), types.StringValue("plain")
			v[tc.field] = tc.value
			plan := valueState(t, s, v)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || calls != 0 {
				t.Fatalf("invalid desired slot caused %d requests: %v", calls, resp.Diagnostics)
			}
		})
	}
}

func TestNotificationTemplateChangedProviderOrLanguageRequiresReplacement(t *testing.T) {
	for _, field := range []string{"language", "delivery_provider"} {
		t.Run(field, func(t *testing.T) {
			r := onpager.NewNotificationTemplateResource()
			calls := 0
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { calls++ })
			v := templateValues()
			v["format"] = types.StringValue("plain")
			state := valueState(t, s, v)
			v["id"], v["etag"] = types.StringUnknown(), types.StringUnknown()
			if field == "language" {
				v[field] = types.StringValue("ru")
			} else {
				v[field] = types.StringValue("zvonok")
			}
			plan := valueState(t, s, v)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || calls != 0 || !resp.State.Raw.Equal(state.Raw) {
				t.Fatalf("identity change overwrote original slot: %v calls=%d", resp.Diagnostics, calls)
			}
		})
	}
}

func TestNotificationTemplateIdentityChangesPlanReplacement(t *testing.T) {
	for _, team := range []bool{false, true} {
		r := onpager.NewNotificationTemplateResource()
		values := templateValues()
		changed := map[string]string{"type": "alert_channel", "delivery_provider": "slack", "language": "ru"}
		if team {
			r = onpager.NewTeamNotificationTemplateResource()
			values["team_id"], values["id"] = types.StringValue(fTeam), types.StringValue(fTeam+"/alert_direct/telegram/en")
			changed["team_id"] = fOtherTeam
		}
		var sr resource.SchemaResponse
		r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		state := valueState(t, sr.Schema, values)
		for field, next := range changed {
			attribute := sr.Schema.Attributes[field].(schema.StringAttribute)
			replace := false
			for _, modifier := range attribute.PlanModifiers {
				var resp planmodifier.StringResponse
				modifier.PlanModifyString(t.Context(), planmodifier.StringRequest{State: state, Plan: tfsdk.Plan{Schema: sr.Schema, Raw: state.Raw}, StateValue: values[field].(types.String), PlanValue: types.StringValue(next)}, &resp)
				if resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
				replace = replace || resp.RequiresReplace
			}
			if !replace {
				t.Fatalf("team=%t changing %s planned an in-place write", team, field)
			}
		}
	}
}

func TestNotificationTemplateVoiceSourceAllowsExact64KiB(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	body := strings.Repeat("я", 32768)
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.URL.Path != "/v1/notification-templates/alert_direct/zvonok/ru" {
			t.Fatalf("wrong voice slot: %s", q.URL.Path)
		}
		w.Header().Set("ETag", `"`+strings.Repeat("a", 64)+`"`)
		if q.Method == "GET" {
			io.WriteString(w, `{"override":null,"effective":{"format":"plain","body":"Inherited"},"source":"builtin"}`)
			return
		}
		var draft map[string]string
		if err := json.NewDecoder(q.Body).Decode(&draft); err != nil || draft["body"] != body || draft["format"] != "plain" {
			t.Fatal("source did not survive encoding")
		}
		json.NewEncoder(w).Encode(map[string]any{"override": draft, "effective": draft, "source": "global"})
	})
	v := templateValues()
	v["id"], v["etag"] = types.StringUnknown(), types.StringUnknown()
	v["delivery_provider"], v["language"], v["format"], v["body"] = types.StringValue("zvonok"), types.StringValue("ru"), types.StringValue("plain"), types.StringValue(body)
	plan := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
	var saved types.String
	resp.State.GetAttribute(t.Context(), path.Root("body"), &saved)
	if resp.Diagnostics.HasError() || calls != 2 || saved.ValueString() != body {
		t.Fatalf("valid source truncated or rejected: %v calls=%d", resp.Diagnostics, calls)
	}
}

func TestNotificationTemplateBotAndVoiceLanguageSpecificCRUD(t *testing.T) {
	for _, team := range []bool{false, true} {
		for _, deliveryProvider := range []string{"telegram", "zvonok"} {
			for _, language := range []string{"ru", "en"} {
				t.Run(fmt.Sprintf("%t/%s/%s", team, deliveryProvider, language), func(t *testing.T) {
					r := onpager.NewNotificationTemplateResource()
					prefix, source := "", "global"
					if team {
						r = onpager.NewTeamNotificationTemplateResource()
						prefix, source = fTeam+"/", "team"
					}
					body := "  Пример\n" + strings.Repeat("speech ", 90) + "  "
					id := prefix + "alert_direct/" + deliveryProvider + "/" + language
					requestPath := "/v1/notification-templates/alert_direct/" + deliveryProvider + "/" + language
					if team {
						requestPath = "/v1/teams/" + fTeam + "/notification-templates/alert_direct/" + deliveryProvider + "/" + language
					}
					stored := false
					calls := 0
					s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
						calls++
						if q.URL.Path != requestPath || q.URL.RawQuery != "" {
							t.Fatalf("request targeted another slot: %s", q.URL.String())
						}
						w.Header().Set("ETag", `"`+strings.Repeat("a", 64)+`"`)
						w.Header().Set("Cache-Control", "no-store")
						if q.Method != "GET" && q.Header.Get("If-Match") != `"`+strings.Repeat("a", 64)+`"` {
							t.Fatal("slot CAS missing")
						}
						if q.Method == "PUT" {
							var draft map[string]string
							if err := json.NewDecoder(q.Body).Decode(&draft); err != nil || len(draft) != 2 || draft["format"] != "plain" || draft["body"] != body {
								t.Fatalf("exact source changed: %v %v", draft, err)
							}
							stored = true
						} else if q.Method == "DELETE" {
							stored = false
						} else if q.Method != "GET" {
							t.Fatalf("unexpected method %s", q.Method)
						}
						var override any
						effective, selectedSource := map[string]string{"format": "plain", "body": "Inherited"}, "builtin"
						if stored {
							effective = map[string]string{"format": "plain", "body": body}
							override, selectedSource = effective, source
						}
						json.NewEncoder(w).Encode(map[string]any{"override": override, "effective": effective, "source": selectedSource, "allowed_actions": map[string]bool{"read": true, "update": true, "delete": true, "preview": true, "test": false}})
					})
					v := templateValues()
					v["delivery_provider"], v["language"], v["format"], v["body"] = types.StringValue(deliveryProvider), types.StringValue(language), types.StringValue("plain"), types.StringValue(body)
					v["id"], v["etag"] = types.StringUnknown(), types.StringUnknown()
					if team {
						v["team_id"] = types.StringValue(fTeam)
					}
					plan := valueState(t, s, v)
					created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
					r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &created)
					if created.Diagnostics.HasError() {
						t.Fatal(created.Diagnostics)
					}
					var savedID, savedBody types.String
					created.State.GetAttribute(t.Context(), path.Root("id"), &savedID)
					created.State.GetAttribute(t.Context(), path.Root("body"), &savedBody)
					if savedID.ValueString() != id || savedBody.ValueString() != body {
						t.Fatal("saved slot/source changed")
					}
					read := resource.ReadResponse{State: created.State}
					r.Read(t.Context(), resource.ReadRequest{State: created.State}, &read)
					updated := resource.UpdateResponse{State: read.State}
					r.Update(t.Context(), resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}, State: read.State}, &updated)
					var deleted resource.DeleteResponse
					r.Delete(t.Context(), resource.DeleteRequest{State: updated.State}, &deleted)
					if read.Diagnostics.HasError() || updated.Diagnostics.HasError() || deleted.Diagnostics.HasError() || stored || calls != 6 {
						t.Fatalf("CRUD failed: %v %v %v calls=%d", read.Diagnostics, updated.Diagnostics, deleted.Diagnostics, calls)
					}
				})
			}
		}
	}
}
func templateView(w http.ResponseWriter, override bool) {
	w.Header().Set("ETag", `"`+strings.Repeat("a", 64)+`"`)
	if !override {
		io.WriteString(w, `{"override":null,"effective":{"format":"telegram_html","body":"Inherited"},"source":"builtin"}`)
		return
	}
	io.WriteString(w, `{"override":{"format":"telegram_html","body":"Custom"},"effective":{"format":"telegram_html","body":"Custom"},"source":"global"}`)
}
func TestNotificationTemplateCRUDUsesScopedOverrideAndETag(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.URL.Path != "/v1/notification-templates/alert_direct/telegram/en" || q.URL.RawQuery != "" {
			t.Fatalf("unexpected %s", q.URL.String())
		}
		if q.Method != "GET" && q.Header.Get("If-Match") != `"`+strings.Repeat("a", 64)+`"` {
			t.Error("ETag missing")
		}
		templateView(w, q.Method != "DELETE")
	})
	values := templateValues()
	values["id"] = types.StringUnknown()
	values["etag"] = types.StringUnknown()
	p := valueState(t, s, values)
	created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &created)
	if created.Diagnostics.HasError() || calls != 2 {
		t.Fatalf("create: %v calls=%d", created.Diagnostics, calls)
	}
	read := resource.ReadResponse{State: created.State}
	r.Read(t.Context(), resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() || read.State.Raw.IsNull() {
		t.Fatalf("read: %v", read.Diagnostics)
	}
	updated := resource.UpdateResponse{State: read.State}
	r.Update(t.Context(), resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, State: read.State}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	var deleted resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: updated.State}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
}
func TestNotificationTemplateOnlyExplicitNullOverrideRemovesState(t *testing.T) {
	for _, status := range []int{200, 403, 404, 500} {
		r := onpager.NewNotificationTemplateResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			if q.Method != "GET" {
				t.Fatal("refresh mutated")
			}
			if status == 200 {
				templateView(w, false)
				return
			}
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"not_found"}}`)
		})
		state := valueState(t, s, templateValues())
		resp := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
		if status == 200 {
			if !resp.State.Raw.IsNull() || resp.Diagnostics.HasError() {
				t.Fatal("inherited template retained as managed override")
			}
		} else if resp.State.Raw.IsNull() || !resp.Diagnostics.HasError() {
			t.Fatalf("HTTP%d removed state or swallowed error", status)
		}
	}
}
func TestNotificationTemplateImportStrictScopedIdentity(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	for _, id := range []string{"alert_direct/telegram/en", fTeam + "/alert_direct/telegram/en", "../telegram", "responder_request/telegram/en", fTeam + "/responder_request/telegram/en", "alert_direct/unknown/en", "ALERT_DIRECT/telegram/en"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		valid := id == "alert_direct/telegram/en" || id == "responder_request/telegram/en"
		if resp.Diagnostics.HasError() == valid {
			t.Fatalf("id=%s diagnostics=%v", id, resp.Diagnostics)
		}
	}
}
func TestNotificationTemplateCommittedMalformedPutKeepsSlotIdentity(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method == "GET" {
			templateView(w, false)
			return
		}
		w.Header().Set("ETag", `"`+strings.Repeat("b", 64)+`"`)
		io.WriteString(w, `{}`)
	})
	v := templateValues()
	v["id"] = types.StringUnknown()
	v["etag"] = types.StringUnknown()
	p := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if !resp.Diagnostics.HasError() || id.ValueString() != "alert_direct/telegram/en" || calls != 2 {
		t.Fatal("committed slot lost")
	}
}
func TestNotificationTemplateUnknownPlanDoesNotSendRequests(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	calls := 0
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { calls++ })
	v := templateValues()
	v["body"] = types.StringUnknown()
	p := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || calls != 0 {
		t.Fatal("unknown desired body caused request")
	}
}

func TestNotificationTemplateConflictAndFailedResetRetainState(t *testing.T) {
	for _, status := range []int{403, 404, 412, 428} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewNotificationTemplateResource()
			mutations := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "GET" {
					templateView(w, true)
					return
				}
				mutations++
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"code":"precondition_failed"}}`)
			})
			state := valueState(t, s, templateValues())
			updated := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &updated)
			if !updated.Diagnostics.HasError() || !updated.State.Raw.Equal(state.Raw) {
				t.Fatal("update failure changed state")
			}
			var deleted resource.DeleteResponse
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &deleted)
			if !deleted.Diagnostics.HasError() || mutations != 2 {
				t.Fatal("failed reset became successful or retried")
			}
		})
	}
}
func TestNotificationTemplateMalformedReadDoesNotEraseOverride(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("ETag", `"`+strings.Repeat("a", 64)+`"`)
		io.WriteString(w, `{"effective":{"format":"telegram_html","body":"Inherited"},"source":"builtin"}`)
	})
	state := valueState(t, s, templateValues())
	resp := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
		t.Fatal("omitted override was mistaken for explicit absence")
	}
}
func TestNotificationTemplateAlreadyResetDestroyUsesReadOnly(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" {
			t.Error("already absent override was mutated")
		}
		templateView(w, false)
	})
	state := valueState(t, s, templateValues())
	var resp resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || calls != 1 {
		t.Fatalf("repeated deletion: %v calls=%d", resp.Diagnostics, calls)
	}
}
func TestNotificationTemplateRejectsMismatchedFormat(t *testing.T) {
	r := onpager.NewNotificationTemplateResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("validation performed I/O") })
	v := templateValues()
	v["format"] = types.StringValue("slack_mrkdwn")
	state := valueState(t, s, v)
	var resp resource.ValidateConfigResponse
	r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("format incompatible with selected delivery provider accepted")
	}
}

func TestNotificationTemplateTeamScopedCRUDAndImport(t *testing.T) {
	r := onpager.NewTeamNotificationTemplateResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.URL.Path != "/v1/teams/"+fTeam+"/notification-templates/alert_direct/telegram/en" {
			t.Fatalf("wrong scoped route %s", q.URL.Path)
		}
		if q.Method != "GET" && q.Header.Get("If-Match") != `"`+strings.Repeat("a", 64)+`"` {
			t.Fatal("slot CAS missing")
		}
		w.Header().Set("ETag", `"`+strings.Repeat("a", 64)+`"`)
		if q.Method == "DELETE" {
			io.WriteString(w, `{"override":null,"effective":{"format":"telegram_html","body":"Inherited"},"source":"global"}`)
			return
		}
		io.WriteString(w, `{"override":{"format":"telegram_html","body":"Custom"},"effective":{"format":"telegram_html","body":"Custom"},"source":"team"}`)
	})
	v := templateValues()
	v["team_id"] = types.StringValue(fTeam)
	v["id"] = types.StringUnknown()
	v["etag"] = types.StringUnknown()
	p := valueState(t, s, v)
	created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	read := resource.ReadResponse{State: created.State}
	r.Read(t.Context(), resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	updated := resource.UpdateResponse{State: read.State}
	r.Update(t.Context(), resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}, State: read.State}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	var deleted resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: updated.State}, &deleted)
	if deleted.Diagnostics.HasError() || calls != 6 {
		t.Fatalf("delete: %v calls=%d", deleted.Diagnostics, calls)
	}
	for _, id := range []string{fTeam + "/alert_direct/telegram/en", "alert_direct/telegram/en", fTeam + "/responder_request/telegram/en", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA/alert_direct/telegram/en"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() == (id == fTeam+"/alert_direct/telegram/en") {
			t.Fatalf("import %s: %v", id, resp.Diagnostics)
		}
	}
}

func TestNotificationTemplateTeamDeniedReadAndResetRetainState(t *testing.T) {
	for _, status := range []int{403, 404, 412, 428} {
		r := onpager.NewTeamNotificationTemplateResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"not_found"}}`)
		})
		v := templateValues()
		v["team_id"] = types.StringValue(fTeam)
		v["id"] = types.StringValue(fTeam + "/alert_direct/telegram/en")
		state := valueState(t, s, v)
		resp := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
		if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
			t.Fatalf("HTTP%d erased state", status)
		}
		var del resource.DeleteResponse
		r.Delete(t.Context(), resource.DeleteRequest{State: state}, &del)
		if !del.Diagnostics.HasError() {
			t.Fatalf("HTTP%d reset succeeded", status)
		}
	}
}

func TestNotificationTemplateTeamNullAndMalformedResponses(t *testing.T) {
	for _, body := range []string{
		`{"override":null,"effective":{"format":"telegram_html","body":"Inherited"},"source":"global"}`,
		`{"effective":{"format":"telegram_html","body":"Inherited"},"source":"global"}`,
		`{"override":{"format":"telegram_html","body":"Custom"},"effective":{"format":"telegram_html","body":"Custom"},"source":"global"}`,
	} {
		r := onpager.NewTeamNotificationTemplateResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			if q.Method != "GET" {
				t.Fatal("refresh mutated")
			}
			w.Header().Set("ETag", `"`+strings.Repeat("a", 64)+`"`)
			io.WriteString(w, body)
		})
		v := templateValues()
		v["team_id"] = types.StringValue(fTeam)
		v["id"] = types.StringValue(fTeam + "/alert_direct/telegram/en")
		state := valueState(t, s, v)
		resp := resource.ReadResponse{State: state}
		r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
		if strings.HasPrefix(body, `{"override":null`) {
			if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
				t.Fatal("explicit inheritance absence not removed")
			}
		} else if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
			t.Fatal("malformed team view lost saved override")
		}
	}
}

func TestNotificationTemplateTeamCommittedWritePreservesSlotAndUnknownInputsDoNotRequest(t *testing.T) {
	for _, field := range []string{"", "team_id", "body", "language", "delivery_provider"} {
		r := onpager.NewTeamNotificationTemplateResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			w.Header().Set("ETag", `"`+strings.Repeat("b", 64)+`"`)
			if q.Method == "GET" {
				io.WriteString(w, `{"override":null,"effective":{"format":"telegram_html","body":"Inherited"},"source":"global"}`)
				return
			}
			io.WriteString(w, `{}`)
		})
		v := templateValues()
		v["team_id"] = types.StringValue(fTeam)
		v["id"] = types.StringUnknown()
		v["etag"] = types.StringUnknown()
		if field != "" {
			v[field] = types.StringUnknown()
		}
		p := valueState(t, s, v)
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("invalid/unknown write succeeded")
		}
		if field != "" {
			if calls != 0 {
				t.Fatal("unknown input caused request")
			}
			continue
		}
		var id types.String
		var tag types.String
		resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
		resp.State.GetAttribute(t.Context(), path.Root("etag"), &tag)
		if calls != 2 || id.ValueString() != fTeam+"/alert_direct/telegram/en" || tag.ValueString() != `"`+strings.Repeat("b", 64)+`"` {
			t.Fatal("committed team slot lost")
		}
	}
}

func TestNotificationTemplateTeamImmutableOwnerAndInconsistentIdentityDoNotWrite(t *testing.T) {
	for _, changed := range []string{"team_id", "id"} {
		r := onpager.NewTeamNotificationTemplateResource()
		s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("changed slot caused request") })
		v := templateValues()
		v["team_id"] = types.StringValue(fTeam)
		v["id"] = types.StringValue(fTeam + "/alert_direct/telegram/en")
		state := valueState(t, s, v)
		if changed == "id" {
			v[changed] = types.StringValue(fOtherTeam + "/alert_direct/telegram/en")
		} else {
			v[changed] = types.StringValue(fOtherTeam)
		}
		p := valueState(t, s, v)
		resp := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
		if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
			t.Fatal("changed identity accepted")
		}
	}
}

func TestNotificationTemplateTeamStaleWritesAndFailedResetUseSavedETag(t *testing.T) {
	for _, status := range []int{403, 404, 412, 428} {
		r := onpager.NewTeamNotificationTemplateResource()
		mutations := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			if q.Method == "GET" {
				w.Header().Set("ETag", `"`+strings.Repeat("b", 64)+`"`)
				io.WriteString(w, `{"override":{"format":"telegram_html","body":"Custom"},"effective":{"format":"telegram_html","body":"Custom"},"source":"team"}`)
				return
			}
			mutations++
			if q.Header.Get("If-Match") != `"`+strings.Repeat("a", 64)+`"` {
				t.Fatal("provider replaced saved CAS with new observation")
			}
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"precondition_failed"}}`)
		})
		v := templateValues()
		v["team_id"] = types.StringValue(fTeam)
		v["id"] = types.StringValue(fTeam + "/alert_direct/telegram/en")
		state := valueState(t, s, v)
		updated := resource.UpdateResponse{State: state}
		r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &updated)
		if !updated.Diagnostics.HasError() || !updated.State.Raw.Equal(state.Raw) {
			t.Fatal("failed update changed state")
		}
		var deleted resource.DeleteResponse
		r.Delete(t.Context(), resource.DeleteRequest{State: state}, &deleted)
		if !deleted.Diagnostics.HasError() || mutations != 2 {
			t.Fatal("failed reset replayed or succeeded")
		}
	}
}
