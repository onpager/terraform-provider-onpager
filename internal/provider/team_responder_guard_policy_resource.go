package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type teamResponderGuardPolicyResource struct{ configuredResource }

type teamResponderGuardPolicyModel struct {
	ID                        types.String `tfsdk:"id"`
	TeamID                    types.String `tfsdk:"team_id"`
	MaxOpenRequestsPerSubject types.Int64  `tfsdk:"max_open_requests_per_subject"`
	Revision                  types.Int64  `tfsdk:"revision"`
}

func NewTeamResponderGuardPolicyResource() resource.Resource {
	return &teamResponderGuardPolicyResource{}
}

func (*teamResponderGuardPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_responder_guard_policy"
}

func (*teamResponderGuardPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	team := uuidAttribute("Immutable owning team UUID, required without a default. Null and empty strings are invalid. Changing it replaces the resource.")
	team.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage one team's explicit responder guard policy override. Create requires an unconfigured override; import existing policies before applying. Destroy resets the override to the current backend default and preserves historical publications.",
		Attributes: map[string]schema.Attribute{
			"id":                            schema.StringAttribute{Computed: true, MarkdownDescription: "Stable policy identity and import ID: the owning team UUID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"team_id":                       team,
			"max_open_requests_per_subject": schema.Int64Attribute{Required: true, MarkdownDescription: "Explicit maximum pending responder requests for the initiator team per subject, across owners. Required integer from 1 through 1000, without a default or unlimited value. Omitted/null and unknown values at apply are invalid. Publishing requires an active team; setting a value equal to the current backend default still creates an explicit override.", Validators: []validator.Int64{teamResponderGuardLimitRule{}}},
			"revision":                      schema.Int64Attribute{Computed: true, MarkdownDescription: "Positive observed publication revision for this explicit override. This counter is metadata, never an If-Match or other write precondition. Concurrent publications apply in backend commit order; updates and resets are not compare-and-swap operations."},
		},
	}
}

type teamResponderGuardLimitRule struct{}

func (teamResponderGuardLimitRule) Description(context.Context) string {
	return "An explicit integer from 1 through 1000; there is no unlimited value."
}
func (v teamResponderGuardLimitRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v teamResponderGuardLimitRule) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if req.ConfigValue.ValueInt64() < 1 || req.ConfigValue.ValueInt64() > 1000 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid responder guard limit", v.Description(ctx))
	}
}

func teamResponderGuardIdentity(m teamResponderGuardPolicyModel, saved bool) error {
	if m.TeamID.IsNull() || m.TeamID.IsUnknown() || !validID(m.TeamID.ValueString()) {
		return errors.New("team_id must be a known nonzero canonical lowercase team UUID before making a request")
	}
	if saved && (m.ID.IsNull() || m.ID.IsUnknown()) || !m.ID.IsNull() && !m.ID.IsUnknown() && m.ID.ValueString() != m.TeamID.ValueString() {
		return errors.New("the policy ID must match the immutable owning team UUID; state is retained")
	}
	return nil
}

func teamResponderGuardPayload(m teamResponderGuardPolicyModel) (client.TeamResponderGuardPolicyInput, error) {
	out := client.TeamResponderGuardPolicyInput{MaxOpenRequestsPerSubject: m.MaxOpenRequestsPerSubject.ValueInt64()}
	if err := teamResponderGuardIdentity(m, false); err != nil {
		return out, err
	}
	if m.MaxOpenRequestsPerSubject.IsNull() || m.MaxOpenRequestsPerSubject.IsUnknown() || out.MaxOpenRequestsPerSubject < 1 || out.MaxOpenRequestsPerSubject > 1000 {
		return out, errors.New("max_open_requests_per_subject must be a known explicit integer from 1 through 1000 before apply")
	}
	return out, nil
}

func teamResponderGuardState(team string, v client.TeamResponderGuardPolicy) teamResponderGuardPolicyModel {
	return teamResponderGuardPolicyModel{ID: types.StringValue(team), TeamID: types.StringValue(team), MaxOpenRequestsPerSubject: types.Int64Value(v.MaxOpenRequestsPerSubject), Revision: types.Int64Value(v.Revision)}
}

func teamResponderGuardCommittedIntent(m teamResponderGuardPolicyModel) teamResponderGuardPolicyModel {
	m.ID = m.TeamID
	m.Revision = types.Int64Null()
	return m
}

func (r *teamResponderGuardPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p teamResponderGuardPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := teamResponderGuardPayload(p)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager team responder guard policy", err)
		return
	}
	current, err := r.client.GetTeamResponderGuardPolicy(ctx, p.TeamID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read unowned OnPager team responder guard policy", err)
		return
	}
	if !current.IsDefault {
		resp.Diagnostics.AddError("Existing responder guard policy requires import", "This team already has an explicit override that is not owned by this resource. Import the owning team UUID before applying, even if the configured limit matches. Create never adopts or overwrites an unowned override.")
		return
	}
	v, err := r.client.PublishTeamResponderGuardPolicy(ctx, p.TeamID.ValueString(), in)
	if v.Committed && err != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderGuardCommittedIntent(p))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "publish initial OnPager team responder guard policy", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderGuardState(p.TeamID.ValueString(), v))...)
}

func (r *teamResponderGuardPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s teamResponderGuardPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := teamResponderGuardIdentity(s, true); err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager team responder guard policy", err)
		return
	}
	v, err := r.client.GetTeamResponderGuardPolicy(ctx, s.TeamID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager team responder guard policy", err)
		return
	}
	if v.IsDefault {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderGuardState(s.TeamID.ValueString(), v))...)
}

func (r *teamResponderGuardPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s teamResponderGuardPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := teamResponderGuardPayload(p)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager team responder guard policy", err)
		return
	}
	if err := teamResponderGuardIdentity(s, true); err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager team responder guard policy", err)
		return
	}
	if !p.TeamID.Equal(s.TeamID) {
		resp.Diagnostics.AddError("Responder guard policy owner changed", "Changing the immutable team_id requires replacement.")
		return
	}
	v, err := r.client.PublishTeamResponderGuardPolicy(ctx, s.TeamID.ValueString(), in)
	if v.Committed && err != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderGuardCommittedIntent(p))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager team responder guard policy", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamResponderGuardState(s.TeamID.ValueString(), v))...)
}

func (r *teamResponderGuardPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s teamResponderGuardPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := teamResponderGuardIdentity(s, true); err != nil {
		apiDiagnostic(&resp.Diagnostics, "reset OnPager team responder guard policy", err)
		return
	}
	if err := r.client.ResetTeamResponderGuardPolicy(ctx, s.TeamID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "reset OnPager team responder guard policy", err)
	}
}

func (*teamResponderGuardPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !validID(req.ID) {
		resp.Diagnostics.AddError("Invalid responder guard policy import ID", "Use the owning team's nonzero canonical lowercase UUID. Import reads only and requires an existing explicit override.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), req.ID)...)
}
