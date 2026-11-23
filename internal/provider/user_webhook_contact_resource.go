package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type userWebhookContactResource struct{ configuredResource }
type userWebhookContactModel struct {
	ID                types.String `tfsdk:"id"`
	UserID            types.String `tfsdk:"user_id"`
	WebhookID         types.String `tfsdk:"webhook_id"`
	RequestID         types.String `tfsdk:"request_id"`
	TemplateContactID types.String `tfsdk:"template_contact_id"`
	DisplayName       types.String `tfsdk:"display_name"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Verified          types.Bool   `tfsdk:"verified"`
	Draft             types.Bool   `tfsdk:"draft"`
	Generation        types.Int64  `tfsdk:"generation"`
	ETag              types.String `tfsdk:"etag"`
	Values            types.Map    `tfsdk:"values"`
}

func NewUserWebhookContactResource() resource.Resource { return &userWebhookContactResource{} }

func (*userWebhookContactResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_webhook_contact"
}

func (*userWebhookContactResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	user := uuidAttribute("Owner user UUID. Changing it replaces the method.")
	hook := uuidAttribute("Accessible notification webhook definition UUID. Changing it replaces the method.")
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	user.PlanModifiers, hook.PlanModifiers = replace, replace
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage one user's notification webhook contact with the tenant permission `user_webhook_contacts.manage`. A new request UUID creates a distinct method. An exact template slot can complete a draft in place. Control verifies the recipient on completion without sending a request to the webhook. Destroy deletes the method and clears its references in retained policy steps. Import uses `user_uuid/method_uuid`.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute(), "user_id": user, "webhook_id": hook,
			"request_id": contactRequestIDAttribute(), "template_contact_id": contactTemplateSlotAttribute(),
			"display_name": schema.StringAttribute{Required: true, MarkdownDescription: "Mutable contact label of 1–200 UTF-8 characters without NUL or surrounding whitespace."},
			"values":       schema.MapAttribute{Optional: true, Computed: true, Sensitive: true, ElementType: types.StringType, PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Complete recipient field map for the webhook definition. Supply it on creation, including `{}` for a definition without required fields. On update, omission or null preserves the saved fields. An explicit map replaces them all and completes a draft, including an empty map when allowed by the definition. Keys and field types must match the definition. Up to 20 fields, 1,024 UTF-8 bytes per value and 16 KiB combined. Privileged reads and imports return saved values, which persist in Terraform state. Drafts have null values."},
			"enabled":      schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Whether the method is enabled. A new method starts enabled. An explicit false applies a guarded update after creation. Omit or set null to preserve the current setting on import, replay and subsequent updates."},
			"verified":     schema.BoolAttribute{Computed: true, MarkdownDescription: "Current verification status. Completing a webhook contact confirms it immediately."},
			"draft":        schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this method is an uncompleted webhook draft."},
			"generation":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Observed method verification generation, including zero. The provider sends the saved generation when completing a draft with recipient values."},
			"etag":         schema.StringAttribute{Computed: true, MarkdownDescription: "The method's strong numeric ETag, sent as If-Match for PATCH and DELETE. A stale tag produces a conflict."},
		},
	}
}

func (*userWebhookContactResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	create := planUserContactCreate(ctx, req, resp, "webhook_id")
	if req.Plan.Raw.IsNull() || !create && len(resp.RequiresReplace) == 0 {
		return
	}
	var values types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("values"), &values)...)
	if values.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("values"), "Webhook recipient values required", "Supply the complete values map for creation or replacement. Use {} when the definition has no required recipient fields.")
	}
}

func webhookRecipientValues(v types.Map) (map[string]string, error) {
	if v.IsUnknown() || v.IsNull() {
		return nil, errors.New("the configured values map must resolve before apply")
	}
	m := make(map[string]string, len(v.Elements()))
	for k, elem := range v.Elements() {
		s, ok := elem.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			return nil, errors.New("each recipient field must be a known, non-null string")
		}
		m[k] = s.ValueString()
	}
	return m, client.ValidateWebhookContactValues(m)
}

func (*userWebhookContactResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m userWebhookContactModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.DisplayName.IsNull() && !m.DisplayName.IsUnknown() {
		if err := client.ValidateManagedContactName(m.DisplayName.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("display_name"), "Invalid contact label", err.Error())
		}
	}
	if !m.Values.IsNull() && !outgoingHasUnknown(m.Values) {
		if _, err := webhookRecipientValues(m.Values); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("values"), "Invalid webhook recipient values", err.Error())
		}
	}
}

// Keep configured omission of optional fields when Control expands them into
// empty strings. Import reads the full stored map. A draft has no recipient yet,
// so an explicit {} can complete it even with no nonempty recipient fields.
func (m userWebhookContactModel) withReceipt(v client.UserWebhookContact) userWebhookContactModel {
	m.ID, m.UserID, m.WebhookID = types.StringValue(v.ID), types.StringValue(v.UserID), types.StringValue(v.WebhookID)
	m.DisplayName, m.ETag = types.StringValue(v.DisplayName), types.StringValue(v.ETag)
	m.Enabled, m.Draft, m.Verified = types.BoolValue(v.Enabled), types.BoolValue(v.Draft), types.BoolValue(v.Verified)
	m.Generation = types.Int64Value(v.Generation)
	if v.Draft {
		m.Values = types.MapNull(types.StringType)
		return m
	}
	remembered := m.Values.Elements()
	all := m.Values.IsNull() || m.Values.IsUnknown()
	fields := make(map[string]attr.Value, len(v.Values))
	for k, value := range v.Values {
		if _, known := remembered[k]; all || known || value != "" {
			fields[k] = types.StringValue(value)
		}
	}
	m.Values = types.MapValueMust(types.StringType, fields)
	return m
}

// Config selects which optional fields are managed; the plan supplies their
// actual values, including the result of lifecycle.ignore_changes.
func (m userWebhookContactModel) desired(config userWebhookContactModel) (client.UserWebhookContactPatch, error) {
	want := client.UserWebhookContactPatch{DisplayName: m.DisplayName.ValueStringPointer()}
	if err := client.ValidateManagedContactName(m.DisplayName.ValueString()); err != nil {
		return want, err
	}
	if !config.Values.IsNull() && !m.Values.IsNull() {
		fields, err := webhookRecipientValues(m.Values)
		if err != nil {
			return want, err
		}
		want.Values = &fields
	}
	if !config.Enabled.IsNull() {
		if m.Enabled.IsNull() || m.Enabled.IsUnknown() {
			return want, errors.New("the configured enabled flag must resolve before apply")
		}
		want.Enabled = m.Enabled.ValueBoolPointer()
	}
	return want, nil
}

// Apply only differences against a complete saved receipt, including a live
// receipt returned by an idempotent POST replay.
func (r *userWebhookContactResource) reconcile(ctx context.Context, old userWebhookContactModel, want client.UserWebhookContactPatch, values types.Map) (userWebhookContactModel, error) {
	next := old
	var patch client.UserWebhookContactPatch
	if *want.DisplayName != old.DisplayName.ValueString() {
		patch.DisplayName = want.DisplayName
		next.DisplayName = types.StringPointerValue(want.DisplayName)
	}
	if want.Enabled != nil && !types.BoolPointerValue(want.Enabled).Equal(old.Enabled) {
		patch.Enabled = want.Enabled
		next.Enabled = types.BoolPointerValue(want.Enabled)
	}
	if want.Values != nil && (!values.Equal(old.Values) || old.Draft.ValueBool()) {
		patch.Values = want.Values
		next.Values = values
		if old.Draft.IsNull() || old.Draft.IsUnknown() {
			return old, errors.New("refresh the webhook contact to determine its draft status before setting values")
		}
		if old.Draft.ValueBool() {
			if old.Generation.IsNull() || old.Generation.IsUnknown() {
				return old, errors.New("refresh the webhook draft to obtain its generation before completing it")
			}
			patch.ExpectedGeneration = old.Generation.ValueInt64Pointer()
		}
	}
	if patch == (client.UserWebhookContactPatch{}) {
		return old, nil
	}
	v, err := r.client.UpdateUserWebhookContact(ctx, old.UserID.ValueString(), old.ID.ValueString(), old.WebhookID.ValueString(), old.ETag.ValueString(), patch)
	if err != nil {
		if !v.Committed {
			return old, err
		}
		// Preserve known intent. A subsequent refresh recovers a trusted ETag
		// and generation after a successful status with a malformed receipt.
		next.ETag, next.Generation = types.StringNull(), types.Int64Null()
		next.Draft, next.Verified = types.BoolNull(), types.BoolNull()
		return next, err
	}
	return next.withReceipt(v), nil
}

func (r *userWebhookContactResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p, config userWebhookContactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	want, err := p.desired(config)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user webhook contact", err)
		return
	}
	if config.RequestID.IsNull() || !validID(p.RequestID.ValueString()) || want.Values == nil {
		resp.Diagnostics.AddError("Missing webhook contact create arguments", "Supply a known request_id UUID and a complete values map, including {} when appropriate.")
		return
	}
	if config.TemplateContactID.IsNull() {
		p.TemplateContactID = types.StringNull()
	} else if !validID(p.TemplateContactID.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("template_contact_id"), "Invalid template contact", "The configured slot UUID must resolve before creation.")
		return
	}
	in := client.UserWebhookContactCreate{RequestID: p.RequestID.ValueString(), TemplateContactID: p.TemplateContactID.ValueStringPointer(), WebhookID: p.WebhookID.ValueString(), DisplayName: *want.DisplayName, Values: *want.Values}
	v, err := r.client.CreateUserWebhookContact(ctx, p.UserID.ValueString(), in)
	if err != nil {
		if v.Committed && validID(v.ID) {
			p.ID = types.StringValue(v.ID)
			p.Enabled, p.Draft, p.Verified = types.BoolNull(), types.BoolNull(), types.BoolNull()
			p.ETag, p.Generation = types.StringNull(), types.Int64Null()
			resp.Diagnostics.Append(resp.State.Set(ctx, p)...)
		}
		contactCreateDiagnostic(&resp.Diagnostics, "create OnPager user webhook contact", err)
		return
	}
	created := p.withReceipt(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, created)...)
	if resp.Diagnostics.HasError() {
		return
	}
	next, err := r.reconcile(ctx, created, want, p.Values)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "reconcile created OnPager user webhook contact", err)
	}
}

func (r *userWebhookContactResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m userWebhookContactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetUserWebhookContact(ctx, m.UserID.ValueString(), m.ID.ValueString(), m.WebhookID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager user webhook contact", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m.withReceipt(v))...)
}

func (r *userWebhookContactResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var old, p, config userWebhookContactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !old.UserID.Equal(p.UserID) || !old.WebhookID.Equal(p.WebhookID) || !validID(old.ID.ValueString()) || !p.ID.IsUnknown() && !old.ID.Equal(p.ID) ||
		!config.RequestID.IsNull() && !p.RequestID.Equal(old.RequestID) || !config.TemplateContactID.IsNull() && !p.TemplateContactID.Equal(old.TemplateContactID) {
		resp.Diagnostics.AddError("Webhook contact identity changed", "An in-place update requires the saved method, owner and webhook UUIDs. Changing the create request UUID or template slot requires replacement.")
		return
	}
	want, err := p.desired(config)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager user webhook contact", err)
		return
	}
	next, err := r.reconcile(ctx, old, want, p.Values)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager user webhook contact", err)
	}
}

func (r *userWebhookContactResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m userWebhookContactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteUserWebhookContact(ctx, m.UserID.ValueString(), m.ID.ValueString(), m.ETag.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager user webhook contact", err)
	}
}

func (*userWebhookContactResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importUserContact(ctx, req, resp)
}
