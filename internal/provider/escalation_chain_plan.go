package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Defaults depend on the step kind. Optional+Computed alone would retain the
// previous importance when an explicit setting is removed from configuration.
func (*escalationChainResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var config, plan chainModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || config.Steps.IsNull() || config.Steps.IsUnknown() || plan.Steps.IsUnknown() {
		return
	}
	for _, v := range config.Steps.Elements() {
		if v.IsUnknown() {
			return
		}
	}
	var configured, planned []chainStepModel
	resp.Diagnostics.Append(config.Steps.ElementsAs(ctx, &configured, false)...)
	resp.Diagnostics.Append(plan.Steps.ElementsAs(ctx, &planned, false)...)
	if resp.Diagnostics.HasError() || len(configured) != len(planned) {
		return
	}
	for i, c := range configured {
		if !c.Importance.IsNull() || c.Kind.IsUnknown() || c.Kind.IsNull() {
			continue
		}
		planned[i].Importance = types.StringNull()
		if c.Kind.ValueString() == "notify_user" || c.Kind.ValueString() == "notify_oncall" {
			planned[i].Importance = types.StringValue("default")
		}
	}
	steps, d := types.ListValueFrom(ctx, chainStepType, planned)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !steps.Equal(plan.Steps) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("steps"), steps)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("etag"), types.StringUnknown())...)
	}
}
