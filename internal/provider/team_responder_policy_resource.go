package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type teamResponderPolicyResource struct{ configuredResource }
type teamResponderPolicyModel struct {
	ID                 types.String `tfsdk:"id"`
	TeamID             types.String `tfsdk:"team_id"`
	Importance         types.String `tfsdk:"importance"`
	Strategy           types.String `tfsdk:"strategy"`
	ChainID            types.String `tfsdk:"chain_id"`
	PublicationTrigger types.String `tfsdk:"publication_trigger"`
	Revision           types.Int64  `tfsdk:"revision"`
	ChainSteps         types.List   `tfsdk:"chain_steps"`
}

var teamResponderStepTypes = map[string]attr.Type{"kind": types.StringType, "wait_seconds": types.Int64Type, "target_user_id": types.StringType, "schedule_id": types.StringType, "importance": types.StringType}
var teamResponderStepType = types.ObjectType{AttrTypes: teamResponderStepTypes}

func NewTeamResponderPolicyResource() resource.Resource { return &teamResponderPolicyResource{} }
func (*teamResponderPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_responder_policy"
}
func (*teamResponderPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	team := uuidAttribute("Immutable owning team UUID. Required without a default. Changing it replaces this resource.")
	team.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one exact team responder policy importance slot. Control publishes immutable revisions and derives chain snapshots. Destroy resets only this slot while preserving history and existing responder calls. Configure one resource per team and importance; import an existing slot before managing it.", Attributes: map[string]schema.Attribute{
		"id":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Stable slot identity and import ID: `team_uuid/default` or `team_uuid/important`."},
		"team_id":             team,
		"importance":          schema.StringAttribute{Required: true, MarkdownDescription: "Immutable exact importance slot: `default` or `important`. Explicitly required; no default, fallback or cross-slot ownership. Changing it replaces this resource.", Validators: []validator.String{enumRule{"default", "important"}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"strategy":            schema.StringAttribute{Required: true, MarkdownDescription: "Responder strategy, explicitly `team_fanout` or `team_chain`. Fanout omits chain_id; chain requires an existing owned escalation chain. Publication requires an active team and does not create responder calls or deliver notifications.", Validators: []validator.String{enumRule{"team_fanout", "team_chain"}}},
		"chain_id":            schema.StringAttribute{Optional: true, MarkdownDescription: "Existing nonzero canonical lowercase escalation-chain UUID. Required only for `team_chain` and must belong to this team. Omit/null for `team_fanout`; empty is invalid. Control derives and saves a canonical immutable snapshot with at least one user/on-call notification and a final wait; channel/webhook steps are unsupported. Later edits to the chain do not change the saved policy until re-publication.", Validators: []validator.String{stringRule{uuid: true}}},
		"publication_trigger": schema.StringAttribute{Optional: true, MarkdownDescription: "Optional opaque Terraform-only string. A change causes in-place re-publication, for example when set to an escalation chain's `etag`. Null/omitted is valid; empty is a distinct valid value. Preserved across refresh, unavailable/null on import, never sent to Control. This is an explicit re-publication trigger, not an API revision, If-Match value or concurrency guarantee."},
		"revision":            schema.Int64Attribute{Computed: true, MarkdownDescription: "Observed positive immutable publication revision for this exact slot. A counter only: publication and reset do not send If-Match or use compare-and-swap. Concurrent publications apply in server commit order."},
		"chain_steps": schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Ordered canonical snapshot returned by Control. Empty [] for fanout. Read-only metadata: configuration cannot supply compatibility steps. A chain retains this snapshot until explicitly re-published; existing responder calls retain their own snapshots.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"kind":           schema.StringAttribute{Computed: true, MarkdownDescription: "Canonical step kind: `wait`, `notify_user`, or `notify_oncall`."},
			"wait_seconds":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Whole seconds for a wait, 1..9223372036. Null for notification steps."},
			"target_user_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Target user UUID for notify_user; null for other kinds."},
			"schedule_id":    schema.StringAttribute{Computed: true, MarkdownDescription: "Owned schedule UUID for notify_oncall; null for other kinds."},
			"importance":     schema.StringAttribute{Computed: true, MarkdownDescription: "Optional notification importance, default or important. Null when omitted, including canonical waits."},
		}}},
	}}
}

func (*teamResponderPolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m teamResponderPolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Strategy.IsUnknown() {
		return
	}
	switch m.Strategy.ValueString() {
	case "team_chain":
		if m.ChainID.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("chain_id"), "Missing responder chain", "team_chain requires an existing owned chain_id; Control derives the canonical snapshot.")
		}
	case "team_fanout":
		if !m.ChainID.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("chain_id"), "Unexpected responder chain", "Omit chain_id for team_fanout.")
		}
	}
}
func teamResponderPolicyInput(m teamResponderPolicyModel) (client.TeamResponderPolicyInput, error) {
	out := client.TeamResponderPolicyInput{Importance: m.Importance.ValueString(), Strategy: m.Strategy.ValueString()}
	if m.TeamID.IsUnknown() || !validID(m.TeamID.ValueString()) || m.Importance.IsUnknown() || out.Importance != "default" && out.Importance != "important" || m.Strategy.IsUnknown() || m.PublicationTrigger.IsUnknown() {
		return out, errors.New("team responder policy requires a known canonical team UUID, explicit importance/strategy and a known publication_trigger before apply")
	}
	switch out.Strategy {
	case "team_fanout":
		if !m.ChainID.IsNull() {
			return out, errors.New("team_fanout requires chain_id to be omitted/null")
		}
	case "team_chain":
		if m.ChainID.IsUnknown() || !validID(m.ChainID.ValueString()) {
			return out, errors.New("team_chain requires a known nonzero canonical chain_id before apply")
		}
		out.ChainID = m.ChainID.ValueString()
	default:
		return out, errors.New("strategy must be team_fanout or team_chain")
	}
	return out, nil
}
func teamResponderPolicyIdentity(m teamResponderPolicyModel) error {
	if m.ID.IsUnknown() || m.TeamID.IsUnknown() || !validID(m.TeamID.ValueString()) || m.Importance.IsUnknown() || m.Importance.ValueString() != "default" && m.Importance.ValueString() != "important" || m.ID.ValueString() != m.TeamID.ValueString()+"/"+m.Importance.ValueString() {
		return errors.New("team responder policy state must identify an exact canonical team_uuid/default or team_uuid/important slot; refresh or import before retrying")
	}
	return nil
}
func teamResponderPolicyReceipt(m teamResponderPolicyModel) teamResponderPolicyModel {
	m.ID = types.StringValue(m.TeamID.ValueString() + "/" + m.Importance.ValueString())
	m.Revision = types.Int64Null()
	m.ChainSteps = types.ListNull(teamResponderStepType)
	return m
}
func teamResponderPolicyState(v client.TeamResponderPolicy, trigger types.String) teamResponderPolicyModel {
	steps := make([]attr.Value, 0, len(v.ChainSteps))
	for _, s := range v.ChainSteps {
		wait, user, schedule, importance := types.Int64Null(), types.StringNull(), types.StringNull(), types.StringNull()
		if s.Kind == "wait" {
			wait = types.Int64Value(s.WaitSeconds)
		}
		if s.TargetUserID != nil {
			user = types.StringValue(*s.TargetUserID)
		}
		if s.ScheduleID != nil {
			schedule = types.StringValue(*s.ScheduleID)
		}
		if s.Importance != "" {
			importance = types.StringValue(s.Importance)
		}
		steps = append(steps, types.ObjectValueMust(teamResponderStepTypes, map[string]attr.Value{"kind": types.StringValue(s.Kind), "wait_seconds": wait, "target_user_id": user, "schedule_id": schedule, "importance": importance}))
	}
	chain := types.StringNull()
	if v.Strategy == "team_chain" {
		chain = types.StringValue(v.ChainID)
	}
	return teamResponderPolicyModel{ID: types.StringValue(v.TeamID + "/" + v.Importance), TeamID: types.StringValue(v.TeamID), Importance: types.StringValue(v.Importance), Strategy: types.StringValue(v.Strategy), ChainID: chain, PublicationTrigger: trigger, Revision: types.Int64Value(v.Revision), ChainSteps: types.ListValueMust(teamResponderStepType, steps)}
}
func (r *teamResponderPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m teamResponderPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := teamResponderPolicyInput(m)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager team responder policy", err)
		return
	}
	current, err := r.client.GetTeamResponderPolicy(ctx, m.TeamID.ValueString(), in.Importance)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read unowned OnPager team responder policy slot", err)
		return
	}
	if current.Configured {
		resp.Diagnostics.AddError("Existing team responder policy requires import", "This exact team/importance slot already contains an unowned publication, even if its configuration matches. Import it with team_uuid/importance before applying. Create never adopts or merges an existing publication.")
		return
	}
	v, err := r.client.PublishTeamResponderPolicy(ctx, m.TeamID.ValueString(), in)
	if v.Committed {
		resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderPolicyReceipt(m))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "publish initial OnPager team responder policy", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderPolicyState(v, m.PublicationTrigger))...)
}
func (r *teamResponderPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m teamResponderPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := teamResponderPolicyIdentity(m); err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager team responder policy", err)
		return
	}
	v, err := r.client.GetTeamResponderPolicy(ctx, m.TeamID.ValueString(), m.Importance.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager team responder policy", err)
		return
	}
	if !v.Configured {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderPolicyState(v, m.PublicationTrigger))...)
}
func (r *teamResponderPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, s teamResponderPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := teamResponderPolicyIdentity(s); err != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager team responder policy", err)
		return
	}
	if !m.TeamID.Equal(s.TeamID) || !m.Importance.Equal(s.Importance) {
		resp.Diagnostics.AddError("Immutable responder policy slot changed", "Changing team_id or importance requires replacement.")
		return
	}
	in, err := teamResponderPolicyInput(m)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager team responder policy", err)
		return
	}
	v, err := r.client.PublishTeamResponderPolicy(ctx, m.TeamID.ValueString(), in)
	if v.Committed {
		resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderPolicyReceipt(m))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager team responder policy", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderPolicyState(v, m.PublicationTrigger))...)
}
func (r *teamResponderPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m teamResponderPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := teamResponderPolicyIdentity(m); err != nil {
		apiDiagnostic(&resp.Diagnostics, "reset OnPager team responder policy", err)
		return
	}
	if err := r.client.ResetTeamResponderPolicy(ctx, m.TeamID.ValueString(), m.Importance.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "reset OnPager team responder policy", err)
	}
}
func (*teamResponderPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || !validID(parts[0]) || parts[1] != "default" && parts[1] != "important" {
		resp.Diagnostics.AddError("Invalid team responder policy import ID", "Use team_uuid/default or team_uuid/important with a nonzero canonical lowercase team UUID and the exact importance.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("importance"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("publication_trigger"), types.StringNull())...)
}
