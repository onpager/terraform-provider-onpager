package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func contactMap(entries map[string]string) types.Map {
	values := make(map[string]attr.Value, len(entries))
	for k, v := range entries {
		values[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, values)
}

func userWebhookValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "user_id": types.StringValue(fOtherTeam), "webhook_id": types.StringValue(fService), "request_id": types.StringValue(scheduleID), "template_contact_id": types.StringNull(), "display_name": types.StringValue("Receiver"), "values": contactMap(nil), "enabled": types.BoolValue(true), "verified": types.BoolValue(true), "draft": types.BoolValue(false), "generation": types.Int64Value(1), "etag": types.StringValue(`"7"`)}
}

func userWebhookReceipt() map[string]any {
	return map[string]any{"id": fTeam, "user_id": fOtherTeam, "webhook_id": fService, "display_name": "Receiver", "values": map[string]string{"optional": ""}, "enabled": true, "verified": true, "draft": false, "generation": 1, "etag": `"7"`}
}

func TestUserWebhookContactCreateSavesPOSTBeforeGuardedPatch(t *testing.T) {
	for _, tc := range []struct {
		status   int
		retained bool
	}{{200, false}, {403, true}, {412, true}} {
		r := onpager.NewUserWebhookContactResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			var body map[string]any
			_ = json.NewDecoder(q.Body).Decode(&body)
			receipt := userWebhookReceipt()
			if calls == 1 {
				if q.Method != "POST" || q.URL.Path != "/v1/users/"+fOtherTeam+"/webhook-contacts" || !reflect.DeepEqual(body, map[string]any{"webhook_id": fService, "request_id": scheduleID, "display_name": "Receiver", "values": map[string]any{}}) {
					t.Error("unexpected POST payload")
				}
				w.Header().Set("ETag", `"7"`)
				w.WriteHeader(201)
			} else {
				if q.Method != "PATCH" || q.URL.Path != "/v1/users/"+fOtherTeam+"/webhook-contacts/"+fTeam || q.Header.Get("If-Match") != `"7"` || !reflect.DeepEqual(body, map[string]any{"enabled": false}) {
					t.Error("unexpected post-create PATCH")
				}
				w.Header().Set("ETag", `"8"`)
				w.WriteHeader(tc.status)
				receipt["etag"], receipt["enabled"] = `"8"`, false
			}
			_ = json.NewEncoder(w).Encode(receipt)
		})
		v := userWebhookValues()
		v["enabled"] = types.BoolValue(false)
		v["id"] = types.StringUnknown()
		conf := valueState(t, s, v)
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: conf.Raw}, Config: tfsdk.Config{Schema: s, Raw: conf.Raw}}, &resp)
		want := userWebhookValues()
		if !tc.retained {
			want["etag"] = types.StringValue(`"8"`)
			want["enabled"] = types.BoolValue(false)
		}
		if calls != 2 || resp.Diagnostics.HasError() != tc.retained || !resp.State.Raw.Equal(valueState(t, s, want).Raw) {
			t.Fatal("lost created method or map shape", tc, resp.Diagnostics)
		}
	}
}

func TestUserWebhookContactImportPreservesDraftUntilExplicitEmptyValues(t *testing.T) {
	for _, complete := range []bool{false, true} {
		r := onpager.NewUserWebhookContactResource()
		var calls int
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			receipt := userWebhookReceipt()
			receipt["verified"], receipt["draft"], receipt["generation"], receipt["values"] = false, true, 0, map[string]string{}
			if calls == 1 {
				if q.Method != "GET" || q.URL.Path != "/v1/users/"+fOtherTeam+"/webhook-contacts/"+fTeam {
					t.Error("unexpected draft import read")
				}
				w.Header().Set("ETag", `"7"`)
			} else {
				want := map[string]any{"display_name": "Renamed"}
				if complete {
					want = map[string]any{"values": map[string]any{}, "expected_generation": float64(0)}
					receipt["verified"], receipt["draft"], receipt["generation"] = true, false, 1
				} else {
					receipt["display_name"], receipt["generation"] = "Renamed", 1
				}
				var body map[string]any
				_ = json.NewDecoder(q.Body).Decode(&body)
				if q.Method != "PATCH" || q.Header.Get("If-Match") != `"7"` || !reflect.DeepEqual(body, want) {
					t.Error("wrong draft PATCH", body)
				}
				w.Header().Set("ETag", `"8"`)
				receipt["etag"] = `"8"`
			}
			_ = json.NewEncoder(w).Encode(receipt)
		})
		imported := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)}}
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: fOtherTeam + "/" + fTeam}, &imported)
		if imported.Diagnostics.HasError() {
			t.Fatal(imported.Diagnostics)
		}
		read := resource.ReadResponse{State: imported.State}
		r.Read(t.Context(), resource.ReadRequest{State: imported.State}, &read)
		v := userWebhookValues()
		v["request_id"], v["values"], v["draft"], v["verified"], v["generation"] = types.StringNull(), types.MapNull(types.StringType), types.BoolValue(true), types.BoolValue(false), types.Int64Value(0)
		if imported.Diagnostics.HasError() || read.Diagnostics.HasError() || !read.State.Raw.Equal(valueState(t, s, v).Raw) {
			t.Fatal("import did not preserve draft identity and absent recipient", read.Diagnostics)
		}
		if complete {
			v["values"] = contactMap(nil)
		} else {
			v["display_name"] = types.StringValue("Renamed")
		}
		next := valueState(t, s, v)
		updated := resource.UpdateResponse{State: read.State}
		r.Update(t.Context(), resource.UpdateRequest{State: read.State, Plan: tfsdk.Plan{Schema: s, Raw: next.Raw}, Config: tfsdk.Config{Schema: s, Raw: next.Raw}}, &updated)
		v["etag"], v["generation"] = types.StringValue(`"8"`), types.Int64Value(1)
		if complete {
			v["draft"], v["verified"] = types.BoolValue(false), types.BoolValue(true)
		}
		if calls != 2 || updated.Diagnostics.HasError() || !updated.State.Raw.Equal(valueState(t, s, v).Raw) {
			t.Fatal("draft patch lost expected method shape", updated.Diagnostics)
		}
	}
}

func TestUserWebhookContactReadRetainsStateOnFailure(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		gone   bool
	}{{404, "not_found", true}, {404, "unknown_route", false}, {403, "forbidden", false}, {500, "internal", false}} {
		r := onpager.NewUserWebhookContactResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprintf(w, `{"error":{"code":%q}}`, tc.code)
		})
		old := valueState(t, s, userWebhookValues())
		resp := resource.ReadResponse{State: old}
		r.Read(t.Context(), resource.ReadRequest{State: old}, &resp)
		if resp.Diagnostics.HasError() == tc.gone || resp.State.Raw.IsNull() != tc.gone || !tc.gone && !old.Raw.Equal(resp.State.Raw) {
			t.Fatal("unexpected refresh state", tc)
		}
	}
}

func TestUserWebhookContactCreateReplayReconcilesDriftBySavedTag(t *testing.T) {
	r := onpager.NewUserWebhookContactResource()
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
		calls++
		view := userWebhookReceipt()
		if calls == 1 {
			view["display_name"] = "Externally renamed"
			view["enabled"] = false
			view["values"] = map[string]string{"recipient": "changed"}
		} else {
			if q.Method != "PATCH" || q.Header.Get("If-Match") != `"7"` {
				t.Error("replayed method update lost binding")
			}
			var body map[string]any
			_ = json.NewDecoder(q.Body).Decode(&body)
			if !reflect.DeepEqual(body, map[string]any{"display_name": "Receiver", "values": map[string]any{}, "enabled": true}) {
				t.Error("incomplete replay reconciliation")
			}
			view["etag"] = `"8"`
		}
		w.Header().Set("ETag", view["etag"].(string))
		_ = json.NewEncoder(w).Encode(view)
	})
	v := userWebhookValues()
	next := valueState(t, s, v)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: next.Raw}, Config: tfsdk.Config{Schema: s, Raw: next.Raw}}, &resp)
	var tag types.String
	resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("etag"), &tag)...)
	if calls != 2 || resp.Diagnostics.HasError() || tag.ValueString() != `"8"` {
		t.Fatal("lost live method on create replay", resp.Diagnostics)
	}
}

func TestUserWebhookContactCreateKeepsOnlyValidatedPartialIdentity(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		r := onpager.NewUserWebhookContactResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			definition := fService
			if foreign {
				definition = fOtherTeam
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": fTeam, "user_id": fOtherTeam, "webhook_id": definition})
		})
		cfg := valueState(t, s, userWebhookValues())
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: cfg.Raw}, Config: tfsdk.Config{Schema: s, Raw: cfg.Raw}}, &resp)
		if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() != foreign {
			t.Fatal("malformed receipt lost diagnostic or adopted foreign binding", resp.Diagnostics)
		}
		if !foreign {
			want := userWebhookValues()
			want["etag"], want["generation"], want["draft"], want["enabled"], want["verified"] = types.StringNull(), types.Int64Null(), types.BoolNull(), types.BoolNull(), types.BoolNull()
			if !resp.State.Raw.Equal(valueState(t, s, want).Raw) {
				t.Fatal("lost committed create identity, key or intent")
			}
		}
	}
}

func TestUserWebhookContactPatchFailureRetainsScopedState(t *testing.T) {
	for _, status := range []int{200, 403, 404, 409, 412} {
		r := onpager.NewUserWebhookContactResource()
		calls := 0
		s := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
			calls++
			var data map[string]any
			_ = json.NewDecoder(q.Body).Decode(&data)
			if q.Method != "PATCH" || q.Header.Get("If-Match") != `"7"` || !reflect.DeepEqual(data, map[string]any{"display_name": "Changed"}) {
				t.Error("rename sent unrelated fields or lacked precondition")
			}
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"code":"not_found","message":"private-marker"}}`)
		})
		v := userWebhookValues()
		v["values"] = contactMap(map[string]string{"label": "remembered"})
		old := valueState(t, s, v)
		v["display_name"] = types.StringValue("Changed")
		plan := valueState(t, s, v)
		v["values"] = types.MapNull(types.StringType)
		config := valueState(t, s, v)
		out := resource.UpdateResponse{State: old}
		r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}, Config: tfsdk.Config{Schema: s, Raw: config.Raw}}, &out)
		if !out.Diagnostics.HasError() || calls != 1 {
			t.Fatal("lost update diagnostic or replayed mutation", out.Diagnostics)
		}
		if status == 200 {
			v["values"], v["etag"], v["generation"], v["verified"], v["draft"] = contactMap(map[string]string{"label": "remembered"}), types.StringNull(), types.Int64Null(), types.BoolNull(), types.BoolNull()
			if !out.State.Raw.Equal(valueState(t, s, v).Raw) {
				t.Fatal("malformed committed receipt lost known configuration or trusted stale ETag")
			}
		} else if !out.State.Raw.Equal(old.Raw) {
			t.Fatal("HTTP failure lost saved state", status)
		}
	}
}
