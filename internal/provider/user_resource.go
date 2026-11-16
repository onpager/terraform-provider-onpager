package provider

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type userResource struct{ configuredResource }
type userModel struct {
	ID                            types.String `tfsdk:"id"`
	DisplayName                   types.String `tfsdk:"display_name"`
	Login                         types.String `tfsdk:"login"`
	LoginNormalized               types.String `tfsdk:"login_normalized"`
	Email                         types.String `tfsdk:"email"`
	EmailNormalized               types.String `tfsdk:"email_normalized"`
	Active                        types.Bool   `tfsdk:"active"`
	InitialPassword               types.String `tfsdk:"initial_password"`
	InitialPasswordChangeRequired types.Bool   `tfsdk:"initial_password_change_required"`
}

func NewUserResource() resource.Resource { return &userResource{} }
func (*userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}
func (*userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage a local OnPager user, metadata and active state with a creation-only write-only password.", Attributes: map[string]schema.Attribute{
		"id":                               idAttribute(),
		"display_name":                     schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty display name without NUL or surrounding whitespace. Mutable; sent through an unversioned sparse metadata PATCH.", Validators: []validator.String{iamTextRule{}}},
		"login":                            schema.StringAttribute{Required: true, MarkdownDescription: "Immutable local login, without NUL or surrounding whitespace. The backend Unicode case-folds it and enforces uniqueness, including retained inactive users. The configured spelling is preserved; login_normalized reports the stored value. Changing login replaces the user, deactivating the old UUID. For import, configure the stored login spelling.", Validators: []validator.String{iamTextRule{}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"login_normalized":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Observed backend-normalized local login from the direct UUID read. OIDC-only users and protected bootstrap users cannot be managed by this resource."},
		"email":                            schema.StringAttribute{Optional: true, MarkdownDescription: "Human email. Omission or null means no email and clears a previously configured email; an empty or whitespace-only string also clears it. The backend trims and Unicode case-folds nonempty input; its observed value is email_normalized. Creation accepts identity text; metadata updates require a valid email address. The configured spelling is preserved until external drift. For import, configure the stored normalized spelling."},
		"email_normalized":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Observed normalized human email, or an empty string when no email is stored. Used to detect external drift while preserving configured input spelling."},
		"active":                           schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether the user is active; defaults to true. Creation initially creates an active user, then deactivates it if false. Deactivation revokes sessions and PATs and completes current incident responsibilities. Reactivation does not restore them. Inactive UUIDs remain readable; refresh/import never mutate the user."},
		"initial_password":                 schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, MarkdownDescription: "Creation-only password, required when creating a user. Untrimmed UTF-8: at least 15 Unicode code points and at most 1024 bytes. Write-only: never stored in Terraform state or plan. May be omitted after creation and for import. Later changes are ignored; Terraform never reads, resets or reconciles the user's password.", Validators: []validator.String{initialUserPasswordRule{}}},
		"initial_password_change_required": schema.BoolAttribute{Optional: true, WriteOnly: true, MarkdownDescription: "Creation-only write-only password policy. Omission or null uses the API default true, requiring a password change at next sign-in. Explicit false keeps the initial password without that requirement. Not stored in state; later changes are ignored."},
	}}
}

type initialUserPasswordRule struct{}

func (initialUserPasswordRule) Description(context.Context) string {
	return "The initial password must be valid untrimmed UTF-8, at least 15 Unicode code points and at most 1024 bytes."
}
func (v initialUserPasswordRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v initialUserPasswordRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !validInitialUserPassword(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid initial password", v.Description(ctx))
	}
}
func validInitialUserPassword(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) >= 15 && len(s) <= 1024
}

func userConfigValid(m userModel) error {
	for _, v := range []types.String{m.DisplayName, m.Login} {
		if v.IsNull() || v.IsUnknown() || !(iamTextRule{}).valid(v.ValueString()) {
			return errors.New("display_name and login must be known nonempty UTF-8 text without NUL or surrounding whitespace")
		}
	}
	if m.Email.IsUnknown() || m.Active.IsUnknown() || m.Active.IsNull() {
		return errors.New("email and active must be known before apply")
	}
	if !m.Email.IsNull() && (!utf8.ValidString(m.Email.ValueString()) || strings.ContainsRune(m.Email.ValueString(), 0)) {
		return errors.New("email must be valid UTF-8 without NUL")
	}
	return nil
}

func (*userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	creating := req.State.Raw.IsNull()
	if !creating {
		var prior, planned types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("login"), &prior)...)
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("login"), &planned)...)
		if resp.Diagnostics.HasError() {
			return
		}
		creating = !planned.IsUnknown() && !prior.Equal(planned)
	}
	if !creating {
		return
	}
	var password types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("initial_password"), &password)...)
	if !resp.Diagnostics.HasError() && password.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("initial_password"), "Initial password required for creation", "Supply initial_password before creating a local user or replacing it with a new login. The password is write-only and is never saved in Terraform state.")
	}
}

// Bind input spelling to the authoritative server value. A changed server
// value is drift; normalization itself is owned entirely by the backend.
func userObservedInput(input, previous types.String, observed string) types.String {
	if !input.IsUnknown() && !previous.IsNull() && !previous.IsUnknown() && previous.ValueString() == observed {
		return input
	}
	if observed == "" {
		return types.StringNull()
	}
	return types.StringValue(observed)
}
func userState(prior userModel, v client.ManagedUser, written bool) userModel {
	// After a committed create whose detail read failed, the local login's
	// normalized spelling is not bound yet. Preserve the known immutable input;
	// importing has no configured login and adopts the direct-read value.
	if prior.Login.IsNull() || prior.Login.IsUnknown() || !prior.LoginNormalized.IsNull() {
		prior.Login = userObservedInput(prior.Login, prior.LoginNormalized, v.LocalLogin)
	}
	if !written {
		prior.Email = userObservedInput(prior.Email, prior.EmailNormalized, v.EmailNormalized)
	}
	prior.ID, prior.DisplayName, prior.Active = types.StringValue(v.ID), types.StringValue(v.DisplayName), types.BoolValue(v.Active)
	prior.LoginNormalized, prior.EmailNormalized = types.StringValue(v.LocalLogin), types.StringValue(v.EmailNormalized)
	prior.InitialPassword, prior.InitialPasswordChangeRequired = types.StringNull(), types.BoolNull()
	return prior
}
func (r *userResource) read(ctx context.Context, id string) (client.ManagedUser, error) {
	v, err := r.client.GetManagedUser(ctx, id)
	if err == nil && (v.LocalLogin == "" || v.Protected) {
		err = errors.New("only local users with a managed local login are supported; external-only and protected bootstrap identities cannot be managed")
	}
	return v, err
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := userConfigValid(plan); err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user", err)
		return
	}
	var password types.String
	var policy types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("initial_password"), &password)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("initial_password_change_required"), &policy)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if password.IsNull() || password.IsUnknown() || !validInitialUserPassword(password.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("initial_password"), "Initial password required for creation", "Supply an initial_password of at least 15 Unicode code points and at most 1024 UTF-8 bytes. It is used only to create this local user and is never saved in Terraform state.")
		return
	}
	if policy.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("initial_password_change_required"), "Unknown initial password policy", "The creation-only password policy must be known before creating the user.")
		return
	}
	in := client.LocalUserInput{DisplayName: plan.DisplayName.ValueString(), Login: plan.Login.ValueString(), Email: plan.Email.ValueString(), Password: password.ValueString()}
	if !policy.IsNull() {
		in.PasswordChangeRequired = new(policy.ValueBool())
	}
	v, err := r.client.CreateLocalUser(ctx, in)
	if v.Committed && validID(v.ID) {
		plan.ID, plan.Active = types.StringValue(v.ID), types.BoolValue(true)
		plan.InitialPassword, plan.InitialPasswordChangeRequired = types.StringNull(), types.BoolNull()
		plan.LoginNormalized, plan.EmailNormalized = types.StringNull(), types.StringNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager user", err)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var desired userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &desired)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !desired.Active.ValueBool() {
		receipt, lifecycleErr := r.client.SetUserActive(ctx, v.ID, false)
		if receipt.Committed || lifecycleErr == nil {
			plan.Active = types.BoolValue(false)
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		}
		if lifecycleErr != nil {
			apiDiagnostic(&resp.Diagnostics, "deactivate newly created OnPager user", lifecycleErr)
			return
		}
		if resp.Diagnostics.HasError() {
			return
		}
	}
	v, err = r.read(ctx, v.ID)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read newly created OnPager user", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userState(plan, v, true))...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.read(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager user", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userState(state, v, false))...)
}

func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := userConfigValid(plan); err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user", err)
		return
	}
	state.InitialPassword, state.InitialPasswordChangeRequired = types.StringNull(), types.BoolNull()
	patch := client.LocalUserPatch{}
	if !state.DisplayName.Equal(plan.DisplayName) {
		patch.DisplayName = new(plan.DisplayName.ValueString())
	}
	if !state.Email.Equal(plan.Email) {
		patch.EmailSet = true
		if !plan.Email.IsNull() && strings.TrimSpace(plan.Email.ValueString()) != "" {
			patch.Email = new(plan.Email.ValueString())
		}
	}
	if patch.DisplayName != nil || patch.EmailSet {
		receipt, err := r.client.UpdateLocalUser(ctx, state.ID.ValueString(), patch)
		if receipt.Committed {
			state.DisplayName, state.Email = plan.DisplayName, plan.Email
			if patch.EmailSet {
				state.EmailNormalized = types.StringNull()
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		}
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "update OnPager user metadata", err)
			return
		}
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !state.Active.Equal(plan.Active) {
		receipt, err := r.client.SetUserActive(ctx, state.ID.ValueString(), plan.Active.ValueBool())
		if receipt.Committed || err == nil {
			state.Active = plan.Active
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		}
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "change OnPager user active state", err)
			return
		}
		if resp.Diagnostics.HasError() {
			return
		}
	}
	v, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read updated OnPager user", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userState(state, v, patch.EmailSet))...)
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.read(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager user before deactivation", err)
		return
	}
	if !v.Active {
		return
	}
	if err = r.client.DeactivateUser(ctx, v.ID); err != nil {
		apiDiagnostic(&resp.Diagnostics, "deactivate OnPager user", err)
	}
}
func (*userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
