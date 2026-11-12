package provider_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

var integrationPollTypes = map[string]attr.Type{
	"enabled": types.BoolType, "base_url": types.StringType, "interval_seconds": types.Int64Type,
	"auth_kind": types.StringType, "username": types.StringType, "password": types.StringType,
	"token": types.StringType, "has_secret": types.BoolType,
}

func integrationValues() map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(fTeam), "name": types.StringValue("Metrics"), "source_kind": types.StringValue("alertmanager"),
		"enabled": types.BoolValue(true), "revision": types.Int64Value(1), "token": types.StringValue("one-time-secret-fixture"),
		"webhook_path": types.StringValue("/hooks/fixture"), "webhook_url": types.StringValue("http://127.0.0.1:8081/hooks/fixture"),
		"polling": types.ObjectNull(integrationPollTypes),
	}
}

func TestIntegrationReadRetainsOneTimeAndPollingSecrets(t *testing.T) {
	r := onpager.NewIntegrationResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" || req.URL.Path != "/v1/integrations/"+fTeam {
			t.Error("integration refresh must only use its detail read")
		}
		io.WriteString(w, `{"id":"`+fTeam+`","name":"Metrics","source_kind":"alertmanager","revision":2,"enabled":true,"webhook_path":"/hooks/fixture","webhook_url":"http://127.0.0.1:8081/hooks/fixture","polling":{"enabled":false,"base_url":"http://127.0.0.1:19090","interval_seconds":60,"auth_kind":"basic","username":"metrics","has_secret":true}}`)
	})
	v := integrationValues()
	v["polling"] = types.ObjectValueMust(integrationPollTypes, map[string]attr.Value{
		"enabled": types.BoolValue(false), "base_url": types.StringValue("http://127.0.0.1:19090/"),
		"interval_seconds": types.Int64Value(60), "auth_kind": types.StringValue("basic"), "username": types.StringValue("metrics"),
		"password": types.StringValue("saved-polling-fixture"), "token": types.StringNull(), "has_secret": types.BoolValue(true),
	})
	state := valueState(t, s, v)
	resp := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var token, password, base types.String
	resp.State.GetAttribute(t.Context(), path.Root("token"), &token)
	resp.State.GetAttribute(t.Context(), path.Root("polling").AtName("password"), &password)
	resp.State.GetAttribute(t.Context(), path.Root("polling").AtName("base_url"), &base)
	if token.ValueString() != "one-time-secret-fixture" || password.ValueString() != "saved-polling-fixture" || base.ValueString() != "http://127.0.0.1:19090/" {
		t.Fatal("refresh lost secrets or changed an equivalent configured URL")
	}
}

func TestIntegrationCreateKeepsIdentityAndTokenIfDisableFails(t *testing.T) {
	r := onpager.NewIntegrationResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" {
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"`+fTeam+`","name":"Metrics","source_kind":"alertmanager","revision":1,"enabled":true,"webhook_path":"/hooks/fixture","token":"one-time-secret-fixture"}`)
			return
		}
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"code":"forbidden"}}`)
	})
	v := integrationValues()
	v["id"], v["token"], v["revision"], v["enabled"] = types.StringUnknown(), types.StringUnknown(), types.Int64Unknown(), types.BoolValue(false)
	state := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("failed disable was swallowed")
	}
	var id, token types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	resp.State.GetAttribute(t.Context(), path.Root("token"), &token)
	if id.ValueString() != fTeam || token.ValueString() != "one-time-secret-fixture" {
		t.Fatal("create partial success lost allocated identity or token")
	}
}

func TestIntegrationCreateKeepsKnownUUIDWhenReceiptIsIncomplete(t *testing.T) {
	r := onpager.NewIntegrationResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(201)
		io.WriteString(w, `{"id":"`+fTeam+`","token":"one-time-fixture"}`)
	})
	v := integrationValues()
	v["id"], v["token"], v["revision"] = types.StringUnknown(), types.StringUnknown(), types.Int64Unknown()
	state := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("incomplete receipt was not diagnosed")
	}
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if id.ValueString() != fTeam {
		t.Fatal("successful create's known UUID was lost after incomplete receipt")
	}
}

func TestIntegrationPollingRequiresExplicitURLWhileDisabled(t *testing.T) {
	for name, url := range map[string]types.String{"empty": types.StringValue(""), "omitted": types.StringNull(), "unknown": types.StringUnknown(), "configured": types.StringValue("http://127.0.0.1:19090/")} {
		t.Run(name, func(t *testing.T) {
			r := onpager.NewIntegrationResource()
			var sr resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
			v := integrationValues()
			v["polling"] = types.ObjectValueMust(integrationPollTypes, map[string]attr.Value{
				"enabled": types.BoolValue(false), "base_url": url, "interval_seconds": types.Int64Value(60),
				"auth_kind": types.StringValue("none"), "username": types.StringValue(""),
				"password": types.StringNull(), "token": types.StringNull(), "has_secret": types.BoolUnknown(),
			})
			state := valueState(t, sr.Schema, v)
			var resp resource.ValidateConfigResponse
			r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: state.Raw}}, &resp)
			wantError := name == "empty" || name == "omitted"
			if resp.Diagnostics.HasError() != wantError {
				t.Fatalf("disabled polling URL validation error=%t, want %t", resp.Diagnostics.HasError(), wantError)
			}
		})
	}
}
