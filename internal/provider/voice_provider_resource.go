package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type voiceProviderResource struct{ configuredResource }

var (
	_ resource.Resource                   = (*voiceProviderResource)(nil)
	_ resource.ResourceWithConfigure      = (*voiceProviderResource)(nil)
	_ resource.ResourceWithValidateConfig = (*voiceProviderResource)(nil)
	_ resource.ResourceWithImportState    = (*voiceProviderResource)(nil)
)

func NewVoiceProviderResource() resource.Resource { return &voiceProviderResource{} }
func (*voiceProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_voice_provider"
}
func (*voiceProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one tenant voice-provider connection and its owned delivery channel. Configuration validation performs no provider requests or calls.", Attributes: map[string]schema.Attribute{
		"id":           idAttribute(),
		"kind":         schema.StringAttribute{Required: true, MarkdownDescription: "Immutable voice-provider kind, currently `zvonok` only. Changing kind replaces the resource. The resource UUID is also its managed delivery-channel UUID.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"zvonok"}}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"display_name": schema.StringAttribute{Required: true, MarkdownDescription: "Nonblank UTF-8 display name, at most 200 bytes, without control characters or surrounding whitespace. Names need not be unique. Updated in place; no default."},
		"enabled":      schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether the connection is enabled. Defaults to `true`. Explicit false creates it disabled atomically; disabled resources still require complete valid configuration and public_key. Saving or refreshing performs no external validation or call."},
		"visibility":   schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("global"), MarkdownDescription: "Channel visibility: `global` (default) or `team`. Global visibility requires an empty team_ids set.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"global", "team"}}}},
		"team_ids":     schema.SetAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Whole explicit channel grant set of at most 1000 nonzero canonical lowercase team UUIDs. Omitted/null and empty both clear all grants. Must be empty for global visibility; team visibility permits an empty set. No team is discovered or created."},
		"configuration": schema.SingleNestedAttribute{Required: true, MarkdownDescription: "Typed Zvonok configuration. Both campaigns must already exist; Terraform performs no campaign or account probe.", Attributes: map[string]schema.Attribute{
			"call_campaign_id":         schema.StringAttribute{Required: true, MarkdownDescription: "Existing advanced free-text campaign identifier, 1–128 UTF-8 bytes without control characters or surrounding whitespace. No default."},
			"verification_campaign_id": schema.StringAttribute{Required: true, MarkdownDescription: "Existing separate tellcode phone-verification campaign identifier, 1–128 UTF-8 bytes without control characters or surrounding whitespace. No default. Terraform never enrolls or verifies phones."},
			"max_text_runes":           schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(500), MarkdownDescription: "Maximum rendered Unicode rune count, from 1 through 500 inclusive. Defaults to `500`; explicit zero is invalid."},
		}},
		"secrets": schema.MapNestedAttribute{Optional: true, MarkdownDescription: "Explicit named credential changes; only `public_key` is supported. Create requires replace with a nonempty value. Updates preserve stored credentials when the map/key is omitted, null or empty; keep also preserves them. Unchanged configured replacements are not replayed during unrelated updates. Known values survive refresh while the key is present. Import cannot recover values; external replacement is undetectable. Configured values persist in Terraform state and saved plans.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"action": schema.StringAttribute{Required: true, MarkdownDescription: "`replace` writes a nonempty public_key; `keep` retains the required stored key. Required public_key cannot be deleted. Omission never implies deletion.", Validators: []validator.String{notificationTemplateStringRule{values: []string{"keep", "replace"}}}},
			"value":  schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Write-only public API key, at most 4096 UTF-8 bytes without control characters. Required, non-null and nonblank for replace; empty replacement is invalid. Omit/null/empty for keep. Known configured values remain in Terraform state while the key is present."},
		}}},
		"secret_keys":         schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Credential names confirmed present by the API. Values are never returned; import recovers names only."},
		"callback_url":        schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Optional secret-bearing inbound callback URL. Null when the server omits it because ingress URL or decryption is unavailable. API-key rotation preserves its identity. It persists in Terraform state; Terraform never calls or configures it remotely."},
		"runtime_status":      schema.StringAttribute{Computed: true, MarkdownDescription: "Observed local runtime state: disabled, starting, reconfiguring, running or error. Running proves local owner/configuration readiness, not external account, balance or campaign validity."},
		"runtime_error_code":  schema.StringAttribute{Computed: true, MarkdownDescription: "Optional safe local runtime error code; null when omitted. No credential or provider response is exposed."},
		"runtime_observed_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Optional authoritative runtime observation timestamp with its returned offset and precision; null when omitted. No timestamp is synthesized."},
	}}
}
func (*voiceProviderResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m voiceProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, d := voiceInput(m, false)
	resp.Diagnostics.Append(d...)
}
func (r *voiceProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m voiceProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, d := voiceInput(m, true)
	resp.Diagnostics.Append(d...)
	if in.Secrets["public_key"].Action != "replace" {
		resp.Diagnostics.AddError("Missing voice-provider public key", "Creating a voice provider requires secrets.public_key with action replace and a nonempty value, including when disabled.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.CreateVoiceProvider(ctx, m.Kind.ValueString(), in)
	if err != nil {
		if v.Committed && validID(v.ID) {
			resp.Diagnostics.Append(resp.State.Set(ctx, voiceCommitted(m, v.ID, nil))...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager voice provider", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, voiceState(v, m))...)
}
func (r *voiceProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m voiceProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetVoiceProvider(ctx, m.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager voice provider", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, voiceState(v, m))...)
}
func (r *voiceProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, old voiceProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, d := voiceInput(m, true)
	resp.Diagnostics.Append(d...)
	if !validID(old.ID.ValueString()) || !m.Kind.Equal(old.Kind) {
		resp.Diagnostics.AddError("Invalid voice-provider update identity", "Update requires the saved canonical UUID and unchanged kind. Changing kind requires replacement.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	in = voiceChangedSecrets(in, m, old)
	v, err := r.client.UpdateVoiceProvider(ctx, old.ID.ValueString(), in)
	if v.Committed {
		m = voiceCommitted(m, old.ID.ValueString(), &old)
		resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager voice provider", err)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	v, err = r.client.GetVoiceProvider(ctx, old.ID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read committed OnPager voice provider", err)
		return
	}
	if !voiceIntentMatches(v, m, in) {
		apiDiagnostic(&resp.Diagnostics, "read committed OnPager voice provider", errors.New("OnPager returned configuration different from the committed voice-provider intent; run a fresh plan"))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, voiceState(v, m))...)
}
func (r *voiceProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m voiceProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteVoiceProvider(ctx, m.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "retire OnPager voice provider", err)
	}
}
func (*voiceProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
