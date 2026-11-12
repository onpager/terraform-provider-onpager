package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type integrationModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	SourceKind  types.String `tfsdk:"source_kind"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Revision    types.Int64  `tfsdk:"revision"`
	WebhookPath types.String `tfsdk:"webhook_path"`
	WebhookURL  types.String `tfsdk:"webhook_url"`
	Token       types.String `tfsdk:"token"`
	Polling     types.Object `tfsdk:"polling"`
}

type integrationPollingModel struct {
	Enabled         types.Bool   `tfsdk:"enabled"`
	BaseURL         types.String `tfsdk:"base_url"`
	IntervalSeconds types.Int64  `tfsdk:"interval_seconds"`
	AuthKind        types.String `tfsdk:"auth_kind"`
	Username        types.String `tfsdk:"username"`
	Password        types.String `tfsdk:"password"`
	Token           types.String `tfsdk:"token"`
	HasSecret       types.Bool   `tfsdk:"has_secret"`
}

var integrationPollingTypes = map[string]attr.Type{
	"enabled": types.BoolType, "base_url": types.StringType, "interval_seconds": types.Int64Type,
	"auth_kind": types.StringType, "username": types.StringType, "password": types.StringType,
	"token": types.StringType, "has_secret": types.BoolType,
}

func integrationPollInput(ctx context.Context, obj types.Object) (*client.IntegrationPollingInput, diag.Diagnostics) {
	if obj.IsNull() {
		return nil, nil
	}
	var p integrationPollingModel
	d := obj.As(ctx, &p, basetypes.ObjectAsOptions{})
	if d.HasError() {
		return nil, d
	}
	return &client.IntegrationPollingInput{Enabled: p.Enabled.ValueBool(), BaseURL: p.BaseURL.ValueString(), IntervalSeconds: p.IntervalSeconds.ValueInt64(), AuthKind: p.AuthKind.ValueString(), Username: p.Username.ValueString(), Password: stringPointer(p.Password), Token: stringPointer(p.Token)}, d
}

func integrationPollingEqual(a, b types.Object) bool {
	if a.IsNull() || b.IsNull() || a.IsUnknown() || b.IsUnknown() {
		return a.Equal(b)
	}
	aa, bb := a.Attributes(), b.Attributes()
	for _, key := range []string{"enabled", "base_url", "interval_seconds", "auth_kind", "username", "password", "token"} {
		if !aa[key].Equal(bb[key]) {
			return false
		}
	}
	return true
}

func integrationState(ctx context.Context, v client.Integration, prior integrationModel) (integrationModel, diag.Diagnostics) {
	m := integrationModel{ID: types.StringValue(v.ID), Name: types.StringValue(v.Name), SourceKind: types.StringValue(v.SourceKind), Enabled: types.BoolValue(v.Enabled), Revision: types.Int64Value(v.Revision), WebhookPath: types.StringValue(v.WebhookPath), WebhookURL: types.StringNull(), Token: prior.Token, Polling: types.ObjectNull(integrationPollingTypes)}
	if v.WebhookURL != "" {
		m.WebhookURL = types.StringValue(v.WebhookURL)
	}
	if v.Token != "" {
		m.Token = types.StringValue(v.Token)
	}
	if m.Token.IsUnknown() {
		m.Token = types.StringNull()
	}
	var d diag.Diagnostics
	if v.Polling == nil {
		return m, d
	}
	// Omitting polling means disabled. The backend retains disabled settings;
	// retain a null declaration while no polling is enabled remotely.
	if prior.Polling.IsNull() && !v.Polling.Enabled {
		return m, d
	}
	old := integrationPollingModel{Password: types.StringNull(), Token: types.StringNull()}
	if !prior.Polling.IsNull() && !prior.Polling.IsUnknown() {
		d.Append(prior.Polling.As(ctx, &old, basetypes.ObjectAsOptions{})...)
	}
	p := v.Polling
	row := integrationPollingModel{Enabled: types.BoolValue(p.Enabled), BaseURL: types.StringValue(p.BaseURL), IntervalSeconds: types.Int64Value(p.IntervalSeconds), AuthKind: types.StringValue(p.AuthKind), Username: types.StringValue(p.Username), Password: types.StringNull(), Token: types.StringNull(), HasSecret: types.BoolValue(p.HasSecret)}
	if !old.BaseURL.IsNull() && !old.BaseURL.IsUnknown() && strings.TrimRight(old.BaseURL.ValueString(), "/") == p.BaseURL {
		row.BaseURL = old.BaseURL
	}
	if p.HasSecret && old.AuthKind.ValueString() == p.AuthKind {
		if p.AuthKind == "basic" {
			row.Password = old.Password
		} else if p.AuthKind == "bearer" {
			row.Token = old.Token
		}
	}
	obj, ds := types.ObjectValueFrom(ctx, integrationPollingTypes, row)
	d.Append(ds...)
	m.Polling = obj
	return m, d
}
