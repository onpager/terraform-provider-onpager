package provider_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func notificationDefaultsResource(t *testing.T) resource.Resource {
	t.Helper()
	for _, newResource := range onpager.New("test")().Resources(t.Context()) {
		r := newResource()
		var meta resource.MetadataResponse
		r.Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "onpager"}, &meta)
		if meta.TypeName == "onpager_user_notification_defaults" {
			return r
		}
	}
	t.Fatal("user notification defaults resource missing from provider")
	return nil
}

func notificationDefaultsTestState(t *testing.T, schema schema.Schema, enabled bool) tfsdk.State {
	t.Helper()
	data := fmt.Sprintf(`{"id":"tenant","revision":3,"enabled":%t,"contacts":[],"policies":{"default":{"configured":false,"steps":[]},"important":{"configured":false,"steps":[]}}}`, enabled)
	raw, err := tftypes.ValueFromJSON([]byte(data), schema.Type().TerraformType(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	return tfsdk.State{Schema: schema, Raw: raw}
}

func TestUserNotificationDefaultsCreateRequiresImport(t *testing.T) {
	for _, test := range []struct {
		name, contacts      string
		enabled, configured bool
	}{
		{"enabled", "[]", true, false},
		{"disabled with contacts", `[{"id":"` + fTeam + `","channel_id":"` + fService + `","display_name":"Contact","require_verification":true}]`, false, false},
		{"configured empty policy", "[]", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := notificationDefaultsResource(t)
			var reads int
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" || q.URL.RequestURI() != "/v1/settings/user-notification-defaults" {
					t.Error("unexpected request", q.Method, q.URL.RequestURI())
				}
				reads++
				fmt.Fprintf(w, `{"revision":7,"enabled":%t,"contacts":%s,"policies":{"default":{"configured":%t,"steps":[]},"important":{"configured":false,"steps":[]}}}`, test.enabled, test.contacts, test.configured)
			})
			p := notificationDefaultsTestState(t, s, true)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: p.Raw}}, &resp)
			if reads != 1 || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Summary() != "Existing user notification defaults require import" || !resp.State.Raw.IsNull() {
				t.Fatalf("expected explicit import requirement, got %v", resp.Diagnostics)
			}
		})
	}
}

func TestUserNotificationDefaultsGETAndPUTFailuresRetainState(t *testing.T) {
	for _, op := range []string{"read", "update", "delete"} {
		for _, status := range []int{403, 404, 500} {
			t.Run(fmt.Sprintf("%s-%d", op, status), func(t *testing.T) {
				r := notificationDefaultsResource(t)
				calls := 0
				s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
					calls++
					w.WriteHeader(status)
				})
				prior := notificationDefaultsTestState(t, s, true)
				plan := notificationDefaultsTestState(t, s, false)
				var got tfsdk.State
				var failed bool
				switch op {
				case "read":
					resp := resource.ReadResponse{State: prior}
					r.Read(t.Context(), resource.ReadRequest{State: prior}, &resp)
					got, failed = resp.State, resp.Diagnostics.HasError()
				case "update":
					resp := resource.UpdateResponse{State: prior}
					r.Update(t.Context(), resource.UpdateRequest{State: prior, Plan: tfsdk.Plan{Raw: plan.Raw, Schema: s}}, &resp)
					got, failed = resp.State, resp.Diagnostics.HasError()
				case "delete":
					resp := resource.DeleteResponse{State: prior}
					r.Delete(t.Context(), resource.DeleteRequest{State: prior}, &resp)
					got, failed = resp.State, resp.Diagnostics.HasError()
				}
				if !failed || calls != 1 || !got.Raw.Equal(prior.Raw) {
					t.Error("failed request changed singleton state or was retried")
				}
			})
		}
	}
}

func TestUserNotificationDefaultsMalformedCommittedReceiptRetainsSubmittedTemplate(t *testing.T) {
	for _, op := range []string{"create", "update", "delete"} {
		t.Run(op, func(t *testing.T) {
			r := notificationDefaultsResource(t)
			s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				switch q.Method {
				case "GET":
					fmt.Fprint(w, `{"enabled":false,"revision":9,"contacts":[],"policies":{"default":{"configured":false,"steps":[]},"important":{"configured":false,"steps":[]}}}`)
				case "PUT":
					fmt.Fprint(w, `{}`)
				default:
					t.Error("unexpected mutation", q.Method)
				}
			})
			prior := notificationDefaultsTestState(t, s, op == "delete")
			planned := notificationDefaultsTestState(t, s, true)
			plan := tfsdk.Plan{Schema: s, Raw: planned.Raw}
			var got tfsdk.State
			var failed bool
			switch op {
			case "create":
				resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
				r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &resp)
				got, failed = resp.State, resp.Diagnostics.HasError()
			case "update":
				resp := resource.UpdateResponse{State: prior}
				r.Update(t.Context(), resource.UpdateRequest{Plan: plan, State: prior}, &resp)
				got, failed = resp.State, resp.Diagnostics.HasError()
			case "delete":
				resp := resource.DeleteResponse{State: prior}
				r.Delete(t.Context(), resource.DeleteRequest{State: prior}, &resp)
				got, failed = resp.State, resp.Diagnostics.HasError()
			}
			var id types.String
			var enabled types.Bool
			var revision types.Int64
			if !failed || got.Raw.IsNull() || got.GetAttribute(t.Context(), path.Root("id"), &id).HasError() || id.ValueString() != "tenant" ||
				got.GetAttribute(t.Context(), path.Root("enabled"), &enabled).HasError() || enabled.ValueBool() != (op != "delete") ||
				got.GetAttribute(t.Context(), path.Root("revision"), &revision).HasError() || !revision.IsNull() {
				t.Error("committed intent or unknown revision lost")
			}
		})
	}
}

func TestUserNotificationDefaultsNullElementsAndUnknownInputs(t *testing.T) {
	for _, tc := range []struct{ name, contacts, steps string }{
		{"null contact", "[null]", "[]"},
		{"null step", "[]", "[null]"},
		{"null list", "null", "[]"},
		{"unknown contacts", "[]", "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := notificationDefaultsResource(t)
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { t.Error("incomplete input reached Control") })
			raw, err := tftypes.ValueFromJSON([]byte(fmt.Sprintf(`{"enabled":false,"contacts":%s,"policies":{"default":{"configured":true,"steps":%s},"important":{"configured":false,"steps":[]}}}`, tc.contacts, tc.steps)), s.Type().TerraformType(t.Context()))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "unknown contacts" {
				var vals map[string]tftypes.Value
				if err := raw.As(&vals); err != nil {
					t.Fatal(err)
				}
				vals["contacts"] = tftypes.NewValue(vals["contacts"].Type(), tftypes.UnknownValue)
				raw = tftypes.NewValue(raw.Type(), vals)
			}
			var validated resource.ValidateConfigResponse
			r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: raw}}, &validated)
			if validated.Diagnostics.HasError() != (tc.name != "unknown contacts") {
				t.Fatalf("configuration validation: %v", validated.Diagnostics)
			}
			created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: raw}}, &created)
			if !created.Diagnostics.HasError() || !created.State.Raw.IsNull() {
				t.Error("apply accepted incomplete template")
			}
		})
	}
}
