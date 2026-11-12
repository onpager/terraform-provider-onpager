package provider

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type serviceAccountResource struct{ configuredResource }
type serviceAccountModel struct {
	ID          types.String `tfsdk:"id"`
	DisplayName types.String `tfsdk:"display_name"`
	OwnerTeamID types.String `tfsdk:"owner_team_id"`
	Active      types.Bool   `tfsdk:"active"`
}

func NewServiceAccountResource() resource.Resource { return &serviceAccountResource{} }
func (*serviceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}
func (*serviceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage a native OnPager service account, its management owner and active state.", Attributes: map[string]schema.Attribute{
		"id":            idAttribute(),
		"display_name":  schema.StringAttribute{Required: true, MarkdownDescription: "Account label of 1–128 UTF-8 bytes, without NUL or leading/trailing whitespace. Display names need not be unique.", Validators: []validator.String{iamTextRule{label: true}}},
		"owner_team_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Management owner team UUID. Omission or `null` means tenant-level management and clears any prior owner. Must be a nonzero canonical lowercase UUID when set. Updates require `service_accounts.update` in both old and new areas. Ownership does not grant business access or limit the account's roles.", Validators: []validator.String{stringRule{uuid: true}}},
		"active":        schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether the account is active. Defaults to `true`. Disabling revokes every account key. Reactivation requires an active owner team and never revives revoked keys. Refresh/import preserve inactive accounts without mutations."},
	}}
}

type iamTextRule struct {
	label bool
	empty bool
}

func (v iamTextRule) Description(context.Context) string {
	return "Valid UTF-8 text without NUL; names must be nonempty without surrounding whitespace, and account labels must be at most 128 bytes."
}
func (v iamTextRule) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v iamTextRule) valid(s string) bool {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return false
	}
	if !v.empty && (s == "" || strings.TrimSpace(s) != s) {
		return false
	}
	return !v.label || len(s) <= 128
}
func (v iamTextRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !v.valid(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid OnPager text", v.Description(ctx))
	}
}
func iamOptionalID(v types.String) (*string, error) {
	if v.IsUnknown() {
		return nil, errors.New("OnPager owner UUID must be known before apply")
	}
	if v.IsNull() {
		return nil, nil
	}
	s := v.ValueString()
	if !validID(s) {
		return nil, errors.New("OnPager owner UUID must be a nonzero canonical lowercase UUID")
	}
	return &s, nil
}
func iamNullableID(v *string) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(*v)
}
func serviceAccountState(v client.ServiceAccount) serviceAccountModel {
	return serviceAccountModel{ID: types.StringValue(v.ID), DisplayName: types.StringValue(v.DisplayName), OwnerTeamID: iamNullableID(v.OwnerTeamID), Active: types.BoolValue(v.Active)}
}
func serviceAccountInput(v serviceAccountModel) (client.ServiceAccountInput, error) {
	if v.DisplayName.IsNull() || v.DisplayName.IsUnknown() || !(iamTextRule{label: true}).valid(v.DisplayName.ValueString()) || v.Active.IsUnknown() || v.Active.IsNull() {
		return client.ServiceAccountInput{}, errors.New("service account display_name and active must be valid known values before apply")
	}
	owner, err := iamOptionalID(v.OwnerTeamID)
	return client.ServiceAccountInput{DisplayName: v.DisplayName.ValueString(), OwnerTeamID: owner}, err
}
func (r *serviceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := serviceAccountInput(plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager service account", err)
		return
	}
	v, err := r.client.CreateServiceAccount(ctx, in)
	if err != nil {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) && validID(v.ID) {
			partial := plan
			partial.ID = types.StringValue(v.ID)
			partial.Active = types.BoolValue(true)
			resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager service account", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceAccountState(v))...)
	if resp.Diagnostics.HasError() || plan.Active.ValueBool() == v.Active {
		return
	}
	if plan.Active.ValueBool() {
		err = r.client.ReactivateServiceAccount(ctx, v.ID)
	} else {
		err = r.client.DeactivateServiceAccount(ctx, v.ID)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "change newly created OnPager service account active state", err)
		return
	}
	v.Active = plan.Active.ValueBool()
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceAccountState(v))...)
}
func (r *serviceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetServiceAccount(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager service account", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceAccountState(v))...)
}
func (r *serviceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := serviceAccountInput(plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager service account", err)
		return
	}
	if !state.DisplayName.Equal(plan.DisplayName) || !state.OwnerTeamID.Equal(plan.OwnerTeamID) {
		v, err := r.client.UpdateServiceAccount(ctx, state.ID.ValueString(), in)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "update OnPager service account", err)
			return
		}
		state = serviceAccountState(v)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !state.Active.Equal(plan.Active) {
		if plan.Active.ValueBool() {
			err = r.client.ReactivateServiceAccount(ctx, state.ID.ValueString())
		} else {
			err = r.client.DeactivateServiceAccount(ctx, state.ID.ValueString())
		}
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "change OnPager service account active state", err)
			return
		}
		state.Active = plan.Active
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}
func (r *serviceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.client.GetServiceAccount(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager service account before deactivation", err)
		return
	}
	if !current.Active {
		return
	}
	if err = r.client.DeactivateServiceAccount(ctx, state.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "deactivate OnPager service account", err)
	}
}
func (*serviceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
