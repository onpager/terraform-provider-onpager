package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type botResource struct{ configuredResource }

var _ resource.ResourceWithConfigure = (*botResource)(nil)
var _ resource.ResourceWithImportState = (*botResource)(nil)
var _ resource.ResourceWithValidateConfig = (*botResource)(nil)
var _ resource.ResourceWithModifyPlan = (*botResource)(nil)

func NewBotResource() resource.Resource { return &botResource{} }
func (*botResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bot"
}
func (*botResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optional := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one OnPager messenger bot and its owned delivery-channel configuration. Bot writes are unversioned. Only configuration is managed; the provider performs no messenger probes, messages, contact enrollment or target discovery.", Attributes: map[string]schema.Attribute{
		"id":                    idAttribute(),
		"messenger":             schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Immutable messenger: `telegram`, `pachca`, `slack`, `mattermost` or `discord`. Changing it replaces the bot."},
		"display_name":          schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty label of at most 200 UTF-8 bytes, without control characters or surrounding whitespace. Names may repeat."},
		"public_handle":         optional("Optional public handle, at most 200 UTF-8 bytes without control characters. Omission/null means an empty handle."),
		"enrollment_url":        optional("HTTPS bot link without credentials, at most 4096 UTF-8 bytes and without surrounding whitespace. Required for Telegram; optional for other messengers. No provider-domain restriction. Omission/null clears it."),
		"enabled":               schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Desired enabled state; defaults to `true`. Saving an enabled bot asynchronously starts its runtime. Disabled create still requires complete valid configuration and secrets, but starts no messenger runtime and needs no callback origin. Refresh/import preserve disabled state."},
		"visibility":            schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("global"), MarkdownDescription: "Bot visibility: `global` (default) or `team`. Global visibility requires empty `team_ids`."},
		"team_ids":              schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(botStrings(nil)), MarkdownDescription: "Whole authoritative set of team UUID grants, at most 1000. Added teams must be active in the tenant. Omission/null clears grants. Only valid with visibility `team`."},
		"addressing_scope_id":   schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()}, MarkdownDescription: "Immutable provider addressing ID of at most 512 UTF-8 bytes without controls or surrounding whitespace. Omit for Telegram: authenticated getMe discovers it asynchronously and it can initially be empty. Required for Pachca bot account, Slack workspace, Mattermost team and Discord guild. Changing a configured ID replaces the bot."},
		"addressing_scope_kind": schema.StringAttribute{Computed: true, MarkdownDescription: "Computed addressing kind: `bot_account`, `workspace`, `team` or `guild`."},
		"configuration": schema.SingleNestedAttribute{Required: true, MarkdownDescription: "Typed public provider settings. Use an empty object for Slack/Discord. Irrelevant messenger settings are rejected.", Attributes: map[string]schema.Attribute{
			"mode":            schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Telegram/Pachca transport: `polling` (default) or `webhook`; empty for other messengers. Webhook mode requires a saved webhook_secret. A correctly configured deployment callback origin is required when enabling webhook mode; disabled settings can be saved without it."},
			"base_url":        optional("Mattermost HTTP(S) endpoint without credentials, query or fragment. Required only for Mattermost. Use canonical form without trailing slashes. Endpoint changes additionally require `bot_credentials.replace`."),
			"allowed_updates": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, MarkdownDescription: "Telegram update names matching ^[a-z][a-z0-9_]{0,63}$, at most 32. Omission/null uses `message`, `callback_query`, `my_chat_member`. An explicit empty set is rejected because the API replaces it with defaults. Other messengers use an empty set."},
		}},
		"pacing": schema.SingleNestedAttribute{Optional: true, Computed: true, Default: objectdefault.StaticValue(botDefaultPacing()), MarkdownDescription: "Application pacing. Defaults to 10 requests per 1000 ms with no search. Search requests cannot exceed floor(requests/10).", Attributes: map[string]schema.Attribute{
			"requests":        schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(10), MarkdownDescription: "Requests per window, 1–2147483647. Defaults to 10."},
			"window_ms":       schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1000), MarkdownDescription: "Window in milliseconds, 1–86400000. Defaults to 1000."},
			"search_requests": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0), MarkdownDescription: "Search requests within the same window, 0–floor(requests/10). Defaults to 0."},
		}},
		"secrets": schema.MapNestedAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Write-only named secret actions. Keys: token (all); app_token (Slack); action_secret (Mattermost); webhook_secret (Telegram/Pachca); optional proxy_url (all). Create requires replacing all required secrets even when disabled. Omitted keys keep saved values; removing the map does not delete remote secrets. Known values persist in Terraform state; imports cannot recover them and external secret replacements cannot be detected. Unchanged replacements are omitted on metadata updates.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"action": schema.StringAttribute{Required: true, MarkdownDescription: "`keep`, `replace` or `delete`. keep/delete forbid a value; replace requires a nonempty value. Required saved secrets cannot be deleted. Delete optional secrets explicitly."},
			"value":  schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Sensitive replacement value, at most 16384 UTF-8 bytes, without controls or whitespace-only text. Required only for replace. proxy_url must be HTTP(S), may contain credentials, and forbids query/fragment. Stored in Terraform state; never returned by the API."},
		}}},
		"secret_keys":  schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Observed saved secret key names. No secret values or fingerprints are exposed."},
		"callback_url": schema.StringAttribute{Computed: true, MarkdownDescription: "Deployment-derived callback URL, or empty when no callback origin is configured. Enabled Mattermost and Telegram/Pachca webhook configurations require a correctly configured origin. Disabled create/update need no origin. CRUD does not check origin connectivity or register callbacks."},
		"runtime": schema.SingleNestedAttribute{Computed: true, MarkdownDescription: "Safe observed runtime status, separate from desired enabled state. Apply does not wait for running.", Attributes: map[string]schema.Attribute{
			"state":         schema.StringAttribute{Computed: true, MarkdownDescription: "Observed starting, running, reconfiguring, disabled or error state."},
			"error_code":    schema.StringAttribute{Computed: true, MarkdownDescription: "Safe runtime error code, or empty."},
			"last_probe_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Observed last successful provider probe timestamp, or empty. The provider never initiates a probe."},
			"observed_at":   schema.StringAttribute{Computed: true, MarkdownDescription: "Observed runtime status timestamp, or empty."},
		}},
	}}
}
func (*botResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var p, c botModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() || p.Messenger.IsUnknown() || p.Messenger.IsNull() || c.Configuration.IsUnknown() {
		return
	}
	if p.Configuration.IsUnknown() {
		if c.Configuration.IsNull() {
			p.Configuration = botDefaultConfiguration(p.Messenger.ValueString())
		} else {
			p.Configuration = c.Configuration
		}
	}
	if !p.Configuration.IsNull() && !p.Configuration.IsUnknown() {
		a := p.Configuration.Attributes()
		def := botDefaultConfiguration(p.Messenger.ValueString()).Attributes()
		ca := c.Configuration.Attributes()
		for k, v := range a {
			configured, exists := ca[k]
			if v.IsNull() || v.IsUnknown() && (!exists || configured.IsNull()) {
				a[k] = def[k]
			}
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("configuration"), types.ObjectValueMust(botConfigurationTypes, a))...)
	}
}
func (*botResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m botModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m = botDefaults(m)
	// Unknown references are deferred; apply repeats all checks before any HTTP I/O.
	for _, v := range []attr.Value{m.Messenger, m.DisplayName, m.PublicHandle, m.EnrollmentURL, m.Enabled, m.Visibility, m.TeamIDs, m.Configuration, m.Pacing, m.Secrets} {
		if botUnknown(v) {
			return
		}
	}
	in, e := botInput(m)
	if e == nil && m.Messenger.ValueString() == "telegram" && !m.ScopeID.IsNull() {
		e = errors.New("addressing_scope_id must be omitted for Telegram")
	}
	if e == nil && m.Messenger.ValueString() != "telegram" {
		if m.ScopeID.IsUnknown() {
			return
		}
		if m.ScopeID.IsNull() || m.ScopeID.ValueString() == "" {
			e = errors.New("addressing_scope_id is required for this messenger")
		}
	}
	if e == nil && m.Messenger.ValueString() == "telegram" && len(in.Configuration.AllowedUpdates) == 0 {
		e = errors.New("Telegram allowed_updates must not be explicitly empty; omit it to use defaults")
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager bot", e)
	}
}
func (r *botResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p botModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, e := botInput(p)
	if e == nil && p.Messenger.ValueString() == "telegram" && !p.ScopeID.IsNull() && !p.ScopeID.IsUnknown() && p.ScopeID.ValueString() != "" {
		e = errors.New("addressing_scope_id must be omitted for Telegram")
	}
	if e == nil && p.Messenger.ValueString() != "telegram" && (p.ScopeID.IsNull() || p.ScopeID.IsUnknown() || p.ScopeID.ValueString() == "") {
		e = errors.New("a known addressing_scope_id is required for this messenger")
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager bot", e)
		return
	}
	create := client.BotCreateInput{BotInput: in, Messenger: p.Messenger.ValueString()}
	if create.Messenger != "telegram" {
		create.AddressingScopeID = p.ScopeID.ValueString()
	}
	v, e := r.client.CreateBot(ctx, create)
	if v.Committed && validID(v.ID) {
		p = botCommittedState(p, botModel{}, v.ID, in, true)
		resp.Diagnostics.Append(resp.State.Set(ctx, p)...)
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager bot", e)
		return
	}
	if !botSettingsEqual(v.BotSettings, in.BotSettings) || v.Messenger != create.Messenger || !p.SecretKeys.Equal(botStrings(v.SecretKeys)) {
		apiDiagnostic(&resp.Diagnostics, "read created OnPager bot", errors.New("OnPager returned configuration different from the committed bot intent; run a fresh plan"))
		return
	}
	m, d := botState(v, p)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
func (r *botResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s botModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetBot(ctx, s.ID.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager bot", e)
		return
	}
	if !botIdentityMatches(v, s) {
		apiDiagnostic(&resp.Diagnostics, "read OnPager bot", errors.New("OnPager returned an unexpected immutable bot identity"))
		return
	}
	m, d := botState(v, s)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
func (r *botResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s botModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, e := botInput(p)
	if e == nil && (!p.Messenger.Equal(s.Messenger) || !p.ScopeID.IsUnknown() && !p.ScopeID.Equal(s.ScopeID)) {
		e = errors.New("messenger and addressing scope are immutable; replace the bot")
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager bot", e)
		return
	}
	in = botChangedSecrets(in, p, s)
	receipt, e := r.client.UpdateBot(ctx, s.ID.ValueString(), s.Messenger.ValueString(), in)
	if receipt.Committed {
		p = botCommittedState(p, s, s.ID.ValueString(), in, false)
		resp.Diagnostics.Append(resp.State.Set(ctx, p)...)
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager bot", e)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetBot(ctx, s.ID.ValueString())
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read committed OnPager bot", e)
		return
	}
	if !botIdentityMatches(v, s) || !botSettingsEqual(v.BotSettings, in.BotSettings) || !p.SecretKeys.Equal(botStrings(v.SecretKeys)) {
		apiDiagnostic(&resp.Diagnostics, "read committed OnPager bot", errors.New("OnPager returned configuration different from the committed bot intent; run a fresh plan"))
		return
	}
	m, d := botState(v, p)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
func (r *botResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s botModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.DeleteBot(ctx, s.ID.ValueString()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager bot", e)
	}
}
func (*botResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
