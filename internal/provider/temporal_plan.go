package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func temporalPlanTimezone(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, zone types.String, revisions ...string) {
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("request_timezone"), zone)...)
	if req.State.Raw.Type() == nil || req.State.Raw.IsNull() {
		return
	}
	var previous types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("request_timezone"), &previous)...)
	if resp.Diagnostics.HasError() || previous.Equal(zone) {
		return
	}
	// Provider configuration can change without changing any resource arguments.
	// Framework unknown marking has already run before ModifyPlan introduces this
	// update, so invalidate the revisions that the forthcoming write will change.
	for _, name := range revisions {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.Int64Unknown())...)
	}
}
