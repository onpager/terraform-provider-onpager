package provider

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type scheduleResource struct{ configuredResource }
type scheduleModel struct {
	ID             types.String `tfsdk:"id"`
	TeamID         types.String `tfsdk:"team_id"`
	Name           types.String `tfsdk:"name"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	WorkCalendarID types.String `tfsdk:"work_calendar_id"`
	Revision       types.Int64  `tfsdk:"revision"`
}

func NewScheduleResource() resource.Resource { return &scheduleResource{} }
func (*scheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_schedule"
}
func (*scheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	team := uuidAttribute("Owning team UUID. Changing the owner replaces the schedule.")
	team.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage an OnPager schedule's settings independently of its rotations, overrides and handover settings.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "team_id": team,
		"name":             schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty schedule name without leading or trailing whitespace. The API permits at most 200 characters.", Validators: []validator.String{stringRule{}}},
		"enabled":          schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether the schedule is enabled. Defaults to `true`. Disabled schedules remain in state and can be re-enabled. Import and refresh do not change this setting."},
		"work_calendar_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Work-calendar UUID belonging to the same team. Must be a nonzero canonical lowercase UUID when set. Omission or `null` clears the link. Referenced calendar entries must use whole-second boundaries.", Validators: []validator.String{stringRule{uuid: true}}},
		"revision":         schema.Int64Attribute{Computed: true, MarkdownDescription: "Schedule settings revision used in strong numeric `If-Match` for updates and disable. Independent of rotation and override changes; stale settings report a conflict."},
	}}
}
func scheduleState(v client.Schedule) scheduleModel {
	calendar := types.StringNull()
	if v.WorkCalendarID != nil {
		calendar = types.StringValue(*v.WorkCalendarID)
	}
	return scheduleModel{ID: types.StringValue(v.ID), TeamID: types.StringValue(v.TeamID), Name: types.StringValue(v.Name), Enabled: types.BoolValue(v.Enabled), WorkCalendarID: calendar, Revision: types.Int64Value(v.Revision)}
}
func scheduleCalendar(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}
func (r *scheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan scheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.CreateSchedule(ctx, plan.TeamID.ValueString(), plan.Name.ValueString(), scheduleCalendar(plan.WorkCalendarID))
	if err != nil {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) && validID(v.ID) && (v.TeamID == "" || v.TeamID == plan.TeamID.ValueString()) {
			// Creation initially enables the schedule. A later disable has not
			// run, and an incomplete receipt must not lose a known created UUID.
			partial := plan
			partial.ID = types.StringValue(v.ID)
			partial.Enabled = types.BoolValue(true)
			partial.Revision = types.Int64Null()
			if v.Revision > 0 {
				partial.Revision = types.Int64Value(v.Revision)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager schedule", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, scheduleState(v))...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Enabled.ValueBool() {
		v, err = r.client.DisableSchedule(ctx, v.ID, v.Revision)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "disable newly created OnPager schedule", err)
			return
		}
		if v.ID == "" {
			resp.Diagnostics.AddError("Created schedule disappeared", "The schedule was absent during disable; refresh before retrying.")
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, scheduleState(v))...)
	}
}
func (r *scheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetSchedule(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager schedule", err)
		return
	}
	if !state.TeamID.IsNull() && !state.TeamID.IsUnknown() && state.TeamID.ValueString() != v.TeamID {
		resp.Diagnostics.AddError("Unexpected schedule owner", "The returned schedule does not belong to the owner recorded in state.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, scheduleState(v))...)
}
func (r *scheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var v client.Schedule
	var err error
	if !plan.Enabled.ValueBool() && !state.Enabled.Equal(plan.Enabled) && state.Name.Equal(plan.Name) && state.WorkCalendarID.Equal(plan.WorkCalendarID) {
		v, err = r.client.DisableSchedule(ctx, state.ID.ValueString(), state.Revision.ValueInt64())
	} else {
		input := client.ScheduleSettings{}
		if !state.Name.Equal(plan.Name) {
			name := plan.Name.ValueString()
			input.Name = &name
		}
		if !state.Enabled.Equal(plan.Enabled) {
			enabled := plan.Enabled.ValueBool()
			input.Enabled = &enabled
		}
		if !state.WorkCalendarID.Equal(plan.WorkCalendarID) {
			input.WorkCalendarID, _ = json.Marshal(scheduleCalendar(plan.WorkCalendarID))
		}
		v, err = r.client.UpdateSchedule(ctx, state.ID.ValueString(), state.Revision.ValueInt64(), input)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager schedule", err)
		return
	}
	if v.TeamID != state.TeamID.ValueString() {
		resp.Diagnostics.AddError("Unexpected schedule owner", "The returned schedule does not belong to the owner recorded in state.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, scheduleState(v))...)
}
func (r *scheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.DisableSchedule(ctx, state.ID.ValueString(), state.Revision.ValueInt64()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager schedule", err)
	}
}
func (*scheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
