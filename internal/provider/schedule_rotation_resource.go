package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type scheduleDeclarationResource struct {
	configuredResource
	primary bool
}

func NewScheduleRotationResource() resource.Resource {
	return &scheduleDeclarationResource{primary: true}
}
func (r *scheduleDeclarationResource) kind() string {
	if r.primary {
		return "primary rotation"
	}
	return "override"
}
func (r *scheduleDeclarationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	suffix := "_schedule_override"
	if r.primary {
		suffix = "_schedule_rotation"
	}
	resp.TypeName = req.ProviderTypeName + suffix
}
func (r *scheduleDeclarationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	schedule := uuidAttribute("Owning schedule UUID. Changing it replaces this child declaration; parent settings and sibling rotations remain independent.")
	schedule.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	attrs := map[string]schema.Attribute{"id": idAttribute(), "schedule_id": schedule, "request_timezone": temporalTimezoneAttribute(), "revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "This child rotation's revision, used in strong numeric `If-Match` for declaration replacement and retirement. Independent of parent settings, siblings, and aggregate revision. Stale writes report a conflict."}, "starts_at": schema.StringAttribute{Required: true, MarkdownDescription: "Recurrence anchor for a primary rotation, or inclusive start for an override. Use a whole-second local `YYYY-MM-DDTHH:mm:ss` or explicit-offset RFC3339 timestamp; zero-only fractional digits are accepted. Source and UTC years must be 0001 through 9999. Equivalent timestamp spellings retain the configured value across request-timezone response changes. Local input is sent unchanged and resolved by the backend in the provider request timezone; DST ambiguity or gaps return API diagnostics. Explicit offsets always identify absolute instants. Changing an explicit provider timezone plans an update for local input. Must precede `ends_at` when set."}, "ends_at": schema.StringAttribute{Required: !r.primary, Optional: r.primary, MarkdownDescription: "Exclusive whole-second end in local `YYYY-MM-DDTHH:mm:ss` or explicit-offset RFC3339 form; zero-only fractional digits are accepted. Source and UTC years must be 0001 through 9999. Must follow `starts_at`. Primary rotations may omit this or use `null` for no end; overrides require a finite end. An empty string is invalid. Equivalent timestamp spellings retain the configured value across request-timezone response changes. Local input is sent unchanged and resolved by the backend in the provider request timezone; DST ambiguity or gaps return API diagnostics. Explicit offsets always identify absolute instants. Changing an explicit provider timezone plans an update for local input."}}
	if r.primary {
		attrs["effective_from"] = schema.StringAttribute{Required: true, MarkdownDescription: "Explicit publication boundary for this complete declaration: a whole-second local `YYYY-MM-DDTHH:mm:ss` or explicit-offset RFC3339 timestamp; zero-only fractional digits are accepted. Source and UTC years must be 0001 through 9999. Independent of the `starts_at` recurrence anchor and never derived from the current time. Updates preserve the earlier plan, including gaps. Equivalent timestamp spellings retain the configured value across request-timezone response changes. Local input is sent unchanged and resolved by the backend in the provider request timezone; DST ambiguity or gaps return API diagnostics. Explicit offsets always identify absolute instants. Changing an explicit provider timezone plans an update for local input."}
		attrs["user_ids"] = schema.ListAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Nonempty ordered list of distinct nonzero canonical lowercase user UUIDs. List order defines rotation order and is preserved. Users must be active members of the owning team; no membership is created implicitly."}
		attrs["recurrence"] = schema.SingleNestedAttribute{Required: true, MarkdownDescription: "Complete recurrence interval anchored at `starts_at`. Required; omission and `null` are invalid.", Attributes: map[string]schema.Attribute{"every": schema.Int64Attribute{Required: true, MarkdownDescription: "Recurrence multiplier from 1 through 2147483647; zero is invalid."}, "unit": schema.StringAttribute{Required: true, MarkdownDescription: "Recurrence unit: `hours`, `days`, `weeks`, or `months`.", Validators: []validator.String{enumRule{"hours", "days", "weeks", "months"}}}}}
		attrs["weekdays"] = schema.SetAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Optional nonempty set of UTC weekdays: `MO`, `TU`, `WE`, `TH`, `FR`, `SA`, `SU`. Omission or `null` means unrestricted weekdays; an explicit empty set is invalid."}
		attrs["max_shift_duration_seconds"] = schema.Int64Attribute{Optional: true, MarkdownDescription: "Optional positive whole-second shift-duration limit. Omission or `null` leaves shifts unrestricted by this limit; zero and negative values are invalid."}
	} else {
		attrs["user_id"] = uuidAttribute("User UUID assigned for this one-off interval. The user must be an active member of the owning team; no membership is created implicitly.")
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one " + r.kind() + " declaration for a schedule, independently of parent settings and sibling declarations.", Attributes: attrs}
}
func (r *scheduleDeclarationResource) read(ctx context.Context, m scheduleDeclarationModel, binding temporalBindings, written bool) (scheduleDeclarationModel, error) {
	if r.primary {
		v, err := r.client.GetPrimaryRotation(ctx, m.ScheduleID.ValueString(), m.ID.ValueString())
		if err != nil {
			return m, err
		}
		if v.ID != m.ID.ValueString() || v.ScheduleID != m.ScheduleID.ValueString() {
			return m, errors.New("OnPager returned a declaration with an unexpected identity")
		}
		if written && v.Revision != m.Revision.ValueInt64() {
			return m, errors.New("OnPager schedule declaration changed concurrently after the committed mutation; refresh and plan again")
		}
		return schedulePrimaryDeclarationState(ctx, m, v, binding, written)
	}
	v, err := r.client.GetOverrideRotation(ctx, m.ScheduleID.ValueString(), m.ID.ValueString())
	if err != nil {
		return m, err
	}
	if v.ID != m.ID.ValueString() || v.ScheduleID != m.ScheduleID.ValueString() {
		return m, errors.New("OnPager returned a declaration with an unexpected identity")
	}
	if written && v.Revision != m.Revision.ValueInt64() {
		return m, errors.New("OnPager schedule declaration changed concurrently after the committed mutation; refresh and plan again")
	}
	return scheduleOverrideDeclarationState(m, v, binding, written)
}
func (r *scheduleDeclarationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	plan := scheduleDeclarationLoad(ctx, req.Plan, r.primary, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), plan.StartsAt, plan.EndsAt, plan.EffectiveFrom)
	var receipt client.RotationReceipt
	var err error
	if r.primary {
		var rule client.PrimaryRotationRule
		rule, err = plan.primaryRule(ctx)
		if err == nil {
			receipt, err = r.client.CreatePrimaryRotation(ctx, plan.ScheduleID.ValueString(), plan.EffectiveFrom.ValueString(), rule)
		}
	} else {
		var rule client.OverrideRotationRule
		rule, err = plan.overrideRule()
		if err == nil {
			receipt, err = r.client.CreateOverrideRotation(ctx, plan.ScheduleID.ValueString(), rule)
		}
	}
	// A trustworthy success receipt must reach state before the authorized read:
	// a denied or malformed read must never orphan a committed new UUID.
	var apiErr *client.APIError
	if validID(receipt.RotationID) && !errors.As(err, &apiErr) {
		plan.ID = types.StringValue(receipt.RotationID)
		plan.Revision = types.Int64Null()
		if receipt.Revision > 0 {
			plan.Revision = types.Int64Value(receipt.Revision)
		}
		resp.Diagnostics.Append(plan.store(ctx, &resp.State, r.primary)...)
		// Old authoritative timestamps must not survive a newly committed plan if
		// the follow-up read fails or observes a concurrent declaration revision.
		if resp.Private != nil {
			temporalBindings{}.store(ctx, resp.Private, &resp.Diagnostics)
		}
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager schedule "+r.kind(), err)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	binding := temporalBindings{}
	next, err := r.read(ctx, plan, binding, true)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read newly created OnPager schedule "+r.kind(), err)
		return
	}
	resp.Diagnostics.Append(next.store(ctx, &resp.State, r.primary)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *scheduleDeclarationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	state := scheduleDeclarationLoad(ctx, req.State, r.primary, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	binding := temporalLoad(ctx, req.Private, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	next, err := r.read(ctx, state, binding, false)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager schedule "+r.kind(), err)
		return
	}
	resp.Diagnostics.Append(next.store(ctx, &resp.State, r.primary)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *scheduleDeclarationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	state := scheduleDeclarationLoad(ctx, req.State, r.primary, &resp.Diagnostics)
	plan := scheduleDeclarationLoad(ctx, req.Plan, r.primary, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), plan.StartsAt, plan.EndsAt, plan.EffectiveFrom)
	var receipt client.RotationReceipt
	var err error
	if r.primary {
		var rule client.PrimaryRotationRule
		rule, err = plan.primaryRule(ctx)
		if err == nil {
			receipt, err = r.client.UpdatePrimaryRotation(ctx, state.ScheduleID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64(), plan.EffectiveFrom.ValueString(), rule)
		}
	} else {
		var rule client.OverrideRotationRule
		rule, err = plan.overrideRule()
		if err == nil {
			receipt, err = r.client.UpdateOverrideRotation(ctx, state.ScheduleID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64(), rule)
		}
	}
	var apiErr *client.APIError
	if receipt.RotationID == state.ID.ValueString() && validID(receipt.RotationID) && !errors.As(err, &apiErr) {
		plan.ID = state.ID
		plan.ScheduleID = state.ScheduleID
		plan.Revision = types.Int64Null()
		if receipt.Revision > 0 {
			plan.Revision = types.Int64Value(receipt.Revision)
		}
		resp.Diagnostics.Append(plan.store(ctx, &resp.State, r.primary)...)
		// Old authoritative timestamps must not survive a newly committed plan if
		// the follow-up read fails or observes a concurrent declaration revision.
		if resp.Private != nil {
			temporalBindings{}.store(ctx, resp.Private, &resp.Diagnostics)
		}
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager schedule "+r.kind(), err)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	binding := temporalBindings{}
	next, err := r.read(ctx, plan, binding, true)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read updated OnPager schedule "+r.kind(), err)
		return
	}
	resp.Diagnostics.Append(next.store(ctx, &resp.State, r.primary)...)
	if resp.Private != nil {
		binding.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *scheduleDeclarationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	state := scheduleDeclarationLoad(ctx, req.State, r.primary, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var err error
	if r.primary {
		err = r.client.DeletePrimaryRotation(ctx, state.ScheduleID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64())
	} else {
		err = r.client.DeleteOverrideRotation(ctx, state.ScheduleID.ValueString(), state.ID.ValueString(), state.Revision.ValueInt64())
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "retire OnPager schedule "+r.kind(), err)
	}
}
func (r *scheduleDeclarationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	schedule, id, ok := splitImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid schedule declaration import ID", "Use schedule_uuid/rotation_uuid with nonzero canonical lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("schedule_id"), schedule)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
func (r *scheduleDeclarationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	m := scheduleDeclarationLoad(ctx, req.Config, r.primary, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := m.validate(r.primary); err != nil {
		resp.Diagnostics.AddError("Invalid schedule declaration", fmt.Sprintf("%s: %s", r.kind(), err))
	}
}

func (r *scheduleDeclarationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	m := scheduleDeclarationLoad(ctx, req.Plan, r.primary, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	temporalPlanTimezone(ctx, req, resp, temporalRequestTimezone(r.client.Timezone(), m.StartsAt, m.EndsAt, m.EffectiveFrom), "revision")
}
