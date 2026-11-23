package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type userPhoneContactResource struct{ configuredResource }

type userPhoneContactModel struct {
	ID                types.String `tfsdk:"id"`
	UserID            types.String `tfsdk:"user_id"`
	ChannelID         types.String `tfsdk:"channel_id"`
	RequestID         types.String `tfsdk:"request_id"`
	TemplateContactID types.String `tfsdk:"template_contact_id"`
	Phone             types.String `tfsdk:"phone"`
	DisplayName       types.String `tfsdk:"display_name"`
	Verified          types.Bool   `tfsdk:"verified"`
	TargetID          types.String `tfsdk:"target_id"`
	MaskedPhone       types.String `tfsdk:"masked_phone"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Draft             types.Bool   `tfsdk:"draft"`
	Generation        types.Int64  `tfsdk:"generation"`
}

func NewUserPhoneContactResource() resource.Resource { return &userPhoneContactResource{} }

func (*userPhoneContactResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_phone_contact"
}

func (*userPhoneContactResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	user := uuidAttribute("Owner user UUID. Changing the user replaces the method.")
	channel := uuidAttribute("Voice channel UUID accessible to the owner during writes. Changing the channel replaces the method.")
	user.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	channel.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	id := idAttribute()
	id.MarkdownDescription = "Stable phone method UUID. Import with `user_uuid/method_uuid`."
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one employee phone contact using the tenant permission `user_phone_contacts.manage`. The method can be read and deleted after the employee loses access to the voice channel. Destroy deletes the exact method and clears its references in retained notification policy steps. Administrative confirmation places no call.", Attributes: map[string]schema.Attribute{
		"id": id, "user_id": user, "channel_id": channel,
		"request_id":          contactRequestIDAttribute(),
		"template_contact_id": contactTemplateSlotAttribute(),
		"phone":               schema.StringAttribute{Optional: true, Computed: true, Sensitive: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "E.164 phone number with a plus sign and 8–15 digits. Required on create and replacement. Terraform stores the full configured number in sensitive state. OnPager returns only its masked suffix and target UUID. Omit or set null to keep the current binding after import or on update. Refresh clears the stored number when the target UUID changes. A configured number is restored on the next apply."},
		"display_name":        schema.StringAttribute{Required: true, MarkdownDescription: "Mutable contact label of 1–200 UTF-8 characters without NUL or surrounding whitespace."},
		"verified":            schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Set true to confirm the exact configured phone on its voice channel. Set false to revoke confirmation. Omission or null leaves verification under OnPager control. New and replaced numbers default to unverified. A rename preserves confirmation. Terraform records the observed status on read."},
		"target_id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Opaque UUID of the current destination. A number change can change this UUID even when the masked suffix is identical."},
		"masked_phone":        schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Masked suffix returned by OnPager. Empty for an unfilled draft."},
		"enabled":             schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether OnPager currently enables the method."},
		"draft":               schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the contact is an unfilled draft."},
		"generation":          schema.Int64Attribute{Computed: true, MarkdownDescription: "Observed verification generation. Phone updates and deletion use the scoped method UUID without a revision precondition."},
	}}
}

func (*userPhoneContactResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var conf userPhoneContactModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &conf)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !conf.Phone.IsUnknown() {
		if err := (client.UserPhoneContactInput{Phone: conf.Phone.ValueStringPointer()}).Validate(); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("phone"), "Invalid phone number", err.Error())
		}
	}
	if !conf.DisplayName.IsUnknown() {
		if err := (client.UserPhoneContactInput{DisplayName: conf.DisplayName.ValueStringPointer()}).Validate(); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("display_name"), "Invalid phone contact label", err.Error())
		}
	}
	if conf.Verified.ValueBool() && conf.Phone.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("phone"), "Phone required for confirmation", "Supply the exact phone number when setting verified to true.")
	}
}

func (*userPhoneContactResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	creating := planUserContactCreate(ctx, req, resp, "channel_id")
	if req.Plan.Raw.IsNull() || resp.Diagnostics.HasError() {
		return
	}
	var configured types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("phone"), &configured)...)
	if configured.IsNull() && (creating || len(resp.RequiresReplace) != 0) {
		resp.Diagnostics.AddAttributeError(path.Root("phone"), "Phone required for creation", "Supply the full phone number when creating or replacing an employee phone contact.")
	}
}

func (p userPhoneContactModel) desired(ctx context.Context, config tfsdk.Config, create bool) (client.UserPhoneContactInput, error) {
	var conf userPhoneContactModel
	var in client.UserPhoneContactInput
	if config.Get(ctx, &conf).HasError() {
		return in, errors.New("could not read the configured user phone contact")
	}
	if !validID(p.UserID.ValueString()) || !validID(p.ChannelID.ValueString()) || p.DisplayName.IsNull() || p.DisplayName.IsUnknown() {
		return in, errors.New("user_id, channel_id and display_name must resolve before apply")
	}
	in.ChannelID, in.DisplayName = p.ChannelID.ValueStringPointer(), p.DisplayName.ValueStringPointer()
	if !conf.Phone.IsNull() {
		// An imported number is null. ignore_changes may retain that null
		// in the plan even with a number in the configuration.
		if p.Phone.IsUnknown() || create && p.Phone.IsNull() {
			return in, errors.New("configured phone must resolve before apply")
		}
		in.Phone = p.Phone.ValueStringPointer()
	}
	if !conf.Verified.IsNull() {
		if p.Verified.IsUnknown() || p.Verified.IsNull() {
			return in, errors.New("configured verified must resolve before apply")
		}
		in.Verified = p.Verified.ValueBoolPointer()
	}
	if create {
		if conf.RequestID.IsNull() || !validID(p.RequestID.ValueString()) || in.Phone == nil {
			return in, errors.New("supply a known request_id and phone when creating a phone contact")
		}
		in.RequestID = p.RequestID.ValueStringPointer()
		if !conf.TemplateContactID.IsNull() {
			if !validID(p.TemplateContactID.ValueString()) {
				return in, errors.New("configured template_contact_id must resolve before apply")
			}
			in.TemplateContactID = p.TemplateContactID.ValueStringPointer()
		}
	}
	return in, in.Validate()
}

// The saved number and target_id form one binding. Only an authoritative
// number write can associate a previously unknown target with cleartext.
func (m userPhoneContactModel) observed(v client.UserPhoneContact, writtenPhone *string) userPhoneContactModel {
	target := types.StringValue(v.TargetID)
	if writtenPhone != nil {
		m.Phone = types.StringPointerValue(writtenPhone)
	} else if !m.TargetID.Equal(target) || v.Draft {
		m.Phone = types.StringNull()
	}
	m.ID, m.UserID, m.ChannelID = types.StringValue(v.ID), types.StringValue(v.UserID), types.StringValue(v.ChannelID)
	m.TargetID, m.MaskedPhone = target, types.StringValue(v.MaskedPhone)
	m.DisplayName, m.Verified = types.StringValue(v.DisplayName), types.BoolValue(v.Verified)
	m.Enabled, m.Draft, m.Generation = types.BoolValue(v.Enabled), types.BoolValue(v.Draft), types.Int64Value(v.Generation)
	return m
}

// If a previous write established a binding, the same target UUID cannot
// identify a different address, even one with the same masked suffix.
func (m userPhoneContactModel) checkBinding(v client.UserPhoneContact, writtenPhone *string) error {
	if !m.ChannelID.IsNull() && m.ChannelID.ValueString() != v.ChannelID {
		return errors.New("OnPager returned a different immutable channel for the phone contact")
	}
	if m.TargetID.ValueString() == v.TargetID && !m.Phone.IsNull() {
		if v.Draft || !strings.HasSuffix(m.Phone.ValueString(), strings.TrimPrefix(v.MaskedPhone, "+…")) ||
			writtenPhone != nil && *writtenPhone != m.Phone.ValueString() {
			return errors.New("OnPager returned conflicting addresses for the same phone target UUID")
		}
	}
	return nil
}

func (r *userPhoneContactResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p userPhoneContactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := p.desired(ctx, req.Config, true)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user phone contact", err)
		return
	}
	v, err := r.client.CreateUserPhoneContact(ctx, p.UserID.ValueString(), in)
	state := userPhoneContactModel{UserID: p.UserID, ChannelID: p.ChannelID, RequestID: p.RequestID, TemplateContactID: types.StringPointerValue(in.TemplateContactID)}
	if err != nil {
		if v.Committed && validID(v.ID) {
			state.ID = types.StringValue(v.ID)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		}
		contactCreateDiagnostic(&resp.Diagnostics, "create OnPager user phone contact", err)
		return
	}
	if v.Created {
		resp.Diagnostics.Append(resp.State.Set(ctx, state.observed(v, in.Phone))...)
		return
	}
	// A 200 response may reflect a different live address. Persist the owned
	// identity first. PATCH also handles a freshly adopted draft idempotently.
	state = state.observed(v, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if !resp.Diagnostics.HasError() {
		r.reconcile(ctx, state, in, &resp.State, &resp.Diagnostics)
	}
}

func (r *userPhoneContactResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var old userPhoneContactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetUserPhoneContact(ctx, old.UserID.ValueString(), old.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		err = old.checkBinding(v, nil)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager user phone contact", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, old.observed(v, nil))...)
}

func (r *userPhoneContactResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, old userPhoneContactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !old.UserID.Equal(p.UserID) || !old.ChannelID.Equal(p.ChannelID) || !validID(old.ID.ValueString()) ||
		!p.ID.IsUnknown() && !p.ID.Equal(old.ID) || !p.RequestID.IsUnknown() && !p.RequestID.Equal(old.RequestID) ||
		!p.TemplateContactID.IsUnknown() && !p.TemplateContactID.Equal(old.TemplateContactID) {
		resp.Diagnostics.AddError("Phone contact requires replacement", "An in-place update requires the original user, method, channel, request key and template slot.")
		return
	}
	in, err := p.desired(ctx, req.Config, false)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user phone contact", err)
		return
	}
	r.reconcile(ctx, old, in, &resp.State, &resp.Diagnostics)
}

func (r *userPhoneContactResource) reconcile(ctx context.Context, old userPhoneContactModel, desired client.UserPhoneContactInput, destination *tfsdk.State, diags *diag.Diagnostics) {
	var patch client.UserPhoneContactInput
	if !types.StringPointerValue(desired.DisplayName).Equal(old.DisplayName) {
		patch.DisplayName = desired.DisplayName
	}
	if desired.Phone != nil && !types.StringPointerValue(desired.Phone).Equal(old.Phone) {
		patch.Phone = desired.Phone
	}
	if desired.Verified != nil && (patch.Phone != nil || !types.BoolPointerValue(desired.Verified).Equal(old.Verified)) {
		patch.Verified = desired.Verified
		if *desired.Verified {
			patch.Phone, patch.ChannelID = desired.Phone, desired.ChannelID
		}
	}
	if patch == (client.UserPhoneContactInput{}) {
		diags.Append(destination.Set(ctx, old)...)
		return
	}
	v, err := r.client.UpdateUserPhoneContact(ctx, old.UserID.ValueString(), old.ID.ValueString(), patch)
	if err == nil {
		err = old.checkBinding(v, patch.Phone)
	}
	if err != nil {
		if v.Committed {
			if patch.DisplayName != nil {
				old.DisplayName = types.StringPointerValue(patch.DisplayName)
			}
			if patch.Phone != nil {
				// A malformed receipt cannot establish the target_id for the
				// requested phone, even if the masked suffix is unchanged.
				old.TargetID, old.Phone, old.MaskedPhone = types.StringNull(), types.StringNull(), types.StringNull()
			}
			diags.Append(destination.Set(ctx, old)...)
		}
		apiDiagnostic(diags, "update OnPager user phone contact", err)
		return
	}
	next := old.observed(v, patch.Phone)
	diags.Append(destination.Set(ctx, next)...)
	// A metadata-only plan retains a known phone. Record any concurrent
	// target change and ask Terraform to plan again with the observed state.
	lostKnownPhone := patch.Phone == nil && !old.Phone.IsNull() && next.Phone.IsNull()
	if lostKnownPhone || !next.DisplayName.Equal(types.StringPointerValue(desired.DisplayName)) ||
		desired.Phone != nil && !next.Phone.Equal(types.StringPointerValue(desired.Phone)) ||
		desired.Verified != nil && !next.Verified.Equal(types.BoolPointerValue(desired.Verified)) {
		diags.AddError("Phone contact changed during update", "The phone contact response differs from the configured binding or metadata. Review the refreshed plan before applying again.")
	}
}

func (r *userPhoneContactResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var old userPhoneContactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteUserPhoneContact(ctx, old.UserID.ValueString(), old.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager user phone contact", err)
	}
}

func (*userPhoneContactResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importUserContact(ctx, req, resp)
}
