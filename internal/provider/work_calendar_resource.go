package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type workCalendarResource struct{ configuredResource }

func NewWorkCalendarResource() resource.Resource { return &workCalendarResource{} }
func (*workCalendarResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_work_calendar"
}
func workCalendarNameAttribute(description string) schema.StringAttribute {
	v := nameAttribute()
	v.MarkdownDescription = description + " Must be nonempty without leading or trailing whitespace."
	return v
}
func workCalendarScope(description string) schema.StringAttribute {
	v := uuidAttribute(description + " Changing it replaces this resource.")
	v.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	return v
}
func workCalendarWindowAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{"starts_at": schema.StringAttribute{Required: true, MarkdownDescription: "Inclusive local datetime in YYYY-MM-DDTHH:mm:ss form or explicit RFC3339 instant. Sent unchanged; the backend resolves local input in the provider request timezone and rejects DST ambiguity or gaps."}, "ends_at": schema.StringAttribute{Required: true, MarkdownDescription: "Exclusive end; same formats as starts_at and must follow it. Mixed local and explicit-offset ordering is checked by the backend."}}
}
func workCalendarScheduleAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{Optional: true, Computed: true, MarkdownDescription: description + " Source weekdays remain UTC. Use exact minute precision; at most 16 normalized intervals per source UTC day. The backend validates source-week bounds and canonicalizes local-midnight fragments.", Attributes: map[string]schema.Attribute{
		"reference_date": schema.StringAttribute{Required: true, MarkdownDescription: "Fixed explicit UTC source Monday: 2024-01-01. This date is a declaration reference, independent of the current date and provider request timezone."},
		"intervals":      schema.SetNestedAttribute{Required: true, MarkdownDescription: "Unordered dated windows. Use [] to clear all intervals. At most 512 windows; do not supply overlapping or touching periods. Local values use the provider request timezone; explicit offsets always identify absolute instants.", NestedObject: schema.NestedAttributeObject{Attributes: workCalendarWindowAttributes()}},
	}}
}
func (*workCalendarResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one work calendar's settings independently of its entries.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "request_timezone": temporalTimezoneAttribute(), "team_id": workCalendarScope("Owning team UUID."), "name": workCalendarNameAttribute("Name unique case-insensitively within this team among non-retired calendars."),
		"revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Calendar settings revision used in strong numeric `If-Match` for settings updates and archival. Independent of entry revisions; stale settings report a conflict."}, "aggregate_revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Aggregate revision including entries. Informational only; never used for settings or entry writes."},
		"base_schedule":               workCalendarScheduleAttribute("Dated weekly intervals. Omission or `null` on creation uses the API 24x7 default; on later applies it retains server settings. Explicit `intervals = []` creates or clears all weekly working hours."),
		"working_weekend_schedule":    workCalendarScheduleAttribute("Dated fallback intervals for production working weekends, restricted to `SA`/`SU`. Omission or `null` on creation defaults to empty; on later applies it retains server settings. Explicit `intervals = []` clears it."),
		"inherit_production_calendar": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Whether production-calendar working/nonworking dates are inherited for `production_country_code`. Defaults to `false`. No production calendar is downloaded by Terraform."}, "production_country_code": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("RU"), MarkdownDescription: "Existing canonical production country code. Defaults to `RU`. Must match `^[A-Z][A-Z0-9_-]{1,15}$`: 2 through 16 uppercase letters, digits, underscores or hyphens, starting with a letter, without surrounding whitespace. Must be valid even when inheritance is `false`; empty and lowercase values are invalid."},
	}}
}
func (r *workCalendarResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workCalendarModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := workCalendarInput(ctx, plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager work calendar", err)
		return
	}
	v, err := r.client.CreateWorkCalendar(ctx, plan.TeamID.ValueString(), in)
	if err != nil {
		if validID(v.ID) && v.TeamID == plan.TeamID.ValueString() {
			plan.ID = workCalendarNullableString(v.ID)
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager work calendar", err)
		return
	}
	// Mutation responses use a current-week display projection. Commit identity
	// before reading the explicit source week used by this declaration.
	plan.ID = types.StringValue(v.ID)
	plan.Revision = types.Int64Value(v.Revision)
	plan.AggregateRevision = types.Int64Value(v.AggregateRevision)
	plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), plan.BaseSchedule, plan.WorkingWeekendSchedule)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if v.BaseSchedule.ReferenceDate != client.WorkCalendarReferenceDate || v.WorkingWeekendSchedule.ReferenceDate != client.WorkCalendarReferenceDate {
		v, err = r.client.GetWorkCalendar(ctx, plan.TeamID.ValueString(), v.ID)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read created OnPager calendar source week", err)
			return
		}
		if !temporalConfirmRevision(plan.Revision.ValueInt64(), v.Revision, &resp.Diagnostics) {
			return
		}
	}
	state, err := workCalendarState(ctx, v)
	if err != nil {
		plan.ID = workCalendarNullableString(v.ID)
		plan.Revision = workCalendarNullableInt(v.Revision)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		apiDiagnostic(&resp.Diagnostics, "store OnPager work calendar", err)
		return
	}
	binding := temporalBindings{}
	state = workCalendarReconcileState(ctx, plan, state, binding, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *workCalendarResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workCalendarModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetWorkCalendar(ctx, state.TeamID.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager work calendar", err)
		return
	}
	if v.RetiredAt != nil {
		resp.State.RemoveResource(ctx)
		return
	}
	next, err := workCalendarState(ctx, v)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager weekly settings", err)
		return
	}
	binding := temporalLoad(ctx, req.Private, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	next = workCalendarReconcileState(ctx, state, next, binding, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *workCalendarResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workCalendarModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input := plan
	if req.Config.Raw.Type() != nil {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("base_schedule"), &input.BaseSchedule)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("working_weekend_schedule"), &input.WorkingWeekendSchedule)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	binding := temporalLoad(ctx, req.Private, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := workCalendarInput(ctx, input)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager work calendar", err)
		return
	}
	v, err := r.client.UpdateWorkCalendar(ctx, state.TeamID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64(), in)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager work calendar", err)
		return
	}
	plan.ID = state.ID
	plan.Revision = types.Int64Value(v.Revision)
	plan.AggregateRevision = types.Int64Value(v.AggregateRevision)
	plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), input.BaseSchedule, input.WorkingWeekendSchedule)
	// Invalidate only bindings whose inputs were written. A failed follow-up
	// read must not pair new local intent with an old authoritative instant.
	if !input.BaseSchedule.IsNull() {
		delete(binding, "base_schedule")
	}
	if !input.WorkingWeekendSchedule.IsNull() {
		delete(binding, "working_weekend_schedule")
	}
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
	if plan.BaseSchedule.IsUnknown() {
		plan.BaseSchedule = state.BaseSchedule
	}
	if plan.WorkingWeekendSchedule.IsUnknown() {
		plan.WorkingWeekendSchedule = state.WorkingWeekendSchedule
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if v.BaseSchedule.ReferenceDate != client.WorkCalendarReferenceDate || v.WorkingWeekendSchedule.ReferenceDate != client.WorkCalendarReferenceDate {
		v, err = r.client.GetWorkCalendar(ctx, state.TeamID.ValueString(), state.ID.ValueString())
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read updated OnPager calendar source week", err)
			return
		}
		if !temporalConfirmRevision(plan.Revision.ValueInt64(), v.Revision, &resp.Diagnostics) {
			return
		}
	}
	next, err := workCalendarState(ctx, v)
	if err != nil {
		plan.ID = state.ID
		plan.Revision = workCalendarNullableInt(v.Revision)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		apiDiagnostic(&resp.Diagnostics, "store OnPager work calendar", err)
		return
	}
	next.BaseSchedule = binding.windows(ctx, "base_schedule", plan.BaseSchedule, next.BaseSchedule, "intervals", !input.BaseSchedule.IsNull())
	next.WorkingWeekendSchedule = binding.windows(ctx, "working_weekend_schedule", plan.WorkingWeekendSchedule, next.WorkingWeekendSchedule, "intervals", !input.WorkingWeekendSchedule.IsNull())
	next.RequestTimezone = plan.RequestTimezone
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *workCalendarResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workCalendarModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.ArchiveWorkCalendar(ctx, state.TeamID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "archive OnPager work calendar", err)
	}
}
func (*workCalendarResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || !validID(parts[0]) || !validID(parts[1]) {
		resp.Diagnostics.AddError("Invalid work calendar import ID", "Use team_uuid/calendar_uuid.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func (*workCalendarResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var country types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("production_country_code"), &country)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := workCalendarCountryCodeError(country); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("production_country_code"), "Invalid production country code", err.Error())
		return
	}

	if !req.Config.Raw.IsFullyKnown() {
		return
	}
	var config workCalendarModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := workCalendarInput(ctx, config); err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager work calendar", err)
	}
}

func (r *workCalendarResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var values workCalendarModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &values)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.Config.Raw.Type() != nil {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("base_schedule"), &values.BaseSchedule)...)
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("working_weekend_schedule"), &values.WorkingWeekendSchedule)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	zone := temporalRequestTimezone(r.client.Timezone(), values.BaseSchedule, values.WorkingWeekendSchedule)
	temporalPlanTimezone(ctx, req, resp, zone, "revision", "aggregate_revision")
}
