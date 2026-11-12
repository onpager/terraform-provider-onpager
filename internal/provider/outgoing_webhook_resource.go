package provider

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type outgoingWebhookResource struct {
	configuredResource
	team bool
}

func NewOutgoingWebhookResource() resource.Resource     { return &outgoingWebhookResource{} }
func NewTeamOutgoingWebhookResource() resource.Resource { return &outgoingWebhookResource{team: true} }
func (r *outgoingWebhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_outgoing_webhook"
	if r.team {
		resp.TypeName = req.ProviderTypeName + "_team_outgoing_webhook"
	}
}
func outgoingOptionalString(description, value string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(value), MarkdownDescription: description}
}
func outgoingPairSchema(description string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{Optional: true, MarkdownDescription: description + " Ordered list of at most 50 pairs; duplicate query/form keys are allowed. Omitted/null and empty lists both configure no pairs. Templates are limited to 65,536 bytes per value.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{"key": schema.StringAttribute{Required: true, MarkdownDescription: "Literal nonempty key, at most 1024 bytes. Template syntax is not allowed in keys."}, "value": schema.StringAttribute{Required: true, MarkdownDescription: "Exact request template value; an explicit empty string is supported. Put credentials in named secrets and reference them through `.Secrets`."}}}}
}
func (r *outgoingWebhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	attrs := map[string]schema.Attribute{
		"id":             idAttribute(),
		"purpose":        schema.StringAttribute{Required: true, MarkdownDescription: "Immutable `notification` or `escalation` purpose. Notification definitions may declare recipient fields; escalation definitions must omit them. Changing purpose replaces this resource.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"notification", "escalation"}}}, PlanModifiers: replace},
		"display_name":   schema.StringAttribute{Required: true, MarkdownDescription: "Nonblank UTF-8 display name, at most 200 characters, without leading or trailing whitespace. Updated in place; no default."},
		"enabled":        schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether the webhook is enabled. Defaults to `true`. Disabled definitions remain readable and can be enabled again. Saving a definition performs no HTTP delivery."},
		"request_id":     schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Create idempotency UUID. When omitted, it remains unknown while planning and a fresh UUID is generated during each create/replacement, then saved in state. An explicit nonzero canonical lowercase UUID supports recovery/replay with the same principal, scope and exact initial payload. Changing an explicit key requires replacement; use a fresh explicit key for intentional replacements. Import cannot recover it and leaves it null. The provider never retries mutations automatically.", Validators: []validator.String{stringRule{uuid: true}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()}},
		"etag":           schema.StringAttribute{Computed: true, MarkdownDescription: "This definition's strong numeric ETag, validated against the response body and used for PUT/DELETE `If-Match`. Concurrent changes fail; no collection or parent revision is substituted."},
		"runtime_status": schema.StringAttribute{Computed: true, MarkdownDescription: "Backend runtime availability (`ready`, `disabled`, or `unavailable`). Availability is observed without invoking or testing the webhook."},
		"secret_keys":    schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Names of credentials the API confirms are present. GET never returns their values; import can recover names only."},
		"request": schema.SingleNestedAttribute{Required: true, MarkdownDescription: "Typed HTTP request definition. The API validates templates when saving. The provider never renders, previews, tests or delivers it. Use named secrets for credentials; all configured fields persist in Terraform state.", Attributes: map[string]schema.Attribute{
			"method":          outgoingOptionalString("Uppercase HTTP method: `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, or `DELETE`. Defaults to `POST`. GET/HEAD require an empty body and no form pairs.", "POST"),
			"endpoint":        schema.StringAttribute{Required: true, MarkdownDescription: "Absolute HTTP(S) endpoint template, at most 8192 bytes. Host and scheme must be literal; user information and fragments are forbidden. Query parameters may be declared here or in `query`; the API stores them as ordered pairs. No request is sent to this endpoint during Terraform operations."},
			"body_format":     outgoingOptionalString("Body format: `json` (default), `form`, or `text`. Form bodies use only `form`; JSON/text bodies use `body_template`.", "json"),
			"body_template":   outgoingOptionalString("Exact body template, at most 65,536 bytes. Defaults to the empty string. Omitted/null selects the default; explicit empty clears the template. Form and GET/HEAD requests require empty content.", ""),
			"content_type":    outgoingOptionalString("Optional media type for `text` bodies only. Defaults to empty, selecting the API's default. JSON/form media types are controlled by `body_format`.", ""),
			"verify_ssl":      schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Verify destination TLS certificates. Defaults to `true`; explicit false disables destination verification only, without changing provider Control TLS verification."},
			"timeout_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(5), MarkdownDescription: "Destination request timeout in whole seconds, from 1 through 60 inclusive. Defaults to `5`; zero is invalid."},
			"query":           outgoingPairSchema("Query string pairs appended after endpoint parameters are parsed."),
			"headers":         outgoingPairSchema("Header pairs. Names must be unique case-insensitively; transport/reserved headers are forbidden. Explicit Authorization conflicts with basic/bearer auth."),
			"form":            outgoingPairSchema("URL-encoded form pairs, supported only for `body_format = \"form\"`."),
			"auth": schema.SingleNestedAttribute{Optional: true, Computed: true, Default: objectdefault.StaticValue(outgoingDefaultAuth()), MarkdownDescription: "Authentication references; defaults to `none`. These fields name secrets rather than returning or exposing credential values.", Attributes: map[string]schema.Attribute{
				"kind":            outgoingOptionalString("Authentication kind: `none` (default), `basic`, or `bearer`.", "none"),
				"username":        outgoingOptionalString("Basic-auth username. Required for basic, forbidden for none/bearer. Must not contain a colon or control characters. Defaults to empty.", ""),
				"password_secret": outgoingOptionalString("Named password secret for basic authentication. Empty means no password. Forbidden for none/bearer; defaults to empty.", ""),
				"token_secret":    outgoingOptionalString("Named credential required for bearer authentication; forbidden for none/basic. Defaults to empty.", ""),
			}},
		}},
		"recipient_fields": schema.ListNestedAttribute{Optional: true, MarkdownDescription: "Ordered notification-recipient field declarations, at most 20. Omitted/null or empty means no fields. Forbidden for escalation purpose. Existing enabled personal methods may prevent incompatible changes; the provider never edits them.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"key":         schema.StringAttribute{Required: true, MarkdownDescription: "Unique ASCII key matching `[A-Za-z][A-Za-z0-9_]{0,63}`; referenced as `.Recipient.key`."},
			"label":       schema.StringAttribute{Required: true, MarkdownDescription: "Nonblank UTF-8 label of at most 200 characters."},
			"kind":        schema.StringAttribute{Required: true, MarkdownDescription: "Value type: `string`, `email`, or `phone`."},
			"required":    schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Require a nonblank recipient value. Defaults to `false`."},
			"placeholder": outgoingOptionalString("Optional field placeholder, at most 1024 bytes. Defaults to empty; explicit empty clears it.", ""),
		}}},
		"secrets": schema.MapNestedAttribute{Optional: true, MarkdownDescription: "Explicit named credential changes (at most 50 keys and 65,536 total value bytes). Omitted/null or empty maps preserve stored credentials; omitted individual keys also preserve them. Only action `delete` removes a credential. Known configured values survive refresh while the API confirms the key exists. Import cannot recover values. Configured values persist in Terraform state.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"action": schema.StringAttribute{Required: true, MarkdownDescription: "`replace` writes `value` (including explicit empty); `keep` requires an existing key and sends no value; `delete` removes this key. No action is inferred from omission.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"keep", "replace", "delete"}}}},
			"value":  schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Sensitive UTF-8 credential, at most 65,536 bytes. Required and non-null for `replace`; explicit empty writes an empty credential. Null/empty for `keep` or `delete` sends no value. Never returned by GET; known configured values remain in state until deleted or externally removed."},
		}}},
	}
	scope := "tenant"
	if r.team {
		scope = "team"
		a := uuidAttribute("Immutable owner team UUID. Changing it replaces the webhook. Import uses team_uuid/webhook_uuid.")
		a.PlanModifiers = replace
		attrs["team_id"] = a
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one " + scope + " outgoing webhook definition, HTTP request configuration and named credentials.", Attributes: attrs}
}

func outgoingDefaultAuth() types.Object {
	return types.ObjectValueMust(outgoingAuthTypes, map[string]attr.Value{"kind": types.StringValue("none"), "username": types.StringValue(""), "password_secret": types.StringValue(""), "token_secret": types.StringValue("")})
}

func (r *outgoingWebhookResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var configured types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("request_id"), &configured)...)
	if resp.Diagnostics.HasError() || !configured.IsNull() {
		return
	}
	key := types.StringUnknown()
	if !req.State.Raw.IsNull() {
		var oldPurpose, nextPurpose, oldTeam, nextTeam types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("purpose"), &oldPurpose)...)
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("purpose"), &nextPurpose)...)
		if r.team {
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("team_id"), &oldTeam)...)
			resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("team_id"), &nextTeam)...)
		}
		if oldPurpose.Equal(nextPurpose) && oldTeam.Equal(nextTeam) {
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("request_id"), &key)...)
		}
	}
	// Terraform re-plans a forced replacement with null prior state. Keep new
	// keys unknown until Create, so repeated plans never invent different UUIDs.
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("request_id"), key)...)
}
func (r *outgoingWebhookResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	m, d := r.readModel(ctx, &req.Config)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, v := range []attr.Value{m.name, m.enabled, m.purpose, m.request, m.secrets, m.recipients} {
		if outgoingHasUnknown(v) {
			return
		}
	}
	// ValidateConfig precedes Framework defaults. Materialize only documented
	// defaults here; the original configuration and planned values are untouched.
	if m.enabled.IsNull() {
		m.enabled = types.BoolValue(true)
	}
	if !m.request.IsNull() {
		a := m.request.Attributes()
		for k, value := range map[string]string{"method": "POST", "body_format": "json", "body_template": "", "content_type": ""} {
			if a[k].IsNull() {
				a[k] = types.StringValue(value)
			}
		}
		if a["verify_ssl"].IsNull() {
			a["verify_ssl"] = types.BoolValue(true)
		}
		if a["timeout_seconds"].IsNull() {
			a["timeout_seconds"] = types.Int64Value(5)
		}
		auth := a["auth"].(types.Object)
		if auth.IsNull() {
			a["auth"] = outgoingDefaultAuth()
		} else {
			aa := auth.Attributes()
			for k, value := range outgoingDefaultAuth().Attributes() {
				if aa[k].IsNull() {
					aa[k] = value
				}
			}
			a["auth"] = types.ObjectValueMust(outgoingAuthTypes, aa)
		}
		m.request = types.ObjectValueMust(outgoingRequestTypes, a)
	}
	_, d = outgoingInput(m)
	resp.Diagnostics.Append(d...)
}
func (r *outgoingWebhookResource) validIdentity(m outgoingWebhookModel) bool {
	return validID(m.id.ValueString()) && (!r.team || validID(m.team.ValueString()))
}
func (r *outgoingWebhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	m, d := r.readModel(ctx, &req.Plan)
	resp.Diagnostics.Append(d...)
	in, d := outgoingInput(m)
	resp.Diagnostics.Append(d...)
	var configuredKey types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("request_id"), &configuredKey)...)
	if configuredKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("request_id"), "Unknown webhook request ID", "An explicitly configured request UUID must resolve before creating the webhook.")
	} else if !configuredKey.IsNull() && (!validID(configuredKey.ValueString()) || !configuredKey.Equal(m.requestID)) {
		resp.Diagnostics.AddAttributeError(path.Root("request_id"), "Invalid webhook request ID", "The explicit request UUID must be known, canonical and equal to its planned value.")
	} else if configuredKey.IsNull() && !m.requestID.IsUnknown() && !m.requestID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("request_id"), "Unexpected planned webhook request ID", "An automatically generated create UUID must remain unknown until apply.")
	}
	if r.team && !validID(m.team.ValueString()) {
		resp.Diagnostics.AddError("Invalid webhook create identity", "The owner team UUID must be known and canonical.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if configuredKey.IsNull() {
		var b [16]byte
		if _, e := rand.Read(b[:]); e != nil {
			resp.Diagnostics.AddError("Unable to generate webhook request ID", "Could not obtain randomness for the create idempotency UUID.")
			return
		}
		b[6] = (b[6] & 15) | 64
		b[8] = (b[8] & 63) | 128
		m.requestID = types.StringValue(fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]))
	}
	v, e := r.client.CreateOutgoingWebhook(ctx, m.team.ValueString(), m.requestID.ValueString(), in)
	if e != nil {
		if v.Committed && validID(v.ID) {
			m.id = types.StringValue(v.ID)
			m.etag = types.StringNull()
			if client.ValidOutgoingWebhookETag(v.ETag) {
				m.etag = types.StringValue(v.ETag)
			}
			m.runtime = types.StringNull()
			m.keys = types.ListNull(types.StringType)
			resp.Diagnostics.Append(r.setModel(ctx, &resp.State, m)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager outgoing webhook", e)
		return
	}
	m, d = outgoingState(ctx, v, m)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.setModel(ctx, &resp.State, m)...)
}
func (r *outgoingWebhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	m, d := r.readModel(ctx, &req.State)
	resp.Diagnostics.Append(d...)
	if !r.validIdentity(m) {
		resp.Diagnostics.AddError("Invalid webhook identity", "Refresh requires the saved webhook UUID and its exact owner scope.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetOutgoingWebhook(ctx, m.team.ValueString(), m.id.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager outgoing webhook", e)
		return
	}
	m, d = outgoingState(ctx, v, m)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.setModel(ctx, &resp.State, m)...)
}
func (r *outgoingWebhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	m, d := r.readModel(ctx, &req.Plan)
	resp.Diagnostics.Append(d...)
	old, d := r.readModel(ctx, &req.State)
	resp.Diagnostics.Append(d...)
	in, d := outgoingInput(m)
	resp.Diagnostics.Append(d...)
	if !r.validIdentity(old) || m.team.ValueString() != old.team.ValueString() || m.purpose.ValueString() != old.purpose.ValueString() {
		resp.Diagnostics.AddError("Outgoing-webhook identity changed", "Changing owner scope or purpose requires replacement.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.UpdateOutgoingWebhook(ctx, old.team.ValueString(), old.id.ValueString(), old.etag.ValueString(), in)
	if e != nil {
		if v.Committed {
			m.id = old.id
			m.etag = types.StringNull()
			if client.ValidOutgoingWebhookETag(v.ETag) {
				m.etag = types.StringValue(v.ETag)
			}
			m.runtime = types.StringNull()
			m.keys = old.keys
			resp.Diagnostics.Append(r.setModel(ctx, &resp.State, m)...)
		}
		apiDiagnostic(&resp.Diagnostics, "update OnPager outgoing webhook", e)
		return
	}
	m, d = outgoingState(ctx, v, m)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.setModel(ctx, &resp.State, m)...)
}
func (r *outgoingWebhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	m, d := r.readModel(ctx, &req.State)
	resp.Diagnostics.Append(d...)
	if !r.validIdentity(m) {
		resp.Diagnostics.AddError("Invalid webhook identity", "Retirement requires the exact saved webhook UUID and scope.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	_, e := r.client.DisableOutgoingWebhook(ctx, m.team.ValueString(), m.id.ValueString(), m.etag.ValueString())
	if e != nil && !client.IsNotFound(e) {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager outgoing webhook", e)
	}
}
func (r *outgoingWebhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, team := req.ID, ""
	if r.team {
		parts := strings.Split(req.ID, "/")
		if len(parts) != 2 {
			resp.Diagnostics.AddError("Invalid webhook import ID", "Use team_uuid/webhook_uuid.")
			return
		}
		team, id = parts[0], parts[1]
	}
	if !validID(id) || r.team && !validID(team) {
		resp.Diagnostics.AddError("Invalid webhook import ID", "Use canonical nonzero UUIDs in the documented scope.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	if r.team {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), team)...)
	}
}
