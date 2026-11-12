package provider

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type rbacRoleResource struct{ configuredResource }
type rbacRoleModel struct {
	ID             types.String `tfsdk:"id"`
	Key            types.String `tfsdk:"key"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	ScopeType      types.String `tfsdk:"scope_type"`
	PermissionKeys types.Set    `tfsdk:"permission_keys"`
	Revision       types.Int64  `tfsdk:"revision"`
}

func NewRBACRoleResource() resource.Resource { return &rbacRoleResource{} }
func (*rbacRoleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rbac_role"
}
func (*rbacRoleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one custom native RBAC role and its complete permission set.", Attributes: map[string]schema.Attribute{
		"id":              idAttribute(),
		"key":             schema.StringAttribute{Required: true, MarkdownDescription: "Immutable role key matching `^[a-z0-9][a-z0-9_-]{0,62}$`, unique in the tenant. Release-controlled built-in role keys are reserved. Changing the key replaces the role; duplicate-key creation never updates an existing role.", Validators: []validator.String{iamRoleKeyRule{}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name":            schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty valid UTF-8 display name without NUL or leading/trailing whitespace. Display names need not be unique.", Validators: []validator.String{iamTextRule{}}},
		"description":     schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Valid UTF-8 description without NUL. Omission or `null` defaults to an empty string; explicit empty string clears the description.", Validators: []validator.String{iamTextRule{empty: true}}},
		"scope_type":      schema.StringAttribute{Required: true, MarkdownDescription: "Immutable role scope: `tenant` or `team`. Permission keys must be custom-grantable in this scope. Changing it replaces the role.", Validators: []validator.String{enumRule{"tenant", "team"}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"permission_keys": schema.SetAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Complete set of exact native permission keys. Explicit `[]` creates or clears an empty draft; `null` is invalid. Unknown, reserved and scope-incompatible keys are rejected by the API. No wildcard or implicit grants exist. Roles assigned to service accounts must not contain human-only permissions."},
		"revision":        schema.Int64Attribute{Computed: true, MarkdownDescription: "Whole-role revision, validated against the API's strong numeric ETag and used in `If-Match` for replace and delete. Independent of tenant policy and assignment revisions; stale revisions report conflicts without retrying."},
	}}
}

var iamRoleKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type iamRoleKeyRule struct{}

func (iamRoleKeyRule) Description(context.Context) string {
	return "A custom role key matching ^[a-z0-9][a-z0-9_-]{0,62}$; built-in keys are reserved."
}
func (v iamRoleKeyRule) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v iamRoleKeyRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !iamRoleKey.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid custom role key", v.Description(ctx))
	}
}
func rbacRoleInput(ctx context.Context, v rbacRoleModel) (client.RBACRoleInput, error) {
	if v.Key.IsNull() || v.Key.IsUnknown() || !iamRoleKey.MatchString(v.Key.ValueString()) || v.Name.IsNull() || v.Name.IsUnknown() || !(iamTextRule{}).valid(v.Name.ValueString()) || v.ScopeType.IsUnknown() || (v.ScopeType.ValueString() != "tenant" && v.ScopeType.ValueString() != "team") || v.Description.IsUnknown() || !(iamTextRule{empty: true}).valid(v.Description.ValueString()) || v.PermissionKeys.IsNull() || v.PermissionKeys.IsUnknown() {
		return client.RBACRoleInput{}, errors.New("custom role key, name, description, scope_type and permission_keys must be valid known values before apply")
	}
	keys := make([]string, 0, len(v.PermissionKeys.Elements()))
	for _, element := range v.PermissionKeys.Elements() {
		key, ok := element.(types.String)
		if !ok || key.IsNull() || key.IsUnknown() || key.ValueString() == "" || strings.TrimSpace(key.ValueString()) != key.ValueString() || strings.ContainsRune(key.ValueString(), 0) {
			return client.RBACRoleInput{}, errors.New("custom role permission_keys must contain known nonempty native permission keys")
		}
		keys = append(keys, key.ValueString())
	}
	return client.RBACRoleInput{Key: v.Key.ValueString(), Name: v.Name.ValueString(), Description: v.Description.ValueString(), ScopeType: v.ScopeType.ValueString(), PermissionKeys: keys}, nil
}
func rbacRoleState(ctx context.Context, v client.RBACRole) rbacRoleModel {
	keys, _ := types.SetValueFrom(ctx, types.StringType, v.PermissionKeys)
	return rbacRoleModel{ID: types.StringValue(v.ID), Key: types.StringValue(v.Key), Name: types.StringValue(v.Name), Description: types.StringValue(v.Description), ScopeType: types.StringValue(v.ScopeType), PermissionKeys: keys, Revision: types.Int64Value(v.Revision)}
}
func rbacRoleIdentity(state rbacRoleModel, v client.RBACRole) bool {
	return (state.Key.IsNull() || state.Key.IsUnknown() || state.Key.ValueString() == v.Key) && (state.ScopeType.IsNull() || state.ScopeType.IsUnknown() || state.ScopeType.ValueString() == v.ScopeType)
}
func (r *rbacRoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rbacRoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := rbacRoleInput(ctx, plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager custom role", err)
		return
	}
	v, err := r.client.CreateRBACRole(ctx, in)
	if err != nil {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) && validID(v.ID) && !v.Builtin && (v.Key == "" || v.Key == in.Key) && (v.ScopeType == "" || v.ScopeType == in.ScopeType) {
			partial := plan
			partial.ID = types.StringValue(v.ID)
			partial.Revision = types.Int64Null()
			if v.Revision > 0 {
				partial.Revision = types.Int64Value(v.Revision)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager custom role", err)
		return
	}
	if !rbacRoleIdentity(plan, v) {
		resp.Diagnostics.AddError("Unexpected custom role identity", "The created custom role has an unexpected immutable key or scope.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, rbacRoleState(ctx, v))...)
}
func (r *rbacRoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rbacRoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetRBACRole(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager custom role", err)
		return
	}
	if !rbacRoleIdentity(state, v) {
		resp.Diagnostics.AddError("Unexpected custom role identity", "The custom role does not match the immutable key or scope recorded in state.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, rbacRoleState(ctx, v))...)
}
func (r *rbacRoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan rbacRoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := rbacRoleInput(ctx, plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager custom role", err)
		return
	}
	if !state.Key.Equal(plan.Key) || !state.ScopeType.Equal(plan.ScopeType) {
		resp.Diagnostics.AddError("Immutable custom role identity", "Changing a custom role key or scope requires replacement.")
		return
	}
	v, err := r.client.UpdateRBACRole(ctx, state.ID.ValueString(), state.Revision.ValueInt64(), client.RBACRoleUpdate{Name: in.Name, Description: in.Description, PermissionKeys: in.PermissionKeys})
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager custom role", err)
		return
	}
	if !rbacRoleIdentity(state, v) {
		resp.Diagnostics.AddError("Unexpected custom role identity", "The updated custom role does not match the immutable key or scope recorded in state.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, rbacRoleState(ctx, v))...)
}
func (r *rbacRoleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rbacRoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteRBACRole(ctx, state.ID.ValueString(), state.Revision.ValueInt64()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager custom role", err)
	}
}
func (*rbacRoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
