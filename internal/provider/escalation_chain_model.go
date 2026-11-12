package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type chainModel struct {
	ID                types.String `tfsdk:"id"`
	TeamID            types.String `tfsdk:"team_id"`
	Name              types.String `tfsdk:"name"`
	ETag              types.String `tfsdk:"etag"`
	Steps             types.List   `tfsdk:"steps"`
	CalendarID        types.String `tfsdk:"work_calendar_id"`
	CalendarReference types.String `tfsdk:"work_calendar_reference"`
	DelegateID        types.String `tfsdk:"off_hours_delegate_chain_id"`
}
type chainStepModel struct {
	Kind           types.String `tfsdk:"kind"`
	WaitSeconds    types.Int64  `tfsdk:"wait_seconds"`
	TargetUserID   types.String `tfsdk:"target_user_id"`
	ScheduleID     types.String `tfsdk:"schedule_id"`
	WebhookID      types.String `tfsdk:"webhook_id"`
	Importance     types.String `tfsdk:"importance"`
	ChannelSources types.List   `tfsdk:"channel_sources"`
}

var chainStepType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"kind": types.StringType, "wait_seconds": types.Int64Type, "target_user_id": types.StringType,
	"schedule_id": types.StringType, "webhook_id": types.StringType, "importance": types.StringType, "channel_sources": types.ListType{ElemType: types.StringType},
}}

func optionalString(v *string) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(*v)
}
func stringPointer(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}
func chainPayload(ctx context.Context, m chainModel) (client.ChainInput, diag.Diagnostics) {
	var rows []chainStepModel
	d := m.Steps.ElementsAs(ctx, &rows, false)
	out := client.ChainInput{Name: m.Name.ValueString(), Steps: make([]client.ChainStep, 0, len(rows)), WorkCalendarID: stringPointer(m.CalendarID), WorkCalendarReference: m.CalendarReference.ValueString(), OffHoursDelegateChainID: stringPointer(m.DelegateID)}
	for _, row := range rows {
		step := client.ChainStep{Kind: row.Kind.ValueString(), WaitSeconds: row.WaitSeconds.ValueInt64(), TargetUserID: stringPointer(row.TargetUserID), ScheduleID: stringPointer(row.ScheduleID), WebhookID: stringPointer(row.WebhookID), Importance: row.Importance.ValueString()}
		if !row.ChannelSources.IsNull() && !row.ChannelSources.IsUnknown() {
			d.Append(row.ChannelSources.ElementsAs(ctx, &step.ChannelSources, false)...)
		}
		out.Steps = append(out.Steps, step)
	}
	return out, d
}
func chainModelFromAPI(ctx context.Context, v client.Chain) (chainModel, diag.Diagnostics) {
	var d diag.Diagnostics
	rows := make([]chainStepModel, 0, len(v.Steps))
	for _, step := range v.Steps {
		row := chainStepModel{Kind: types.StringValue(step.Kind), WaitSeconds: types.Int64Null(), TargetUserID: optionalString(step.TargetUserID), ScheduleID: optionalString(step.ScheduleID), WebhookID: optionalString(step.WebhookID), Importance: types.StringNull(), ChannelSources: types.ListNull(types.StringType)}
		if step.WaitSeconds != 0 {
			row.WaitSeconds = types.Int64Value(step.WaitSeconds)
		}
		if step.Kind == "notify_user" || step.Kind == "notify_oncall" {
			importance := step.Importance
			if importance == "" {
				importance = "default"
			}
			row.Importance = types.StringValue(importance)
		}
		if step.ChannelSources != nil {
			var ds diag.Diagnostics
			row.ChannelSources, ds = types.ListValueFrom(ctx, types.StringType, step.ChannelSources)
			d.Append(ds...)
		}
		rows = append(rows, row)
	}
	steps, ds := types.ListValueFrom(ctx, chainStepType, rows)
	d.Append(ds...)
	tag := types.StringNull()
	if v.ETag != "" {
		tag = types.StringValue(v.ETag)
	}
	return chainModel{ID: types.StringValue(v.ID), TeamID: types.StringValue(v.TeamID), Name: types.StringValue(v.Name), ETag: tag, Steps: steps, CalendarID: optionalString(v.WorkCalendarID), CalendarReference: types.StringValue(v.WorkCalendarReference), DelegateID: optionalString(v.OffHoursDelegateChainID)}, d
}
