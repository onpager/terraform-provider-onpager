package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type teamMembershipResource struct{ configuredResource }
type membershipModel struct {
	ID     types.String `tfsdk:"id"`
	TeamID types.String `tfsdk:"team_id"`
	UserID types.String `tfsdk:"user_id"`
	Lead   types.Bool   `tfsdk:"lead"`
}

func NewTeamMembershipResource() resource.Resource { return &teamMembershipResource{} }
func (*teamMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_membership"
}
func (*teamMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	team, user := uuidAttribute("Team UUID. Changing it replaces the membership."), uuidAttribute("Existing active user UUID. Changing it replaces the membership.")
	id := idAttribute()
	id.MarkdownDescription = "Composite membership identity `team_uuid/user_uuid`, also used for import. Both components are nonzero canonical lowercase UUIDs."
	team.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	user.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage a user's team membership and lead marker.", Attributes: map[string]schema.Attribute{
		"id": id, "team_id": team, "user_id": user,
		"lead": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Whether this membership has the lead marker, which affects team default roles. Defaults to `false`. This marker does not assign an explicit native RBAC role."},
	}}
}
func membershipState(v client.Membership) membershipModel {
	return membershipModel{ID: types.StringValue(v.TeamID + "/" + v.UserID), TeamID: types.StringValue(v.TeamID), UserID: types.StringValue(v.UserID), Lead: types.BoolValue(v.Lead)}
}
func (r *teamMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.AddMembership(ctx, plan.TeamID.ValueString(), plan.UserID.ValueString(), plan.Lead.ValueBool())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager membership", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, membershipState(v))...)
}
func (r *teamMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetMembership(ctx, state.TeamID.ValueString(), state.UserID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager membership", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, membershipState(v))...)
}
func (r *teamMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.UpdateMembership(ctx, plan.TeamID.ValueString(), plan.UserID.ValueString(), plan.Lead.ValueBool())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager membership lead marker", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, membershipState(v))...)
}
func (r *teamMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.RemoveMembership(ctx, state.TeamID.ValueString(), state.UserID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "revoke OnPager membership", err)
	}
}
func (*teamMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || !validID(parts[0]) || !validID(parts[1]) {
		resp.Diagnostics.AddError("Invalid membership import ID", "Use team_uuid/user_uuid with two nonzero canonical lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), parts[1])...)
}
