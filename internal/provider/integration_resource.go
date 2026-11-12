package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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

type integrationResource struct{ configuredResource }

func NewIntegrationResource() resource.Resource { return &integrationResource{} }
func (*integrationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_integration"
}

func (*integrationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage an OnPager source integration, including optional Alertmanager polling.", Attributes: map[string]schema.Attribute{
		"id":           idAttribute(),
		"name":         schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty integration name without leading or trailing whitespace.", Validators: []validator.String{stringRule{}}},
		"source_kind":  schema.StringAttribute{Required: true, MarkdownDescription: "Source format: `alertmanager`, `grafana_unified`, `grafana_legacy`, or `generic_onpager_v1`. Changing it replaces the integration.", Validators: []validator.String{enumRule{"alertmanager", "grafana_unified", "grafana_legacy", "generic_onpager_v1"}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"enabled":      schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether the integration accepts events and permits configured polling. Defaults to `true`. Disabled integrations remain in state."},
		"revision":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Integration settings revision used in strong numeric `If-Match` for updates and deletion. Stale revisions report a conflict; run a new plan before retrying."},
		"webhook_path": schema.StringAttribute{Computed: true, MarkdownDescription: "Ingress webhook path supplied by the server for this integration."},
		"webhook_url":  schema.StringAttribute{Computed: true, MarkdownDescription: "Ingress webhook URL supplied when the server configures its public base URL; otherwise `null`. Never derived from the Control endpoint."},
		"token":        schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "One-time webhook credential returned on creation. Preserved across refresh and updates; unavailable after import. Stored in Terraform state even though normal display is hidden by the sensitive marking. Token rotation is not implemented.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"polling": schema.SingleNestedAttribute{Optional: true, MarkdownDescription: "Polling settings, accepted only for `alertmanager`. Omission or `null` disables polling while the server retains disabled settings. Enabling polling lets the backend contact `base_url`; the provider sends no immediate poll command. Secrets are stored in Terraform state and cannot be recovered on import.", Attributes: map[string]schema.Attribute{
			"enabled":          schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Whether the backend polls the configured endpoint. Defaults to `false`. Polling also requires the integration to be enabled."},
			"base_url":         schema.StringAttribute{Required: true, MarkdownDescription: "Explicit nonempty HTTP(S) base URL, required even when polling is disabled. Must have a host and no user information, query, fragment, encoded path, backslashes, or dot path segments. Trailing slashes are accepted."},
			"interval_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(60), MarkdownDescription: "Polling interval in whole seconds, from 15 through 3600. Defaults to 60; zero is invalid."},
			"auth_kind":        schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("none"), MarkdownDescription: "Authentication kind: `none` (default), `basic`, or `bearer`. `none` forbids credentials; `basic` uses username/password; `bearer` uses only token.", Validators: []validator.String{enumRule{"none", "basic", "bearer"}}},
			"username":         schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Basic authentication username. Defaults to an empty string; a nonblank username is required for `basic`. Must be empty for `none` or `bearer`."},
			"password":         schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Basic authentication password, only for `basic`. Omission or `null` preserves a saved server secret; an empty string explicitly replaces it with an empty password. Configured values persist in Terraform state even though marked sensitive; the API never returns the secret."},
			"token":            schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Bearer polling credential, only for `bearer`. Omission or `null` preserves a saved server secret. A replacement must be nonempty and contain no whitespace or control characters. Configured values persist in Terraform state even though marked sensitive; the API never returns the secret."},
			"has_secret":       schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the backend stores a polling credential. This flag does not reveal or recover the secret."},
		}},
	}}
}

func (r *integrationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan integrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	poll, d := integrationPollInput(ctx, plan.Polling)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	input := client.IntegrationInput{Name: plan.Name.ValueString(), SourceKind: plan.SourceKind.ValueString(), Polling: poll}
	// Create has no enabled field. For a disabled integration, configure polling
	// with the subsequent atomic disabled PATCH so it cannot start before disable.
	if !plan.Enabled.ValueBool() {
		input.Polling = nil
	}
	v, err := r.client.CreateIntegration(ctx, input)
	if err != nil {
		// A successful HTTP create can still carry an incomplete receipt. Keep
		// its trustworthy UUID/token so Terraform can refresh/retire the object.
		if validID(v.ID) {
			partial := plan
			partial.ID, partial.Enabled = types.StringValue(v.ID), types.BoolValue(true)
			partial.Revision = types.Int64Null()
			if v.Revision > 0 {
				partial.Revision = types.Int64Value(v.Revision)
			}
			partial.WebhookPath, partial.WebhookURL, partial.Token = types.StringNull(), types.StringNull(), types.StringNull()
			if v.WebhookPath != "" {
				partial.WebhookPath = types.StringValue(v.WebhookPath)
			}
			if v.WebhookURL != "" {
				partial.WebhookURL = types.StringValue(v.WebhookURL)
			}
			if v.Token != "" {
				partial.Token = types.StringValue(v.Token)
			}
			partial.Polling = types.ObjectNull(integrationPollingTypes)
			resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager integration", err)
		return
	}
	m, d := integrationState(ctx, v, plan)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Enabled.ValueBool() {
		off := false
		receipt, err := r.client.UpdateIntegration(ctx, v.ID, v.Revision, client.IntegrationPatch{Enabled: &off, Polling: poll})
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "configure disabled OnPager integration", err)
			return
		}
		m.Enabled, m.Revision = types.BoolValue(false), types.Int64Value(receipt.Revision)
		resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
		if resp.Diagnostics.HasError() {
			return
		}
		fresh, err := r.client.GetIntegration(ctx, v.ID)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "refresh created OnPager integration", err)
			return
		}
		plan.Token = m.Token
		m, d = integrationState(ctx, fresh, plan)
		resp.Diagnostics.Append(d...)
		resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
	}
}

func (r *integrationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetIntegration(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager integration", err)
		return
	}
	m, d := integrationState(ctx, v, state)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *integrationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := client.IntegrationPatch{}
	if !state.Name.Equal(plan.Name) {
		name := plan.Name.ValueString()
		patch.Name = &name
	}
	if !state.Enabled.Equal(plan.Enabled) {
		enabled := plan.Enabled.ValueBool()
		patch.Enabled = &enabled
	}
	if !integrationPollingEqual(state.Polling, plan.Polling) {
		var d diag.Diagnostics
		patch.Polling, d = integrationPollInput(ctx, plan.Polling)
		resp.Diagnostics.Append(d...)
		if plan.Polling.IsNull() {
			patch.Polling = &client.IntegrationPollingInput{Enabled: false}
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if patch.Name != nil || patch.Enabled != nil || patch.Polling != nil {
		receipt, err := r.client.UpdateIntegration(ctx, state.ID.ValueString(), state.Revision.ValueInt64(), patch)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "update OnPager integration", err)
			return
		}
		state.Name, state.Enabled, state.Revision = plan.Name, plan.Enabled, types.Int64Value(receipt.Revision)
		// Save actual successful settings with known computed values even if GET
		// later fails; the one-time token always comes from existing state.
		if patch.Polling != nil {
			p := patch.Polling
			partial := client.Integration{ID: state.ID.ValueString(), Name: state.Name.ValueString(), SourceKind: state.SourceKind.ValueString(), Enabled: state.Enabled.ValueBool(), Revision: receipt.Revision, WebhookPath: state.WebhookPath.ValueString(), WebhookURL: state.WebhookURL.ValueString(), Polling: &client.IntegrationPolling{Enabled: p.Enabled, BaseURL: p.BaseURL, IntervalSeconds: p.IntervalSeconds, AuthKind: p.AuthKind, Username: p.Username, HasSecret: p.AuthKind != "none"}}
			prior := plan
			prior.Token = state.Token
			var d diag.Diagnostics
			state, d = integrationState(ctx, partial, prior)
			resp.Diagnostics.Append(d...)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	v, err := r.client.GetIntegration(ctx, state.ID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "refresh updated OnPager integration", err)
		return
	}
	prior := plan
	prior.Token = state.Token
	m, d := integrationState(ctx, v, prior)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *integrationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteIntegration(ctx, state.ID.ValueString(), state.Revision.ValueInt64()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager integration", err)
	}
}
func (*integrationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
