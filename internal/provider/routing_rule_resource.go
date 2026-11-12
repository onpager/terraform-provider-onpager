package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type routingRuleResource struct{ configuredResource }

func NewEscalationRuleResource() resource.Resource { return &routingRuleResource{} }
func (*routingRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_escalation_rule"
}
func (*routingRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one team's CEL escalation rule, target escalation chain and active state.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "team_id": routingOwnerAttribute("team"), "name": schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty rule name without leading or trailing whitespace.", Validators: []validator.String{stringRule{}}}, "cel_source": schema.StringAttribute{Required: true, MarkdownDescription: "CEL condition source selecting this rule. Supply the expression directly, without JSON encoding."}, "chain_id": uuidAttribute("Explicit escalation-chain UUID."), "active": routingActiveAttribute(), "revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Latest published revision. Rule publication does not use If-Match."},
	}}
}

type routingRuleModel struct {
	ID        types.String `tfsdk:"id"`
	TeamID    types.String `tfsdk:"team_id"`
	Name      types.String `tfsdk:"name"`
	CELSource types.String `tfsdk:"cel_source"`
	ChainID   types.String `tfsdk:"chain_id"`
	Active    types.Bool   `tfsdk:"active"`
	Revision  types.Int64  `tfsdk:"revision"`
}

func rulePayload(m routingRuleModel) client.EscalationRuleInput {
	return client.EscalationRuleInput{Name: m.Name.ValueString(), CELSource: m.CELSource.ValueString(), ChainID: m.ChainID.ValueString(), Active: m.Active.ValueBool()}
}
func ruleModel(v client.EscalationRule) routingRuleModel {
	return routingRuleModel{ID: types.StringValue(v.RuleID), TeamID: types.StringValue(v.TeamID), Name: types.StringValue(v.Name), CELSource: types.StringValue(v.CELSource), ChainID: types.StringValue(v.ChainID), Active: types.BoolValue(v.Active), Revision: types.Int64Value(v.Revision)}
}
func (r *routingRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p routingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.CreateEscalationRule(ctx, p.TeamID.ValueString(), rulePayload(p))
	if e != nil {
		// A 2xx response can still be incomplete. Retain a trustworthy identity
		// in the requested scope so refresh or destroy can recover the object.
		// Error HTTP responses are never decoded into v by the client.
		if validID(v.RuleID) && (v.TeamID == "" || v.TeamID == p.TeamID.ValueString()) {
			partial := p
			partial.ID = types.StringValue(v.RuleID)
			partial.Revision = types.Int64Null()
			if v.Revision > 0 {
				partial.Revision = types.Int64Value(v.Revision)
			}
			if v.Name != "" {
				partial.Name = types.StringValue(v.Name)
			}
			if v.CELSource != "" {
				partial.CELSource = types.StringValue(v.CELSource)
			}
			if validID(v.ChainID) {
				partial.ChainID = types.StringValue(v.ChainID)
			}
			if v.ActivePresent {
				partial.Active = types.BoolValue(v.Active)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &partial)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager escalation rule", e)
		return
	}
	m := ruleModel(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *routingRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var p routingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetEscalationRule(ctx, p.TeamID.ValueString(), p.ID.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager escalation rule", e)
		return
	}
	m := ruleModel(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *routingRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s routingRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.PublishEscalationRule(ctx, s.TeamID.ValueString(), s.ID.ValueString(), rulePayload(p))
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager escalation rule", e)
		return
	}
	m := ruleModel(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *routingRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s routingRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.DisableEscalationRule(ctx, s.TeamID.ValueString(), s.ID.ValueString()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager escalation rule", e)
	}
}
func (*routingRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	routingScopedImport(ctx, req, resp, "team_id")
}
func routingScopedImport(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, owner string) {
	scope, id, ok := splitImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid scoped import ID", "Use owner_uuid/object_uuid with canonical nonzero lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(owner), scope)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
