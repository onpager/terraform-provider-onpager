package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type workCalendarEntryResource struct{ configuredResource }

func NewWorkCalendarEntryResource() resource.Resource { return &workCalendarEntryResource{} }
func (*workCalendarEntryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_work_calendar_entry"
}
func (*workCalendarEntryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one work calendar entry with its own UUID and revision.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "request_timezone": temporalTimezoneAttribute(), "team_id": workCalendarScope("Owning team UUID."), "calendar_id": workCalendarScope("Owning calendar UUID."), "revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Entry revision used in strong numeric `If-Match` for replacing or deleting this exception. Independent of parent settings and sibling entries; stale revisions report a conflict."}, "name": workCalendarNameAttribute("Exception name; it does not identify the entry."),
		"effect": schema.StringAttribute{Required: true, MarkdownDescription: "Calendar effect: `working` or `non_working`.", Validators: []validator.String{enumRule{"working", "non_working"}}}, "rule_type": schema.StringAttribute{Required: true, MarkdownDescription: "Exception rule: `date` requires start_date; `date_range` requires start_date and end_date; `datetime_interval` requires starts_at and ends_at; `weekly` requires weekdays; `yearly` requires month and day. Only weekly/yearly accept optional date bounds and clock.", Validators: []validator.String{enumRule{"date", "date_range", "datetime_interval", "weekly", "yearly"}}},
		"start_date": schema.StringAttribute{Optional: true, MarkdownDescription: "Source UTC date in `YYYY-MM-DD` format. Required for `date` and `date_range`; optional inclusive lower bound for `weekly` and `yearly`. Empty strings are invalid."}, "end_date": schema.StringAttribute{Optional: true, MarkdownDescription: "Inclusive source UTC date in `YYYY-MM-DD` format. Required for `date_range`; optional upper bound for `weekly` and `yearly`. Must not precede `start_date`; empty strings are invalid."},
		"starts_at": schema.StringAttribute{Optional: true, MarkdownDescription: "Start instant, required only for `datetime_interval`. Use local `YYYY-MM-DDTHH:mm:ss` or RFC3339 with an explicit offset, at most six fractional digits (microsecond precision). Local values use the provider request timezone; the backend resolves them and rejects DST gaps or ambiguity. Must precede `ends_at`; empty strings are invalid. Equivalent instants retain configured state. Changing an explicit provider timezone plans an update for local inputs."}, "ends_at": schema.StringAttribute{Optional: true, MarkdownDescription: "Exclusive end instant, required only for `datetime_interval`. Use local `YYYY-MM-DDTHH:mm:ss` or RFC3339 with an explicit offset, at most six fractional digits (microsecond precision). Local values use the provider request timezone; the backend resolves them and rejects DST gaps or ambiguity. Must follow `starts_at`; empty strings are invalid. Equivalent instants retain configured state. Changing an explicit provider timezone plans an update for local inputs."},
		"weekdays": schema.SetAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Nonempty set of source UTC weekdays: `MO`, `TU`, `WE`, `TH`, `FR`, `SA`, `SU`. Required only for `weekly`; an empty set is invalid."}, "month": schema.Int64Attribute{Optional: true, MarkdownDescription: "Source UTC month from 1 through 12. Required only for `yearly`; zero is invalid. Must form a valid date with `day`."}, "day": schema.Int64Attribute{Optional: true, MarkdownDescription: "Source UTC day of month, required only for `yearly`. Must form a valid date with `month`, including February 29; zero is invalid."},
		"clock": schema.SingleNestedAttribute{Optional: true, MarkdownDescription: "Dated recurring clock accepted only for weekly/yearly. Omission or null means the entire source UTC day. Source weekdays/month/day retain UTC identities. The backend must resolve all windows to one uniform whole-second UTC clock and duration fitting the source day; DST errors remain backend diagnostics.", Attributes: map[string]schema.Attribute{
			"reference_date": schema.StringAttribute{Required: true, MarkdownDescription: "Explicit fixed UTC source date: 2024-01-01 for weekly, or the selected yearly month/day in leap year 2024."},
			"windows":        schema.SetNestedAttribute{Required: true, MarkdownDescription: "One through seven whole-second dated windows, covering the selected source days. Local datetimes and explicit RFC3339 offsets are sent unchanged. The displayed local date may differ from its source UTC weekday/date.", NestedObject: schema.NestedAttributeObject{Attributes: workCalendarWindowAttributes()}},
		}},
	}}
}
func (r *workCalendarEntryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workCalendarEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := workCalendarEntryInput(ctx, plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager calendar entry", err)
		return
	}
	v, err := r.client.CreateWorkCalendarEntry(ctx, plan.TeamID.ValueString(), plan.CalendarID.ValueString(), in)
	if err != nil {
		if validID(v.ID) && v.CalendarID == plan.CalendarID.ValueString() {
			plan.ID = types.StringValue(v.ID)
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager calendar entry", err)
		return
	}
	plan.ID = types.StringValue(v.ID)
	plan.Revision = types.Int64Value(v.Revision)
	plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), plan.StartsAt, plan.EndsAt, plan.Clock)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if resp.Private != nil {
		temporalBindings{}.store(ctx, resp.Private, &resp.Diagnostics)
	}
	if v.Clock != nil && !workCalendarClockProjectionMatches(ctx, plan.Clock, v.Clock) {
		v, err = r.client.GetWorkCalendarEntry(ctx, plan.TeamID.ValueString(), plan.CalendarID.ValueString(), v.ID)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read created OnPager calendar entry source declaration", err)
			return
		}
		if !temporalConfirmRevision(plan.Revision.ValueInt64(), v.Revision, &resp.Diagnostics) {
			return
		}
	}
	next, err := workCalendarEntryState(ctx, plan.TeamID.ValueString(), v)
	if err != nil {
		plan.ID = types.StringValue(v.ID)
		plan.Revision = types.Int64Value(v.Revision)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		apiDiagnostic(&resp.Diagnostics, "store OnPager calendar entry", err)
		return
	}
	binding := temporalBindings{}
	next = workCalendarEntryReconcileState(ctx, plan, next, binding, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *workCalendarEntryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workCalendarEntryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetWorkCalendarEntry(ctx, state.TeamID.ValueString(), state.CalendarID.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager calendar entry", err)
		return
	}
	next, err := workCalendarEntryState(ctx, state.TeamID.ValueString(), v)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager calendar entry", err)
		return
	}
	binding := temporalLoad(ctx, req.Private, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	next = workCalendarEntryReconcileState(ctx, state, next, binding, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *workCalendarEntryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workCalendarEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := workCalendarEntryInput(ctx, plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager calendar entry", err)
		return
	}
	v, err := r.client.UpdateWorkCalendarEntry(ctx, state.TeamID.ValueString(), state.CalendarID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64(), in)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager calendar entry", err)
		return
	}
	plan.ID = state.ID
	plan.Revision = types.Int64Value(v.Revision)
	plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), plan.StartsAt, plan.EndsAt, plan.Clock)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if resp.Private != nil {
		temporalBindings{}.store(ctx, resp.Private, &resp.Diagnostics)
	}
	if v.Clock != nil && !workCalendarClockProjectionMatches(ctx, plan.Clock, v.Clock) {
		v, err = r.client.GetWorkCalendarEntry(ctx, state.TeamID.ValueString(), state.CalendarID.ValueString(), state.ID.ValueString())
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read updated OnPager calendar entry source declaration", err)
			return
		}
		if !temporalConfirmRevision(plan.Revision.ValueInt64(), v.Revision, &resp.Diagnostics) {
			return
		}
	}
	next, err := workCalendarEntryState(ctx, state.TeamID.ValueString(), v)
	if err != nil {
		plan.ID = state.ID
		plan.Revision = types.Int64Value(v.Revision)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		apiDiagnostic(&resp.Diagnostics, "store OnPager calendar entry", err)
		return
	}
	binding := temporalBindings{}
	next = workCalendarEntryReconcileState(ctx, plan, next, binding, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *workCalendarEntryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workCalendarEntryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteWorkCalendarEntry(ctx, state.TeamID.ValueString(), state.CalendarID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager calendar entry", err)
	}
}
func (*workCalendarEntryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || !validID(parts[0]) || !validID(parts[1]) || !validID(parts[2]) {
		resp.Diagnostics.AddError("Invalid calendar entry import ID", "Use team_uuid/calendar_uuid/entry_uuid.")
		return
	}
	for i, key := range []string{"team_id", "calendar_id", "id"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(key), parts[i])...)
	}
}

func (*workCalendarEntryResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if !req.Config.Raw.IsFullyKnown() {
		return
	}
	var config workCalendarEntryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := workCalendarEntryInput(ctx, config); err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager calendar entry", err)
	}
}

func (r *workCalendarEntryResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var values workCalendarEntryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &values)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone := temporalRequestTimezone(r.client.Timezone(), values.StartsAt, values.EndsAt, values.Clock)
	temporalPlanTimezone(ctx, req, resp, zone, "revision")
}
