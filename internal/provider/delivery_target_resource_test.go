package provider_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func targetTestValues(snapshot types.String) map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fTeam), "channel_id": types.StringValue(fService), "snapshot_json": snapshot, "enabled": types.BoolValue(true), "kind": types.StringValue("bot"), "contract_version": types.StringValue("v1")}
}
func TestDeliveryTargetCreateRetainsSnapshotAndActualEnabledOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		full    bool
		put     int
		enabled bool
	}{
		{"incomplete POST", false, 0, true}, {"failed PUT", true, 403, true}, {"unconfirmed PUT", true, 200, false}, {"disabled", true, 204, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := onpager.NewDeliveryTargetResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if calls == 1 {
					if req.Method != "POST" {
						t.Error("expected create")
					}
					w.WriteHeader(201)
					if !tc.full {
						fmt.Fprintf(w, `{"id":%q}`, fTeam)
						return
					}
					fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"kind":"bot","contract_version":"v1","enabled":true}`, fTeam, fService)
					return
				}
				if tc.put == 0 || req.Method != "PUT" {
					t.Error("unexpected follow-up request")
					w.WriteHeader(500)
					return
				}
				w.WriteHeader(tc.put)
			})
			snapshot := types.StringValue(`{"address":"fixture"}`)
			p := targetTestValues(snapshot)
			p["id"], p["kind"], p["contract_version"], p["enabled"] = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.BoolValue(false)
			plan := valueState(t, s, p)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			var id, snap, kind types.String
			var enabled types.Bool
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("id"), &id)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("snapshot_json"), &snap)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("kind"), &kind)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)...)
			if resp.Diagnostics.HasError() != (tc.put != 204) || id.ValueString() != fTeam || !snap.Equal(snapshot) || kind.IsUnknown() || enabled.ValueBool() != tc.enabled || calls != map[bool]int{true: 2, false: 1}[tc.full] {
				t.Fatalf("failed to save committed identity, destination or enabled state: %v", resp.Diagnostics)
			}
		})
	}
}

func TestDeliveryTargetRefreshDoesNotAssociateHiddenSnapshotWithForeignChannel(t *testing.T) {
	for _, channel := range []string{fService, fOtherTeam} {
		r := onpager.NewDeliveryTargetResource()
		s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
			fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"kind":"bot","contract_version":"v1","enabled":false}`, fTeam, channel)
		})
		old := valueState(t, s, targetTestValues(types.StringValue(`{"addr":"fixture"}`)))
		resp := resource.ReadResponse{State: old}
		r.Read(t.Context(), resource.ReadRequest{State: old}, &resp)
		var snap, ch types.String
		var enabled types.Bool
		resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("snapshot_json"), &snap)...)
		resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("channel_id"), &ch)...)
		resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("enabled"), &enabled)...)
		if resp.Diagnostics.HasError() != (channel != fService) || ch.ValueString() != fService || snap.ValueString() != `{"addr":"fixture"}` || enabled.ValueBool() != (channel != fService) {
			t.Fatal("refresh lost identity, intent or state")
		}
	}
}

func TestDeliveryTargetSnapshotPlanUsesExactJSONValues(t *testing.T) {
	for _, tc := range []struct {
		name, prior, planned string
		replace              bool
	}{
		{"different spelling", `{"X":{"b":[1,2],"a":"hello"}}`, `{ "X": {"a":"h\u0065llo","b" : [1e0,2.00] } }`, false},
		{"different large IDs", `{"id":9007199254740992}`, `{"id":9007199254740993}`, true},
		{"different string and number", `{"id":"1"}`, `{"id":1}`, true},
		{"different case", `{"Name":"a"}`, `{"name":"a"}`, true},
		{"different array order", `{"parts":["a","b"]}`, `{"parts":["b","a"]}`, true},
		{"imported with unknown destination", "", "{}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewDeliveryTargetResource()
			s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("planning performed HTTP") })
			prior := types.StringValue(tc.prior)
			if tc.prior == "" {
				prior = types.StringNull()
			}
			old := valueState(t, s, targetTestValues(prior))
			cfg := valueState(t, s, targetTestValues(types.StringValue(tc.planned)))
			p := tfsdk.Plan{Schema: s, Raw: cfg.Raw}
			resp := resource.ModifyPlanResponse{Plan: p}
			r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: s, Raw: cfg.Raw}, State: old, Plan: p}, &resp)
			if resp.Diagnostics.HasError() || (len(resp.RequiresReplace) > 0) != tc.replace {
				t.Fatalf("incorrect snapshot replacement: %v", resp.Diagnostics)
			}
		})
	}
}

func TestDeliveryTargetOmittedSnapshotKeepsKnownValueDuringUpdate(t *testing.T) {
	r := onpager.NewDeliveryTargetResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("planning performed HTTP") })
	previous := types.StringValue(`{"target":42}`)
	old := valueState(t, s, targetTestValues(previous))
	cfg := valueState(t, s, targetTestValues(types.StringNull()))
	p := tfsdk.Plan{Schema: s, Raw: cfg.Raw}
	resp := resource.ModifyPlanResponse{Plan: p}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: s, Raw: cfg.Raw}, State: old, Plan: p}, &resp)
	var saved types.String
	resp.Diagnostics.Append(resp.Plan.GetAttribute(t.Context(), path.Root("snapshot_json"), &saved)...)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 0 || !saved.Equal(previous) {
		t.Fatal("omitting a known hidden destination discarded it or replaced its owner")
	}
}

func TestDeliveryTargetUnknownConfiguredSnapshotRequiresReplacement(t *testing.T) {
	r := onpager.NewDeliveryTargetResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("planning performed HTTP") })
	old := valueState(t, s, targetTestValues(types.StringValue(`{"target":42}`)))
	cfg := valueState(t, s, targetTestValues(types.StringUnknown()))
	config := tfsdk.Config{Schema: s, Raw: cfg.Raw}
	p := tfsdk.Plan{Schema: s, Raw: cfg.Raw}
	resp := resource.ModifyPlanResponse{Plan: p}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{Config: config, State: old, Plan: p}, &resp)
	var planned types.String
	resp.Diagnostics.Append(resp.Plan.GetAttribute(t.Context(), path.Root("snapshot_json"), &planned)...)
	if resp.Diagnostics.HasError() || !planned.IsUnknown() || len(resp.RequiresReplace) != 1 || !resp.RequiresReplace.Contains(path.Root("snapshot_json")) {
		t.Fatal("unknown configured snapshot should require replacement")
	}
}

func TestDeliveryTargetRefreshAndDestroyRequireConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		code   int
		body   string
		absent bool
	}{
		{404, `{"error":{"code":"not_found"}}`, true},
		{404, `<html>not found</html>`, false},
		{404, `{"error":{"code":"unknown_route"}}`, false},
		{403, `{"error":{"code":"not_found"}}`, false},
		{500, `{"error":{"code":"internal_error"}}`, false},
	} {
		t.Run(fmt.Sprint(tc.code, tc.body), func(t *testing.T) {
			r := onpager.NewDeliveryTargetResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(tc.code); fmt.Fprint(w, tc.body) })
			old := valueState(t, s, targetTestValues(types.StringValue(`{}`)))
			read := resource.ReadResponse{State: old}
			r.Read(t.Context(), resource.ReadRequest{State: old}, &read)
			if read.Diagnostics.HasError() == tc.absent || read.State.Raw.IsNull() != tc.absent {
				t.Fatal("read did not distinguish confirmed absence from an error")
			}
			del := resource.DeleteResponse{State: old}
			r.Delete(t.Context(), resource.DeleteRequest{State: old}, &del)
			if del.Diagnostics.HasError() == tc.absent || !tc.absent && del.State.Raw.IsNull() {
				t.Fatal("destroy did not distinguish confirmed absence from an error")
			}
		})
	}
}

func TestDeliveryTargetRespectsTerraformIgnoredDestinationChange(t *testing.T) {
	r := onpager.NewDeliveryTargetResource()
	s := configuredResource(t, r, func(http.ResponseWriter, *http.Request) { t.Error("planning performed HTTP") })
	old := valueState(t, s, targetTestValues(types.StringValue(`{"target":"retained"}`)))
	cfg := valueState(t, s, targetTestValues(types.StringValue(`{"target":"ignored"}`)))
	// Terraform has already applied lifecycle.ignore_changes to the proposed plan.
	p := tfsdk.Plan{Schema: s, Raw: old.Raw}
	resp := resource.ModifyPlanResponse{Plan: p}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: s, Raw: cfg.Raw}, State: old, Plan: p}, &resp)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 0 {
		t.Fatal("provider replaced the target for an ignored change")
	}
}
