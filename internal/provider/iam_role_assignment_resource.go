package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type roleAssignmentResource struct{ configuredResource }
type roleAssignmentModel struct {
	ID           types.String `tfsdk:"id"`
	RoleID       types.String `tfsdk:"role_id"`
	Recipient    types.Object `tfsdk:"recipient"`
	TargetTeamID types.String `tfsdk:"target_team_id"`
}

var assignmentRecipientTypes = map[string]attr.Type{"kind": types.StringType, "id": types.StringType}

func NewRoleAssignmentResource() resource.Resource { return &roleAssignmentResource{} }
func (*roleAssignmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role_assignment"
}
func (*roleAssignmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	role := uuidAttribute("Existing role UUID. The role and its scope determine which permissions are granted. Changes update the assignment atomically while preserving its UUID.")
	target := optionalUUID("Target team UUID for a team-scoped role. Omission or null denotes a tenant-scoped assignment. The API validates role/target compatibility. Changes update the assignment atomically while preserving its UUID.")
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one native role assignment for an explicit role, recipient and target scope. Changes atomically replace the full assignment tuple while preserving its UUID and comparing its saved prior tuple.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "role_id": role, "target_team_id": target,
		"recipient": schema.SingleNestedAttribute{Required: true, MarkdownDescription: "Exact recipient of this role assignment. Changing its kind or UUID updates the assignment atomically. Ownership and token issuance do not create this grant implicitly.", Attributes: map[string]schema.Attribute{
			"kind": schema.StringAttribute{Required: true, MarkdownDescription: "Recipient kind: `user`, `service_account`, `group` or `team`. Team and group recipients grant access through their current eligible memberships.", Validators: []validator.String{enumRule{"user", "service_account", "group", "team"}}},
			"id":   uuidAttribute("Existing recipient UUID matching the selected kind."),
		}},
	}}
}
func roleAssignmentInput(m roleAssignmentModel) (client.RoleAssignmentInput, error) {
	var in client.RoleAssignmentInput
	if m.RoleID.IsUnknown() || !validID(m.RoleID.ValueString()) || m.Recipient.IsUnknown() || m.Recipient.IsNull() {
		return in, errors.New("role assignment identifiers must be known canonical UUIDs before apply")
	}
	in.RoleID = m.RoleID.ValueString()
	fields := m.Recipient.Attributes()
	kind, ok := fields["kind"].(types.String)
	if !ok || kind.IsUnknown() || kind.IsNull() {
		return in, errors.New("recipient kind must be known before apply")
	}
	switch kind.ValueString() {
	case "user", "service_account", "group", "team":
	default:
		return in, errors.New("invalid role assignment recipient kind")
	}
	id, ok := fields["id"].(types.String)
	if !ok || id.IsUnknown() || !validID(id.ValueString()) {
		return in, errors.New("recipient UUID must be known and canonical before apply")
	}
	in.Recipient = client.RoleRecipient{Kind: kind.ValueString(), ID: id.ValueString()}
	owner, err := iamOptionalID(m.TargetTeamID)
	in.TargetTeamID = owner
	return in, err
}
func roleAssignmentState(v client.RoleAssignment) roleAssignmentModel {
	return roleAssignmentModel{
		ID: types.StringValue(v.ID), RoleID: types.StringValue(v.RoleID), TargetTeamID: iamNullableID(v.TargetTeamID),
		Recipient: types.ObjectValueMust(assignmentRecipientTypes, map[string]attr.Value{"kind": types.StringValue(v.Recipient.Kind), "id": types.StringValue(v.Recipient.ID)}),
	}
}
func assignmentReceiptCompatible(v client.RoleAssignment, in client.RoleAssignmentInput) bool {
	targetMatches := true
	if v.TargetTeamPresent || v.TargetTeamID != nil {
		targetMatches = v.TargetTeamID == nil && in.TargetTeamID == nil || v.TargetTeamID != nil && in.TargetTeamID != nil && *v.TargetTeamID == *in.TargetTeamID
	}
	return !v.Protected && (v.RoleID == "" || v.RoleID == in.RoleID) && (v.Recipient.Kind == "" || v.Recipient.Kind == in.Recipient.Kind) && (v.Recipient.ID == "" || v.Recipient.ID == in.Recipient.ID) && targetMatches
}
func (r *roleAssignmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan roleAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := roleAssignmentInput(plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager role assignment", err)
		return
	}
	value, err := r.client.CreateRoleAssignment(ctx, in)
	if err != nil {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) && validID(value.ID) && assignmentReceiptCompatible(value, in) {
			plan.ID = types.StringValue(value.ID)
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager role assignment", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, roleAssignmentState(value))...)
}
func (r *roleAssignmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var old roleAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.client.GetRoleAssignment(ctx, old.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager role assignment", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, roleAssignmentState(value))...)
}
func (r *roleAssignmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, old roleAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	desired, err := roleAssignmentInput(plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager role assignment", err)
		return
	}
	expected, err := roleAssignmentInput(old)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read saved OnPager role assignment tuple", err)
		return
	}
	value, err := r.client.UpdateRoleAssignment(ctx, old.ID.ValueString(), desired, expected)
	if value.Committed {
		// A successful update can remove the caller's own read permission. Save
		// committed intent even if its receipt is malformed; never issue a GET.
		plan.ID = old.ID
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager role assignment", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, roleAssignmentState(value))...)
}
func (r *roleAssignmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var old roleAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteRoleAssignment(ctx, old.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "revoke OnPager role assignment", err)
	}
}
func (*roleAssignmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
