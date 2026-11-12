package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type handoverResource struct{ configuredResource }
type handoverModel struct {
	ID         types.String `tfsdk:"id"`
	TeamID     types.String `tfsdk:"team_id"`
	ScheduleID types.String `tfsdk:"schedule_id"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	Channels   types.Set    `tfsdk:"oncall_channel_ids"`
	Revision   types.Int64  `tfsdk:"revision"`
}

func NewScheduleHandoverResource() resource.Resource { return &handoverResource{} }
func (*handoverResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_schedule_handover"
}
func (*handoverResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	team := uuidAttribute("Immutable owning team UUID, required without a default. Changing it replaces this settings slot.")
	schedule := uuidAttribute("Immutable schedule UUID belonging to team_id, required without a default. Changing it replaces this settings slot.")
	team.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	schedule.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one schedule's handover notification settings and selected on-call catalog bindings.", Attributes: map[string]schema.Attribute{
		"id":      schema.StringAttribute{Computed: true, MarkdownDescription: "Stable scoped identity `team_id/schedule_id`, also used for import.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"team_id": team, "schedule_id": schedule,
		"enabled":            schema.BoolAttribute{Required: true, MarkdownDescription: "Whether future observed on-call changes are broadcast. Required without a default; null is invalid. true requires at least one oncall_channel_ids entry. false retains explicitly selected channels until updated; destroy always clears them. Ordinary changes require an active owner; canonical false plus [] cleanup also works with an inactive owner or disabled schedule."},
		"oncall_channel_ids": schema.SetAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Complete set of 0–100 distinct nonzero canonical lowercase binding UUIDs from the owning team's on-call channel catalog. These are catalog binding IDs, not delivery-channel UUIDs. Required without a default; null is invalid and explicit [] clears selections. enabled=true requires a nonempty set. The provider performs no discovery, target lookup, preview or delivery.", Validators: []validator.Set{handoverChannelRule{}}},
		"revision":           schema.Int64Attribute{Computed: true, MarkdownDescription: "Independent handover-settings revision sent in the PUT body for compare-and-swap, without If-Match. Unwritten settings have revision 0; stored settings have positive revisions. Schedule settings/rotation revisions are never used. Stale writes return HTTP 409 and require a fresh plan."},
	}}
}

type handoverChannelRule struct{}

func (handoverChannelRule) Description(context.Context) string {
	return "At most 100 nonzero canonical lowercase on-call catalog binding UUIDs."
}
func (v handoverChannelRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v handoverChannelRule) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if len(req.ConfigValue.Elements()) > 100 {
		resp.Diagnostics.AddAttributeError(req.Path, "Too many handover channels", v.Description(ctx))
		return
	}
	for _, value := range req.ConfigValue.Elements() {
		s, ok := value.(types.String)
		if !ok || s.IsNull() || !s.IsUnknown() && !validID(s.ValueString()) {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid handover channel", v.Description(ctx))
			return
		}
	}
}
func handoverID(v handoverModel) string {
	return v.TeamID.ValueString() + "/" + v.ScheduleID.ValueString()
}
func handoverIdentity(v handoverModel) diag.Diagnostics {
	var d diag.Diagnostics
	if v.TeamID.IsNull() || v.TeamID.IsUnknown() || !validID(v.TeamID.ValueString()) || v.ScheduleID.IsNull() || v.ScheduleID.IsUnknown() || !validID(v.ScheduleID.ValueString()) || !v.ID.IsNull() && !v.ID.IsUnknown() && v.ID.ValueString() != handoverID(v) {
		d.AddError("Invalid handover identity", "Use known canonical lowercase team and schedule UUIDs matching the saved team_id/schedule_id identity.")
	}
	return d
}
func handoverInput(v handoverModel) (client.ScheduleHandover, diag.Diagnostics) {
	d := handoverIdentity(v)
	out := client.ScheduleHandover{Enabled: v.Enabled.ValueBool(), OncallChannelIDs: []string{}}
	if v.Enabled.IsUnknown() || v.Enabled.IsNull() || v.Channels.IsUnknown() || v.Channels.IsNull() {
		d.AddError("Unknown handover settings", "enabled and oncall_channel_ids must be known and non-null before saving.")
		return out, d
	}
	if len(v.Channels.Elements()) > 100 {
		d.AddError("Too many handover channels", "Select at most 100 catalog binding UUIDs.")
		return out, d
	}
	for _, value := range v.Channels.Elements() {
		s, ok := value.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() || !validID(s.ValueString()) {
			d.AddError("Invalid handover channel", "Every selected channel must be a known nonzero canonical lowercase catalog binding UUID.")
			return out, d
		}
		out.OncallChannelIDs = append(out.OncallChannelIDs, s.ValueString())
	}
	if out.Enabled && len(out.OncallChannelIDs) == 0 {
		d.AddError("Missing handover channels", "enabled=true requires at least one explicit oncall_channel_ids binding.")
	}
	return out, d
}
func (*handoverResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var v handoverModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &v)...)
	if resp.Diagnostics.HasError() || v.Enabled.IsUnknown() || v.Enabled.IsNull() || v.Channels.IsUnknown() || v.Channels.IsNull() {
		return
	}
	if v.Enabled.ValueBool() && len(v.Channels.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("oncall_channel_ids"), "Missing handover channels", "enabled=true requires at least one explicit on-call catalog binding UUID.")
	}
}
func handoverSave(ctx context.Context, state *tfsdk.State, p handoverModel, v client.ScheduleHandover, partial bool) diag.Diagnostics {
	var d diag.Diagnostics
	p.ID = types.StringValue(handoverID(p))
	p.Revision = types.Int64Null()
	if v.Revision > 0 {
		p.Revision = types.Int64Value(v.Revision)
	}
	if !partial {
		p.Enabled = types.BoolValue(v.Enabled)
		p.Channels, d = types.SetValueFrom(ctx, types.StringType, v.OncallChannelIDs)
	}
	d.Append(state.Set(ctx, p)...)
	return d
}
func (r *handoverResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p handoverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	input, d := handoverInput(p)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Only the exact handover slot provides the expected independent revision.
	before, err := r.client.GetScheduleHandover(ctx, p.TeamID.ValueString(), p.ScheduleID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read current OnPager handover settings", err)
		return
	}
	input.Revision = before.Revision
	v, err := r.client.PutScheduleHandover(ctx, p.TeamID.ValueString(), p.ScheduleID.ValueString(), input)
	if err != nil {
		if v.Committed {
			resp.Diagnostics.Append(handoverSave(ctx, &resp.State, p, v, true)...)
		}
		apiDiagnostic(&resp.Diagnostics, "save OnPager handover settings", err)
		return
	}
	resp.Diagnostics.Append(handoverSave(ctx, &resp.State, p, v, false)...)
}
func (r *handoverResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var p handoverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
	resp.Diagnostics.Append(handoverIdentity(p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetScheduleHandover(ctx, p.TeamID.ValueString(), p.ScheduleID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager handover settings", err)
		return
	}
	if v.Revision == 0 {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(handoverSave(ctx, &resp.State, p, v, false)...)
}
func (r *handoverResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, old handoverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	input, d := handoverInput(p)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(handoverIdentity(old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if handoverID(p) != handoverID(old) {
		resp.Diagnostics.AddError("Handover slot changed", "Changing team_id or schedule_id requires replacement.")
		return
	}
	if old.Revision.IsNull() || old.Revision.IsUnknown() || old.Revision.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Missing handover revision", "Refresh the exact handover slot before updating its settings.")
		return
	}
	input.Revision = old.Revision.ValueInt64()
	v, err := r.client.PutScheduleHandover(ctx, p.TeamID.ValueString(), p.ScheduleID.ValueString(), input)
	if err != nil {
		if v.Committed {
			resp.Diagnostics.Append(handoverSave(ctx, &resp.State, p, v, true)...)
		}
		apiDiagnostic(&resp.Diagnostics, "update OnPager handover settings", err)
		return
	}
	resp.Diagnostics.Append(handoverSave(ctx, &resp.State, p, v, false)...)
}
func (r *handoverResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var p handoverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
	resp.Diagnostics.Append(handoverIdentity(p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	before, err := r.client.GetScheduleHandover(ctx, p.TeamID.ValueString(), p.ScheduleID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager handover settings before disable", err)
		return
	}
	if before.Revision == 0 {
		return
	}
	if p.Revision.IsNull() || p.Revision.IsUnknown() || p.Revision.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Missing handover revision", "Refresh the exact handover slot before disabling its settings.")
		return
	}
	_, err = r.client.PutScheduleHandover(ctx, p.TeamID.ValueString(), p.ScheduleID.ValueString(), client.ScheduleHandover{Enabled: false, OncallChannelIDs: []string{}, Revision: p.Revision.ValueInt64()})
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager handover settings", err)
	}
}
func (*handoverResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || !validID(parts[0]) || !validID(parts[1]) {
		resp.Diagnostics.AddError("Invalid handover import ID", "Use team_id/schedule_id with nonzero canonical lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, handoverModel{ID: types.StringValue(req.ID), TeamID: types.StringValue(parts[0]), ScheduleID: types.StringValue(parts[1]), Enabled: types.BoolNull(), Channels: types.SetNull(types.StringType), Revision: types.Int64Null()})...)
}
