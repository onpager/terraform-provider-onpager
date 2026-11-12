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

var webhookPairType = types.ObjectType{AttrTypes: map[string]attr.Type{"key": types.StringType, "value": types.StringType}}
var webhookAuthType = types.ObjectType{AttrTypes: map[string]attr.Type{"kind": types.StringType, "username": types.StringType, "password_secret": types.StringType, "token_secret": types.StringType}}
var webhookRequestTypes = map[string]attr.Type{"method": types.StringType, "endpoint": types.StringType, "body_format": types.StringType, "body_template": types.StringType, "content_type": types.StringType, "verify_ssl": types.BoolType, "timeout_seconds": types.Int64Type, "query": types.ListType{ElemType: webhookPairType}, "headers": types.ListType{ElemType: webhookPairType}, "form": types.ListType{ElemType: webhookPairType}, "auth": webhookAuthType}
var webhookRecipientType = types.ObjectType{AttrTypes: map[string]attr.Type{"key": types.StringType, "label": types.StringType, "kind": types.StringType, "required": types.BoolType, "placeholder": types.StringType}}
var webhookSecretType = types.ObjectType{AttrTypes: map[string]attr.Type{"action": types.StringType, "value": types.StringType}}

func outgoingValues(team string) map[string]attr.Value {
	req := types.ObjectValueMust(webhookRequestTypes, map[string]attr.Value{"method": types.StringValue("POST"), "endpoint": types.StringValue("https://passive.invalid/hook"), "body_format": types.StringValue("json"), "body_template": types.StringValue("{}"), "content_type": types.StringValue(""), "verify_ssl": types.BoolValue(true), "timeout_seconds": types.Int64Value(5), "query": types.ListNull(webhookPairType), "headers": types.ListNull(webhookPairType), "form": types.ListNull(webhookPairType), "auth": types.ObjectValueMust(webhookAuthType.AttrTypes, map[string]attr.Value{"kind": types.StringValue("none"), "username": types.StringValue(""), "password_secret": types.StringValue(""), "token_secret": types.StringValue("")})})
	v := map[string]attr.Value{"id": types.StringValue(fService), "display_name": types.StringValue("Passive"), "purpose": types.StringValue("notification"), "enabled": types.BoolValue(false), "etag": types.StringValue(`"7"`), "runtime_status": types.StringValue("disabled"), "request_id": types.StringValue(fOtherTeam), "request": req, "recipient_fields": types.ListNull(webhookRecipientType), "secrets": types.MapValueMust(webhookSecretType, map[string]attr.Value{"token": types.ObjectValueMust(webhookSecretType.AttrTypes, map[string]attr.Value{"action": types.StringValue("replace"), "value": types.StringValue("private-value")})}), "secret_keys": types.ListValueMust(types.StringType, []attr.Value{types.StringValue("token")})}
	if team != "" {
		v["team_id"] = types.StringValue(team)
	}
	return v
}
func outgoingView(w http.ResponseWriter, team string) {
	w.Header().Set("ETag", `"7"`)
	scope := "null"
	if team != "" {
		scope = fmt.Sprintf("%q", team)
	}
	fmt.Fprintf(w, `{"id":%q,"scope":{"team_id":%s},"purpose":"notification","display_name":"Passive","enabled":false,"etag":"\"7\"","request":{"method":"POST","endpoint":"https://passive.invalid/hook","body_format":"json","body_template":"{}","verify_ssl":true,"timeout_seconds":5,"auth":{"kind":"none"}},"recipient_fields":[],"secrets":[{"key":"token","has_value":true}],"runtime_status":"disabled"}`, fService, scope)
}

func TestOutgoingWebhookResourceDirectLifecycleAndSecretPersistence(t *testing.T) {
	for _, team := range []string{"", fTeam} {
		t.Run(team, func(t *testing.T) {
			r := onpager.NewOutgoingWebhookResource()
			if team != "" {
				r = onpager.NewTeamOutgoingWebhookResource()
			}
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				prefix := "/v1/outgoing-webhooks"
				if team != "" {
					prefix = "/v1/teams/" + team + "/outgoing-webhooks"
				}
				want := prefix + "/" + fService
				if q.Method == "POST" {
					want = prefix
				}
				if q.URL.Path != want {
					t.Errorf("unexpected route %s", q.URL.Path)
				}
				if q.Method == "PUT" || q.Method == "DELETE" {
					if q.Header.Get("If-Match") != `"7"` {
						t.Error("wrong CAS")
					}
				}
				outgoingView(w, team)
			})
			p := valueState(t, s, outgoingValues(team))
			created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Config: tfsdk.Config{Schema: s, Raw: p.Raw}, Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}
			refreshed := resource.ReadResponse{State: created.State}
			r.Read(t.Context(), resource.ReadRequest{State: created.State}, &refreshed)
			if refreshed.Diagnostics.HasError() {
				t.Fatal(refreshed.Diagnostics)
			}
			var secret types.String
			refreshed.State.GetAttribute(t.Context(), path.Root("secrets").AtMapKey("token").AtName("value"), &secret)
			if secret.ValueString() != "private-value" {
				t.Fatal("refresh erased known secret")
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
			if calls != 4 {
				t.Fatalf("requests=%d", calls)
			}
		})
	}
}

func TestOutgoingWebhookResourceRetainsStateOnDeniedAndMalformedReads(t *testing.T) {
	for _, status := range []int{403, 404, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewOutgoingWebhookResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"code":"forbidden","message":"private-value"}}`)
			})
			state := valueState(t, s, outgoingValues(""))
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) || strings.Contains(fmt.Sprint(resp.Diagnostics), "private-value") {
				t.Fatalf("unsafe read result %v", resp.Diagnostics)
			}
		})
	}
}

func TestOutgoingWebhookResourceConfirmedAbsenceAndFailedRetirement(t *testing.T) {
	for _, status := range []int{404, 403, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewOutgoingWebhookResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				w.WriteHeader(status)
				code := "forbidden"
				if status == 404 {
					code = "not_found"
				}
				fmt.Fprintf(w, `{"error":{"code":%q}}`, code)
			})
			state := valueState(t, s, outgoingValues(""))
			read := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &read)
			if status == 404 {
				if read.Diagnostics.HasError() || !read.State.Raw.IsNull() {
					t.Fatal("confirmed absence retained")
				}
			} else if !read.Diagnostics.HasError() {
				t.Fatal("error swallowed")
			}
			var deleted resource.DeleteResponse
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &deleted)
			if deleted.Diagnostics.HasError() != (status != 404) {
				t.Fatal("retirement result lost")
			}
		})
	}
}

func TestOutgoingWebhookResourceCommittedPartialCreateAndUpdate(t *testing.T) {
	r := onpager.NewOutgoingWebhookResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("ETag", `"8"`)
		fmt.Fprintf(w, `{"id":%q,"etag":"\"8\""}`, fService)
	})
	state := valueState(t, s, outgoingValues(""))
	created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &created)
	var id types.String
	created.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if !created.Diagnostics.HasError() || id.ValueString() != fService {
		t.Fatal("committed create identity lost")
	}
	updated := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &updated)
	updated.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if !updated.Diagnostics.HasError() || id.ValueString() != fService {
		t.Fatal("committed update identity lost")
	}
}

func TestOutgoingWebhookResourceSecretActionsAndInvalidPlans(t *testing.T) {
	r := onpager.NewOutgoingWebhookResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		var b struct {
			Secrets map[string]struct{ Action, Value string }
		}
		json.NewDecoder(q.Body).Decode(&b)
		if b.Secrets["token"].Action != "delete" || b.Secrets["token"].Value != "" {
			t.Error("explicit delete action lost")
		}
		outgoingView(w, "")
	})
	v := outgoingValues("")
	v["secrets"] = types.MapValueMust(webhookSecretType, map[string]attr.Value{"token": types.ObjectValueMust(webhookSecretType.AttrTypes, map[string]attr.Value{"action": types.StringValue("delete"), "value": types.StringNull()})})
	state := valueState(t, s, v)
	resp := resource.UpdateResponse{State: state}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	v["purpose"] = types.StringValue("escalation")
	v["recipient_fields"] = types.ListValueMust(webhookRecipientType, []attr.Value{types.ObjectValueMust(webhookRecipientType.AttrTypes, map[string]attr.Value{"key": types.StringValue("mail"), "label": types.StringValue("Email"), "kind": types.StringValue("email"), "required": types.BoolValue(true), "placeholder": types.StringValue("")})})
	invalid := valueState(t, s, v)
	create := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Config: tfsdk.Config{Schema: s, Raw: invalid.Raw}, Plan: tfsdk.Plan{Schema: s, Raw: invalid.Raw}}, &create)
	if !create.Diagnostics.HasError() || calls != 1 {
		t.Fatal("invalid escalation fields reached API")
	}
}

func TestOutgoingWebhookResourceImportsSecretsReadOnly(t *testing.T) {
	r := onpager.NewTeamOutgoingWebhookResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" {
			t.Error("import mutated webhook")
		}
		outgoingView(w, fTeam)
	})
	nullState := valueState(t, s, outgoingValues(fTeam))
	nullState.RemoveResource(t.Context())
	imported := resource.ImportStateResponse{State: nullState}
	r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: fTeam + "/" + fService}, &imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
	read := resource.ReadResponse{State: imported.State}
	r.Read(t.Context(), resource.ReadRequest{State: imported.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var secrets types.Map
	read.State.GetAttribute(t.Context(), path.Root("secrets"), &secrets)
	if !secrets.IsNull() || calls != 1 {
		t.Fatal("import fabricated secret or performed extra I/O")
	}
}

func TestOutgoingWebhookConfigRejectsInvalidTimeoutAndMissingReplacementValue(t *testing.T) {
	for _, mode := range []string{"timeout", "secret"} {
		t.Run(mode, func(t *testing.T) {
			r := onpager.NewOutgoingWebhookResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("validation made HTTP request") })
			v := outgoingValues("")
			if mode == "timeout" {
				a := v["request"].(types.Object).Attributes()
				a["timeout_seconds"] = types.Int64Value(0)
				v["request"] = types.ObjectValueMust(webhookRequestTypes, a)
			} else {
				v["secrets"] = types.MapValueMust(webhookSecretType, map[string]attr.Value{"token": types.ObjectValueMust(webhookSecretType.AttrTypes, map[string]attr.Value{"action": types.StringValue("replace"), "value": types.StringNull()})})
			}
			state := valueState(t, s, v)
			var resp resource.ValidateConfigResponse
			r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestOutgoingWebhookConfigAllowsOmittedOptionalDefaults(t *testing.T) {
	r := onpager.NewOutgoingWebhookResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Fatal("validation made HTTP request") })
	v := outgoingValues("")
	a := v["request"].(types.Object).Attributes()
	for _, k := range []string{"method", "body_format", "body_template", "content_type"} {
		a[k] = types.StringNull()
	}
	a["timeout_seconds"] = types.Int64Null()
	a["verify_ssl"] = types.BoolNull()
	a["auth"] = types.ObjectNull(webhookAuthType.AttrTypes)
	v["request"] = types.ObjectValueMust(webhookRequestTypes, a)
	v["enabled"] = types.BoolNull()
	v["secrets"] = types.MapNull(webhookSecretType)
	state := valueState(t, s, v)
	var resp resource.ValidateConfigResponse
	r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: state.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}
