package provider

import (
	"context"
	"net/url"
	"strings"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

func (*integrationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m integrationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Polling.IsNull() || m.Polling.IsUnknown() {
		return
	}
	if !m.SourceKind.IsUnknown() && !m.SourceKind.IsNull() && m.SourceKind.ValueString() != "alertmanager" {
		resp.Diagnostics.AddAttributeError(path.Root("polling"), "Unsupported polling source", "Only alertmanager integrations support polling settings.")
	}
	var p integrationPollingModel
	resp.Diagnostics.Append(m.Polling.As(ctx, &p, basetypes.ObjectAsOptions{})...)
	if resp.Diagnostics.HasError() {
		return
	}
	base := path.Root("polling")
	if !p.IntervalSeconds.IsNull() && !p.IntervalSeconds.IsUnknown() && (p.IntervalSeconds.ValueInt64() < 15 || p.IntervalSeconds.ValueInt64() > 3600) {
		resp.Diagnostics.AddAttributeError(base.AtName("interval_seconds"), "Invalid polling interval", "Use 15 through 3600 seconds.")
	}
	if !p.BaseURL.IsUnknown() {
		u, err := url.Parse(p.BaseURL.ValueString())
		valid := err == nil && u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" && u.RawPath == "" && !strings.Contains(u.Path, "\\")
		if valid {
			for _, part := range strings.Split(u.Path, "/") {
				if part == "." || part == ".." {
					valid = false
				}
			}
		}
		if !valid {
			resp.Diagnostics.AddAttributeError(base.AtName("base_url"), "Invalid polling URL", "Use an HTTP(S) base URL without user information, query, fragment or dot path segments.")
		}
	}
	if p.AuthKind.IsUnknown() {
		return
	}
	kind := p.AuthKind.ValueString()
	if kind == "" {
		kind = "none"
	}
	switch kind {
	case "none":
		if !p.Password.IsNull() || !p.Token.IsNull() || p.Username.ValueString() != "" {
			resp.Diagnostics.AddAttributeError(base.AtName("auth_kind"), "Unexpected polling credentials", "Credentials require basic or bearer authentication.")
		}
	case "basic":
		if !p.Username.IsUnknown() && strings.TrimSpace(p.Username.ValueString()) == "" {
			resp.Diagnostics.AddAttributeError(base.AtName("username"), "Missing Basic username", "Basic authentication requires a username.")
		}
		if !p.Token.IsNull() {
			resp.Diagnostics.AddAttributeError(base.AtName("token"), "Inapplicable polling token", "Basic authentication uses password rather than token.")
		}
	case "bearer":
		if !p.Password.IsNull() || p.Username.ValueString() != "" {
			resp.Diagnostics.AddAttributeError(base.AtName("auth_kind"), "Inapplicable polling credentials", "Bearer authentication uses token only.")
		}
		if !p.Token.IsNull() && !p.Token.IsUnknown() && (p.Token.ValueString() == "" || strings.IndexFunc(p.Token.ValueString(), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
			resp.Diagnostics.AddAttributeError(base.AtName("token"), "Invalid polling token", "A replacement Bearer token must be nonempty without whitespace or control characters.")
		}
	}
}
