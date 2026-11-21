package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type userNotificationDefaultsResource struct{ configuredResource }

func NewUserNotificationDefaultsResource() resource.Resource {
	return &userNotificationDefaultsResource{}
}

func (*userNotificationDefaultsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_notification_defaults"
}

func notificationDefaultPolicySchema(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Required: true, MarkdownDescription: description,
		Attributes: map[string]schema.Attribute{
			"configured": schema.BoolAttribute{Required: true, MarkdownDescription: "Whether eligible users inherit this policy. Set to false with an empty step list to skip this policy. Set to true with an empty step list to publish an explicitly empty policy."},
			"steps": schema.ListNestedAttribute{Required: true, MarkdownDescription: "Complete ordered list of notification and wait steps. Use an empty list when configured is false.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"kind":         schema.StringAttribute{Required: true, MarkdownDescription: "Step kind, either `notify` or `wait`.", Validators: []validator.String{enumRule{"notify", "wait"}}},
				"contact_id":   schema.StringAttribute{Optional: true, MarkdownDescription: "UUID of a contact slot in this template. Required for notify. Omit or set to null for wait.", Validators: []validator.String{stringRule{uuid: true}}},
				"wait_seconds": schema.Int64Attribute{Optional: true, MarkdownDescription: "Wait duration in whole seconds, from 1 through 9223372036. Required for wait. Omit or set to null for notify."},
			}}},
		},
	}
}

func (*userNotificationDefaultsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage the tenant template for personal contacts and notification policies. OnPager applies eligible sections when creating a user or granting individual access, before the API returns.", Attributes: map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Singleton ID and import identifier, always `tenant`. Configure one resource per tenant.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Server-assigned template revision, initially zero. Each committed replacement advances this counter. Concurrent saves take effect in commit order."},
		"enabled":  schema.BoolAttribute{Required: true, MarkdownDescription: "Whether OnPager applies the template to eligible users. Enabling a populated template also schedules updates to inherited sections for existing users. Disabling it preserves the template content and existing personal settings."},
		"contacts": schema.ListNestedAttribute{Required: true, MarkdownDescription: "Complete list of contact slots. Use an empty list to configure no contacts. Each slot and delivery channel may appear once.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"id":                   uuidAttribute("Stable slot UUID chosen for this template. Policy notify steps reference this ID."),
			"channel_id":           uuidAttribute("Known enabled, globally available delivery-channel UUID. A webhook's channel UUID is distinct from its webhook UUID."),
			"display_name":         schema.StringAttribute{Required: true, MarkdownDescription: "Contact label from 1 through 200 Unicode characters, without surrounding whitespace.", Validators: []validator.String{stringRule{}}},
			"require_verification": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether enrollment requires verification. Omission or null defaults to true. False requires a channel supporting automatic verification. Telegram and Discord require true in the template. An authorized administrator can separately confirm a known bot recipient."},
			"lookup_attribute":     schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Identity attribute used to resolve the recipient, such as `email` or `preferred_username`, when supported by the channel. Omission, null and an empty string select no lookup attribute. Channels offering lookup attributes require an explicit supported value."},
		}}},
		"policies": schema.SingleNestedAttribute{Required: true, MarkdownDescription: "Both complete personal notification policy templates. Each policy is required and may be unconfigured or explicitly empty.", Attributes: map[string]schema.Attribute{
			"default":   notificationDefaultPolicySchema("Template for default-importance notifications."),
			"important": notificationDefaultPolicySchema("Template for important notifications."),
		}},
	}}
}

func (*userNotificationDefaultsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if req.Config.Raw.IsFullyKnown() {
		notificationDefaultsInput(ctx, req.Config, &resp.Diagnostics)
	}
}

func (r *userNotificationDefaultsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	in := notificationDefaultsInput(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.client.GetUserNotificationDefaults(ctx)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read unowned OnPager user notification defaults", err)
		return
	}
	if !current.IsEmpty() {
		resp.Diagnostics.AddError("Existing user notification defaults require import", "The tenant already has an enabled template, saved contacts or a configured policy. Import the resource with ID tenant before applying, including for a disabled template with content or an explicitly empty configured policy.")
		return
	}
	if in.IsEmpty() {
		resp.Diagnostics.Append(resp.State.Set(ctx, notificationDefaultsState(current.UserNotificationDefaultsInput, types.Int64Value(current.Revision)))...)
		return
	}
	r.replace(ctx, in, &resp.State, &resp.Diagnostics)
}

func (r *userNotificationDefaultsResource) Read(ctx context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	v, err := r.client.GetUserNotificationDefaults(ctx)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager user notification defaults", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, notificationDefaultsState(v.UserNotificationDefaultsInput, types.Int64Value(v.Revision)))...)
}

func (r *userNotificationDefaultsResource) replace(ctx context.Context, in client.UserNotificationDefaultsInput, state *tfsdk.State, d *diag.Diagnostics) {
	v, err := r.client.ReplaceUserNotificationDefaults(ctx, in)
	if err != nil {
		if v.Committed {
			// Preserve the submitted snapshot. A valid GET can recover its revision.
			d.Append(state.Set(ctx, notificationDefaultsState(in, types.Int64Null()))...)
		}
		apiDiagnostic(d, "replace OnPager user notification defaults", err)
		return
	}
	d.Append(state.Set(ctx, notificationDefaultsState(v.UserNotificationDefaultsInput, types.Int64Value(v.Revision)))...)
}

func (r *userNotificationDefaultsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	in := notificationDefaultsInput(ctx, req.Plan, &resp.Diagnostics)
	if !resp.Diagnostics.HasError() {
		r.replace(ctx, in, &resp.State, &resp.Diagnostics)
	}
}

func (r *userNotificationDefaultsResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	r.replace(ctx, client.EmptyUserNotificationDefaults(), &resp.State, &resp.Diagnostics)
}

func (*userNotificationDefaultsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != "tenant" {
		resp.Diagnostics.AddError("Invalid user notification defaults import ID", "Use the literal ID tenant to import the complete template for this tenant.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
