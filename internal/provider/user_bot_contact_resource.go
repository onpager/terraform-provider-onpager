package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type userBotContactResource struct{ configuredResource }
type userBotContactModel struct {
	ID          types.String `tfsdk:"id"`
	UserID      types.String `tfsdk:"user_id"`
	BotID       types.String `tfsdk:"bot_id"`
	DisplayName types.String `tfsdk:"display_name"`
	RecipientID types.String `tfsdk:"recipient_id"`
	Verified    types.Bool   `tfsdk:"verified"`
}

func NewUserBotContactResource() resource.Resource { return &userBotContactResource{} }
func (*userBotContactResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_bot_contact"
}
func (*userBotContactResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	user := uuidAttribute("Owner user UUID. Changing the owner replaces the contact.")
	user.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	id := idAttribute()
	id.MarkdownDescription = "Stable delivery method UUID. Import with the composite ID `user_uuid/method_uuid`."
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one user's bot contact with the tenant permission `user_bot_contacts.manage`. Declaring the contact adopts the existing method for that user and bot, including a method created by a notification template. Destroy deletes the method and clears its references in retained policy steps. Verification records an administrator's assertion without looking up the recipient or sending a message.", Attributes: map[string]schema.Attribute{
		"id":           id,
		"user_id":      user,
		"bot_id":       uuidAttribute("Bot UUID. The user must have access to the chosen bot when creating a binding or switching bots. An update preserves the method UUID. Switching bots clears the previous recipient and verification before applying any explicitly configured replacements."),
		"display_name": schema.StringAttribute{Required: true, MarkdownDescription: "Mutable contact label of 1–200 UTF-8 bytes, without NUL or surrounding whitespace."},
		"recipient_id": schema.StringAttribute{Optional: true, Computed: true, Sensitive: true, MarkdownDescription: "Exact messenger recipient ID, 1–1024 UTF-8 bytes without whitespace or control characters. Omit or set null to use the current recipient on the same bot. A new draft has no recipient by default. Removing this argument preserves the saved address. The API returns the address on read and import, and Terraform stores it in state."},
		"verified":     schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Set true to confirm the supplied or already saved recipient. Set false to revoke confirmation. Omission or null preserves verification on the same bot and recipient. A new contact or changed binding defaults to unverified. The current verification status is read into state."},
	}}
}

func (*userBotContactResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m userBotContactModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.DisplayName.IsUnknown() {
		if err := (client.UserBotContactInput{DisplayName: m.DisplayName.ValueStringPointer()}).Validate(); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("display_name"), "Invalid contact display name", err.Error())
		}
	}
	if !m.RecipientID.IsUnknown() {
		if err := (client.UserBotContactInput{RecipientID: m.RecipientID.ValueStringPointer()}).Validate(); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("recipient_id"), "Invalid contact recipient", err.Error())
		}
	}
}

// Config distinguishes omitted arguments from computed state. For explicit
// arguments, use the planned value so lifecycle.ignore_changes is respected.
func (p userBotContactModel) desired(ctx context.Context, config tfsdk.Config) (client.UserBotContactInput, error) {
	var m userBotContactModel
	if d := config.Get(ctx, &m); d.HasError() {
		return client.UserBotContactInput{}, errors.New("could not read the configured user bot contact")
	}
	if !validID(p.UserID.ValueString()) || !validID(p.BotID.ValueString()) || p.DisplayName.IsUnknown() || p.DisplayName.IsNull() {
		return client.UserBotContactInput{}, errors.New("user_id, bot_id and display_name must be known before apply")
	}
	in := client.UserBotContactInput{BotID: p.BotID.ValueStringPointer(), DisplayName: p.DisplayName.ValueStringPointer()}
	if !m.RecipientID.IsNull() {
		if p.RecipientID.IsUnknown() {
			return client.UserBotContactInput{}, errors.New("recipient_id must resolve before apply")
		}
		in.RecipientID = p.RecipientID.ValueStringPointer()
	}
	if !m.Verified.IsNull() {
		if p.Verified.IsUnknown() {
			return client.UserBotContactInput{}, errors.New("verified must resolve before apply")
		}
		in.Verified = p.Verified.ValueBoolPointer()
	}
	return in, in.Validate()
}

func userBotContactState(user types.String, v client.UserBotContact) userBotContactModel {
	m := userBotContactModel{ID: types.StringValue(v.ID), UserID: user, BotID: types.StringValue(v.BotID), DisplayName: types.StringValue(v.DisplayName), Verified: types.BoolValue(v.Verified)}
	if v.RecipientID != "" {
		m.RecipientID = types.StringValue(v.RecipientID)
	}
	return m
}

func (r *userBotContactResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p userBotContactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := p.desired(ctx, req.Config)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user bot contact", err)
		return
	}
	v, err := r.client.UpsertUserBotContact(ctx, p.UserID.ValueString(), in)
	if err != nil {
		if v.Committed && validID(v.ID) {
			p.ID = types.StringValue(v.ID)
			p.RecipientID = types.StringPointerValue(in.RecipientID)
			p.Verified = types.BoolPointerValue(in.Verified)
			if v.Created && in.Verified == nil {
				p.Verified = types.BoolValue(false)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, p)...)
		}
		apiDiagnostic(&resp.Diagnostics, "declare OnPager user bot contact", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userBotContactState(p.UserID, v))...)
}

func (r *userBotContactResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var old userBotContactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetUserBotContact(ctx, old.UserID.ValueString(), old.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager user bot contact", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userBotContactState(old.UserID, v))...)
}

func (old userBotContactModel) patch(in client.UserBotContactInput) (client.UserBotContactInput, userBotContactModel) {
	var patch client.UserBotContactInput
	next := old
	botChanged := *in.BotID != old.BotID.ValueString()
	if botChanged {
		patch.BotID = in.BotID
		next.BotID = types.StringPointerValue(in.BotID)
		next.RecipientID = types.StringNull()
		next.Verified = types.BoolValue(false)
	}
	if *in.DisplayName != old.DisplayName.ValueString() {
		patch.DisplayName = in.DisplayName
		next.DisplayName = types.StringPointerValue(in.DisplayName)
	}
	recipientChanged := in.RecipientID != nil && *in.RecipientID != next.RecipientID.ValueString()
	if recipientChanged {
		patch.RecipientID = in.RecipientID
		next.RecipientID = types.StringPointerValue(in.RecipientID)
		next.Verified = types.BoolValue(false)
	}
	if in.Verified != nil && (botChanged || recipientChanged || !types.BoolPointerValue(in.Verified).Equal(old.Verified)) {
		patch.Verified = in.Verified
		next.Verified = types.BoolPointerValue(in.Verified)
		// Bind an explicit confirmation to the configured bot and recipient,
		// even if another writer changed the binding after the last refresh.
		if *in.Verified {
			patch.BotID = in.BotID
			patch.RecipientID = in.RecipientID
		}
	}
	return patch, next
}

func (r *userBotContactResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, old userBotContactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !p.UserID.Equal(old.UserID) || !validID(old.ID.ValueString()) || !p.ID.IsUnknown() && !p.ID.Equal(old.ID) {
		resp.Diagnostics.AddError("Invalid user bot contact identity", "An in-place update requires the original user and method UUIDs.")
		return
	}
	in, err := p.desired(ctx, req.Config)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user bot contact", err)
		return
	}
	patch, next := old.patch(in)
	if patch == (client.UserBotContactInput{}) {
		resp.Diagnostics.Append(resp.State.Set(ctx, old)...)
		return
	}
	v, err := r.client.UpdateUserBotContact(ctx, old.UserID.ValueString(), old.ID.ValueString(), patch)
	if err == nil && !in.Matches(v) {
		err = errors.New("OnPager returned a contact that differs from the configured binding or metadata")
	}
	if err != nil {
		if v.Committed {
			resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
		}
		apiDiagnostic(&resp.Diagnostics, "update OnPager user bot contact", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userBotContactState(old.UserID, v))...)
}

func (r *userBotContactResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var old userBotContactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteUserBotContact(ctx, old.UserID.ValueString(), old.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager user bot contact", err)
	}
}
func (*userBotContactResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	user, method, ok := splitImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid user bot contact import ID", "Use user_uuid/method_uuid with two nonzero canonical lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userBotContactModel{UserID: types.StringValue(user), ID: types.StringValue(method)})...)
}
