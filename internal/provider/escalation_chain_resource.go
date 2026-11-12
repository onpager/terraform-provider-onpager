package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type escalationChainResource struct{ configuredResource }

func NewEscalationChainResource() resource.Resource { return &escalationChainResource{} }
func (*escalationChainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_escalation_chain"
}
func optionalUUID(description string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, MarkdownDescription: description + " Must be a nonzero canonical lowercase UUID when set.", Validators: []validator.String{stringRule{uuid: true}}}
}
func (*escalationChainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	team := uuidAttribute("Owning team UUID. Changing it replaces the chain.")
	team.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one team's escalation chain and its complete ordered step definition.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "team_id": team,
		"name":                        schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty chain name without leading or trailing whitespace. Duplicate names are allowed.", Validators: []validator.String{stringRule{}}},
		"etag":                        schema.StringAttribute{Computed: true, MarkdownDescription: "Strong SHA-256 configuration ETag used in `If-Match` for definition replacement and archival. A stale ETag reports a conflict; run a new plan before retrying."},
		"work_calendar_id":            optionalUUID("Calendar UUID belonging to the owning team. Omission or `null` removes the calendar link when the definition is updated."),
		"work_calendar_reference":     schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("start_time"), MarkdownDescription: "Calendar evaluation reference: `start_time` (default) or `step_time`. `step_time` requires `work_calendar_id`.", Validators: []validator.String{enumRule{"start_time", "step_time"}}},
		"off_hours_delegate_chain_id": optionalUUID("Explicit off-hours delegation target chain UUID. Omission or `null` removes delegation when the definition is updated."),
		"steps": schema.ListNestedAttribute{Required: true, MarkdownDescription: "Ordered chain steps; list order defines execution order. An empty list is allowed. Each kind accepts only its applicable settings; omit or use `null` for other settings. Server-generated step IDs are not managed.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"kind":           schema.StringAttribute{Required: true, MarkdownDescription: "Step kind: `wait`, `repeat`, `notify_user`, `notify_oncall`, `notify_channel`, or `notify_webhook`. `repeat` takes no additional settings.", Validators: []validator.String{enumRule{"wait", "repeat", "notify_user", "notify_oncall", "notify_channel", "notify_webhook"}}},
			"wait_seconds":   schema.Int64Attribute{Optional: true, MarkdownDescription: "Whole-second wait duration from 1 through 9223372036. Required only for `wait`; zero is invalid."},
			"target_user_id": optionalUUID("User UUID. Required only for `notify_user`."), "schedule_id": optionalUUID("Schedule UUID. Required only for `notify_oncall`."), "webhook_id": optionalUUID("Webhook UUID. Required only for `notify_webhook`."),
			"importance":      schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Notification importance: `default` or `important`, only for `notify_user` and `notify_oncall`. Omission or `null` selects `default`, including after removing an explicit value. Must be omitted or `null` for other kinds.", Validators: []validator.String{enumRule{"default", "important"}}},
			"channel_sources": schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Nonempty list of unique `team` and/or `service` catalog sources in priority order. Required only for `notify_channel`."},
		}}},
	}}
}
func (r *escalationChainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan chainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input, d := chainPayload(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.client.CreateChain(ctx, plan.TeamID.ValueString(), input)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager escalation chain", err)
		return
	}
	partial, d := chainModelFromAPI(ctx, client.Chain{ID: id, TeamID: plan.TeamID.ValueString(), ChainInput: input})
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &partial)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetChain(ctx, partial.TeamID.ValueString(), id)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read newly created OnPager escalation chain", err)
		return
	}
	m, d := chainModelFromAPI(ctx, v)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *escalationChainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state chainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetChain(ctx, state.TeamID.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager escalation chain", err)
		return
	}
	if v.RetiredAt != nil {
		resp.State.RemoveResource(ctx)
		return
	}
	m, d := chainModelFromAPI(ctx, v)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *escalationChainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan chainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input, d := chainPayload(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.UpdateChain(ctx, state.TeamID.ValueString(), state.ID.ValueString(), state.ETag.ValueString(), input)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "replace OnPager escalation chain", err)
		return
	}
	m, d := chainModelFromAPI(ctx, v)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *escalationChainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state chainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.ArchiveChain(ctx, state.TeamID.ValueString(), state.ID.ValueString(), state.ETag.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "archive OnPager escalation chain", err)
	}
}
func (*escalationChainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	team, id, ok := splitImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid chain import ID", "Use team_uuid/chain_uuid with canonical nonzero lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), team)...)
}
