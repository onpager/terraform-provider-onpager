package provider

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type notificationTemplateResource struct {
	configuredResource
	team bool
}

func NewNotificationTemplateResource() resource.Resource { return &notificationTemplateResource{} }
func NewTeamNotificationTemplateResource() resource.Resource {
	return &notificationTemplateResource{team: true}
}
func (r *notificationTemplateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_template"
	if r.team {
		resp.TypeName = req.ProviderTypeName + "_team_notification_template"
	}
}

var notificationTemplateTypes = []string{"alert_direct", "alert_channel", "schedule_handover", "incident_announcement", "incident_call", "responder_request", "alert_channel_call", "responder_feedback", "connection_confirmation"}
var teamNotificationTemplateTypes = []string{"alert_direct", "alert_channel", "schedule_handover"}
var notificationTemplateFormats = map[string][]string{"telegram": {"telegram_html", "telegram_markdown_v2", "plain"}, "mattermost": {"mattermost_markdown"}, "slack": {"slack_mrkdwn", "slack_plain"}, "discord": {"discord_markdown"}, "pachca": {"pachca_markdown"}, "zvonok": {"plain"}}

func notificationTemplateProviderSupportsType(kind, deliveryProvider string) bool {
	if deliveryProvider == "zvonok" {
		return notificationContains([]string{"alert_direct", "incident_call", "responder_request", "responder_feedback"}, kind)
	}
	return notificationTemplateFormats[deliveryProvider] != nil && notificationContains(notificationTemplateTypes, kind)
}

func notificationContains(values []string, v string) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}
	return false
}

type notificationTemplateStringRule struct {
	values []string
	body   bool
}

func (v notificationTemplateStringRule) Description(context.Context) string {
	if v.body {
		return "A nonblank UTF-8 template body of at most 65536 bytes without NUL characters."
	}
	return "One of: " + strings.Join(v.values, ", ") + "."
}
func (v notificationTemplateStringRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v notificationTemplateStringRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if v.body && (strings.TrimSpace(s) == "" || len(s) > 64<<10 || strings.ContainsRune(s, 0) || !utf8.ValidString(s)) || !v.body && !notificationContains(v.values, s) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid notification template", v.Description(ctx))
	}
}
func (r *notificationTemplateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	kinds := notificationTemplateTypes
	if r.team {
		kinds = teamNotificationTemplateTypes
	}
	replacement := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	attrs := map[string]schema.Attribute{
		"id":                schema.StringAttribute{Computed: true, MarkdownDescription: "Stable global override slot identity: `type/provider/language`. Also used for import.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"type":              schema.StringAttribute{Required: true, MarkdownDescription: "Immutable notification type, required without a default; null and empty strings are invalid. Supported values: `" + strings.Join(kinds, "`, `") + "`. Changing it replaces this resource.", Validators: []validator.String{notificationTemplateStringRule{values: kinds}}, PlanModifiers: replacement},
		"delivery_provider": schema.StringAttribute{Required: true, MarkdownDescription: "Immutable delivery provider, required without a default: `telegram`, `mattermost`, `slack`, `discord`, `pachca`, or `zvonok`. Zvonok supports only `alert_direct`, `incident_call`, `responder_request`, and `responder_feedback`; only `alert_direct` supports a team override. Null and empty strings are invalid. Changing it replaces this resource. No channel lookup or delivery occurs.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"telegram", "mattermost", "slack", "discord", "pachca", "zvonok"}}}, PlanModifiers: replacement},
		"language":          schema.StringAttribute{Required: true, MarkdownDescription: "Immutable template language, exactly `ru` or `en`, required without a default. Null and empty strings are invalid. Each language has independent overrides, inheritance and ETags; there is no fallback to the other language. Changing it replaces this resource.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"ru", "en"}}}, PlanModifiers: replacement},
		"format":            schema.StringAttribute{Required: true, MarkdownDescription: "Template format, required without a default. Telegram supports `telegram_html`, `telegram_markdown_v2`, or `plain`; Mattermost `mattermost_markdown`; Slack `slack_mrkdwn` or `slack_plain`; Discord `discord_markdown`; Pachca `pachca_markdown`; Zvonok `plain`. Must match `delivery_provider`; null and empty strings are invalid.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"telegram_html", "telegram_markdown_v2", "plain", "mattermost_markdown", "slack_mrkdwn", "slack_plain", "discord_markdown", "pachca_markdown"}}}},
		"body":              schema.StringAttribute{Required: true, MarkdownDescription: "Exact UTF-8 template source, required without a default. At most 65,536 bytes; blank bodies, NUL characters, null and empty strings are rejected. Whitespace and newlines are preserved. The API validates syntax and allowed context fields when saving. Stored in Terraform state.", Validators: []validator.String{notificationTemplateStringRule{body: true}}},
		"etag":              schema.StringAttribute{Computed: true, MarkdownDescription: "Strong SHA-256 ETag of this exact override slot, used as `If-Match` for replacement and deletion. Independent of inherited content; concurrent changes report a conflict."},
	}
	if r.team {
		team := uuidAttribute("Immutable owning team UUID, required without a default; null and empty strings are invalid. Changing it replaces the resource.")
		team.PlanModifiers = replacement
		attrs["team_id"] = team
		attrs["id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "Stable team override slot identity: `team_id/type/provider/language`. Also used for import.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one global notification template override for an explicit notification type, delivery provider and language.", Attributes: attrs}
	if r.team {
		resp.Schema.MarkdownDescription = "Manage one team notification template override for an explicit notification type, delivery provider and language."
	}
}

type notificationTemplateModel struct{ id, team, kind, deliveryProvider, language, format, body, etag types.String }

func (r *notificationTemplateResource) readModel(ctx context.Context, from routingAttributeReader) (notificationTemplateModel, diag.Diagnostics) {
	var v notificationTemplateModel
	var d diag.Diagnostics
	for _, a := range []struct {
		name  string
		value *types.String
	}{{"id", &v.id}, {"type", &v.kind}, {"delivery_provider", &v.deliveryProvider}, {"language", &v.language}, {"format", &v.format}, {"body", &v.body}, {"etag", &v.etag}} {
		d.Append(from.GetAttribute(ctx, path.Root(a.name), a.value)...)
	}
	if r.team {
		d.Append(from.GetAttribute(ctx, path.Root("team_id"), &v.team)...)
	}
	return v, d
}
func notificationTemplateID(v notificationTemplateModel) string {
	prefix := ""
	if !v.team.IsNull() && !v.team.IsUnknown() && v.team.ValueString() != "" {
		prefix = v.team.ValueString() + "/"
	}
	return prefix + v.kind.ValueString() + "/" + v.deliveryProvider.ValueString() + "/" + v.language.ValueString()
}
func (r *notificationTemplateResource) setModel(ctx context.Context, state *tfsdk.State, v notificationTemplateModel) diag.Diagnostics {
	values := map[string]attr.Value{"id": types.StringValue(notificationTemplateID(v)), "type": v.kind, "delivery_provider": v.deliveryProvider, "language": v.language, "format": v.format, "body": v.body, "etag": v.etag}
	if r.team {
		values["team_id"] = v.team
	}
	attributeTypes := make(map[string]attr.Type, len(values))
	for name := range values {
		attributeTypes[name] = types.StringType
	}
	obj, d := types.ObjectValue(attributeTypes, values)
	if d.HasError() {
		return d
	}
	d.Append(state.Set(ctx, obj)...)
	return d
}
func (r *notificationTemplateResource) validateModel(v notificationTemplateModel, desired bool) diag.Diagnostics {
	var d diag.Diagnostics
	kinds := notificationTemplateTypes
	if r.team {
		kinds = teamNotificationTemplateTypes
	}
	if r.team && (v.team.IsUnknown() || v.team.IsNull() || !validID(v.team.ValueString())) {
		d.AddError("Invalid notification-template owner", "Use a known nonzero canonical lowercase team UUID before making a request.")
		return d
	}
	if v.kind.IsUnknown() || v.kind.IsNull() || !notificationContains(kinds, v.kind.ValueString()) || v.deliveryProvider.IsUnknown() || v.deliveryProvider.IsNull() || !notificationTemplateProviderSupportsType(v.kind.ValueString(), v.deliveryProvider.ValueString()) || v.language.IsUnknown() || v.language.IsNull() || !notificationContains([]string{"ru", "en"}, v.language.ValueString()) {
		d.AddError("Invalid notification-template identity", "Use a known supported type and delivery provider combination with explicit language ru or en before making a request.")
		return d
	}
	if !v.id.IsNull() && !v.id.IsUnknown() && v.id.ValueString() != notificationTemplateID(v) {
		d.AddError("Inconsistent notification-template identity", "The saved slot ID must match its team, type, delivery provider and language; state is retained.")
		return d
	}
	if desired && (v.format.IsUnknown() || v.format.IsNull() || !notificationContains(notificationTemplateFormats[v.deliveryProvider.ValueString()], v.format.ValueString()) || v.body.IsUnknown() || v.body.IsNull() || strings.TrimSpace(v.body.ValueString()) == "" || len(v.body.ValueString()) > 64<<10 || strings.ContainsRune(v.body.ValueString(), 0) || !utf8.ValidString(v.body.ValueString())) {
		d.AddError("Invalid notification-template content", "Use a known format supported by the delivery provider and a nonblank UTF-8 body of at most 65536 bytes without NUL characters.")
	}
	return d
}
func (r *notificationTemplateResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	v, d := r.readModel(ctx, &req.Config)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !v.deliveryProvider.IsUnknown() && !v.deliveryProvider.IsNull() {
		if !v.kind.IsUnknown() && !v.kind.IsNull() && !notificationTemplateProviderSupportsType(v.kind.ValueString(), v.deliveryProvider.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root("delivery_provider"), "Unsupported notification template", "Select a delivery provider that supports the configured notification type. Zvonok supports alert_direct, incident_call, responder_request and responder_feedback, with team overrides only for alert_direct.")
		}
		if !v.format.IsUnknown() && !v.format.IsNull() && !notificationContains(notificationTemplateFormats[v.deliveryProvider.ValueString()], v.format.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root("format"), "Format does not match delivery provider", "Select one of the formats documented for the configured delivery provider.")
		}
	}
}
func (r *notificationTemplateResource) saveResult(ctx context.Context, state *tfsdk.State, p notificationTemplateModel, v client.NotificationTemplateView, diags *diag.Diagnostics) {
	if v.Override != nil {
		p.format = types.StringValue(v.Override.Format)
		p.body = types.StringValue(v.Override.Body)
	}
	p.etag = types.StringNull()
	if v.ETag != "" {
		p.etag = types.StringValue(v.ETag)
	}
	diags.Append(r.setModel(ctx, state, p)...)
}
func (r *notificationTemplateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	p, d := r.readModel(ctx, &req.Plan)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.validateModel(p, true)...)
	if resp.Diagnostics.HasError() {
		return
	}
	before, err := r.client.GetNotificationTemplate(ctx, p.team.ValueString(), p.kind.ValueString(), p.deliveryProvider.ValueString(), p.language.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read current OnPager notification-template slot", err)
		return
	}
	v, err := r.client.PutNotificationTemplate(ctx, p.team.ValueString(), p.kind.ValueString(), p.deliveryProvider.ValueString(), p.language.ValueString(), before.ETag, client.NotificationTemplateDraft{Format: p.format.ValueString(), Body: p.body.ValueString()})
	if err != nil {
		if v.Committed {
			v.Override = nil
			r.saveResult(ctx, &resp.State, p, v, &resp.Diagnostics)
		}
		apiDiagnostic(&resp.Diagnostics, "save OnPager notification-template override", err)
		return
	}
	if v.Override == nil {
		r.saveResult(ctx, &resp.State, p, v, &resp.Diagnostics)
		resp.Diagnostics.AddError("Missing saved override", "OnPager accepted the save but did not confirm a stored override; refresh before retrying.")
		return
	}
	r.saveResult(ctx, &resp.State, p, v, &resp.Diagnostics)
}
func (r *notificationTemplateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	p, d := r.readModel(ctx, &req.State)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.validateModel(p, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetNotificationTemplate(ctx, p.team.ValueString(), p.kind.ValueString(), p.deliveryProvider.ValueString(), p.language.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager notification-template override", err)
		return
	}
	if v.Override == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	r.saveResult(ctx, &resp.State, p, v, &resp.Diagnostics)
}
func (r *notificationTemplateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	p, d := r.readModel(ctx, &req.Plan)
	resp.Diagnostics.Append(d...)
	old, d := r.readModel(ctx, &req.State)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.validateModel(p, true)...)
	resp.Diagnostics.Append(r.validateModel(old, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if notificationTemplateID(p) != notificationTemplateID(old) {
		resp.Diagnostics.AddError("Notification-template slot changed", "Changing the team, template type, delivery provider or language requires replacement.")
		return
	}
	v, err := r.client.PutNotificationTemplate(ctx, p.team.ValueString(), p.kind.ValueString(), p.deliveryProvider.ValueString(), p.language.ValueString(), old.etag.ValueString(), client.NotificationTemplateDraft{Format: p.format.ValueString(), Body: p.body.ValueString()})
	if err != nil {
		if v.Committed {
			v.Override = nil
			r.saveResult(ctx, &resp.State, p, v, &resp.Diagnostics)
		}
		apiDiagnostic(&resp.Diagnostics, "update OnPager notification-template override", err)
		return
	}
	if v.Override == nil {
		r.saveResult(ctx, &resp.State, p, v, &resp.Diagnostics)
		resp.Diagnostics.AddError("Missing saved override", "OnPager accepted the update but did not confirm a stored override; refresh before retrying.")
		return
	}
	r.saveResult(ctx, &resp.State, p, v, &resp.Diagnostics)
}
func (r *notificationTemplateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	p, d := r.readModel(ctx, &req.State)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.validateModel(p, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Only a successful exact read confirms that the managed override is gone.
	// An HTTP failure must never be treated as a successful reset.
	before, err := r.client.GetNotificationTemplate(ctx, p.team.ValueString(), p.kind.ValueString(), p.deliveryProvider.ValueString(), p.language.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager notification-template override before reset", err)
		return
	}
	if before.Override == nil {
		return
	}
	v, err := r.client.DeleteNotificationTemplate(ctx, p.team.ValueString(), p.kind.ValueString(), p.deliveryProvider.ValueString(), p.language.ValueString(), p.etag.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "reset OnPager notification-template override", err)
		return
	}
	if v.Override != nil {
		resp.Diagnostics.AddError("Override was not removed", "OnPager did not confirm removal of the stored override; state is retained.")
	}
}
func (r *notificationTemplateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	expected, message := 3, "Use type/provider/language for the global override slot."
	if r.team {
		expected, message = 4, "Use team_id/type/provider/language for the team override slot."
	}
	if len(parts) != expected {
		resp.Diagnostics.AddError("Invalid notification-template import ID", message)
		return
	}
	v := notificationTemplateModel{kind: types.StringValue(parts[expected-3]), deliveryProvider: types.StringValue(parts[expected-2]), language: types.StringValue(parts[expected-1])}
	if r.team {
		v.team = types.StringValue(parts[0])
	}
	resp.Diagnostics.Append(r.validateModel(v, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.setModel(ctx, &resp.State, v)...)
}
