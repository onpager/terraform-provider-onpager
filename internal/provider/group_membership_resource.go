package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type groupMembershipResource struct{ configuredResource }
type groupMembershipModel struct {
	ID      types.String `tfsdk:"id"`
	GroupID types.String `tfsdk:"group_id"`
	UserID  types.String `tfsdk:"user_id"`
}

func NewGroupMembershipResource() resource.Resource { return &groupMembershipResource{} }
func (*groupMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_membership"
}
func groupOwnerAttribute() schema.StringAttribute {
	a := uuidAttribute("Custom group UUID. Changing it replaces the resource. The derived All Users group cannot be managed.")
	a.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	return a
}
func (*groupMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	id := idAttribute()
	id.MarkdownDescription = "Explicit manual source identity `group_uuid/user_uuid`, also used for import. Both components are nonzero canonical lowercase UUIDs."
	user := uuidAttribute("Existing human user UUID. Changing it replaces the manual source. This resource does not create users or manage OIDC-derived membership.")
	user.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one manual group membership source for an existing user.", Attributes: map[string]schema.Attribute{"id": id, "group_id": groupOwnerAttribute(), "user_id": user}}
}
func groupMembershipState(group, user string) groupMembershipModel {
	return groupMembershipModel{ID: types.StringValue(group + "/" + user), GroupID: types.StringValue(group), UserID: types.StringValue(user)}
}
func (r *groupMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p groupMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.EnsureGroupMembership(ctx, p.GroupID.ValueString(), p.UserID.ValueString()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "ensure OnPager manual group membership", e)
		return
	}
	// The idempotent 204 commits this exact pair, so no follow-up read can erase it.
	resp.Diagnostics.Append(resp.State.Set(ctx, groupMembershipState(p.GroupID.ValueString(), p.UserID.ValueString()))...)
}
func (r *groupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s groupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetGroupMembership(ctx, s.GroupID.ValueString(), s.UserID.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager manual group membership", e)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupMembershipState(v.GroupID, v.UserID))...)
}
func (r *groupMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p groupMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
	}
}
func (r *groupMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s groupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.RemoveGroupMembership(ctx, s.GroupID.ValueString(), s.UserID.ValueString()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "remove OnPager manual group membership", e)
	}
}
func (*groupMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	group, user, ok := splitImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid manual group membership import ID", "Use group_uuid/user_uuid with two nonzero canonical lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), group)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), user)...)
}
