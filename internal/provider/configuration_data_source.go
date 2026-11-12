package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// configurationDataSource shares selector validation and configured transport;
// each kind has its own schema and typed state model.
type configurationDataSource struct {
	kind   string
	client *client.Client
}

var (
	_ datasource.DataSource                   = (*configurationDataSource)(nil)
	_ datasource.DataSourceWithConfigure      = (*configurationDataSource)(nil)
	_ datasource.DataSourceWithValidateConfig = (*configurationDataSource)(nil)
)

func NewUserDataSource() datasource.DataSource    { return &configurationDataSource{kind: "user"} }
func NewTeamDataSource() datasource.DataSource    { return &configurationDataSource{kind: "team"} }
func NewServiceDataSource() datasource.DataSource { return &configurationDataSource{kind: "service"} }
func NewRBACRoleDataSource() datasource.DataSource {
	return &configurationDataSource{kind: "rbac_role"}
}
func NewGroupDataSource() datasource.DataSource { return &configurationDataSource{kind: "group"} }
func NewDeliveryChannelDataSource() datasource.DataSource {
	return &configurationDataSource{kind: "delivery_channel"}
}

func (d *configurationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.kind
}

func (d *configurationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok || c == nil {
		resp.Diagnostics.AddError("Invalid OnPager client", "Provider configuration supplied an unexpected client type.")
		return
	}
	d.client = c
}

func lookupSelectorAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: description}
}

func lookupComputedString(description string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: description}
}

func (d *configurationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	id := lookupSelectorAttribute("Stable OnPager UUID. Configure exactly one identity selector. Must be a nonzero canonical lowercase UUID; computed when another selector is configured.")
	name := lookupSelectorAttribute("Exact case-sensitive name, preserving whitespace. Configure exactly one identity selector. Maximum 4096 UTF-8 bytes; use `id` for longer stored names. Computed when another selector is configured.")
	key := lookupSelectorAttribute("Exact immutable key. Configure exactly one identity selector. Maximum 4096 UTF-8 bytes; computed when another selector is configured.")
	active := schema.BoolAttribute{Computed: true, MarkdownDescription: "Observed active state. Inactive objects remain readable; this data source never changes their state."}
	attrs := map[string]schema.Attribute{"id": id}
	description := "Read one OnPager " + strings.ReplaceAll(d.kind, "_", " ") + " through authorized configuration reads. Absence, ambiguity and denied reads are errors. Reads never modify configuration."
	switch d.kind {
	case "user":
		attrs["local_login"] = schema.StringAttribute{Optional: true, MarkdownDescription: "Exact local credential login selector; never an OIDC subject or display name. Configure exactly one of `id`, `local_login` or `email`. The backend trims and Unicode case-folds this selector. The original configured value is retained; maximum 4096 UTF-8 bytes."}
		attrs["email"] = schema.StringAttribute{Optional: true, MarkdownDescription: "Exact human email selector for local or OIDC users. Configure exactly one of `id`, `local_login` or `email`. The backend trims and Unicode case-folds this selector; ambiguous email matches fail. The original configured value is retained; maximum 4096 UTF-8 bytes."}
		attrs["display_name"] = lookupComputedString("Observed public display name.")
		attrs["email_normalized"] = lookupComputedString("Observed normalized human email, or an empty string when no email is stored. The private identity read requires tenant `users.read`.")
		attrs["active"] = active
	case "team":
		attrs["name"], attrs["active"] = name, active
	case "service":
		attrs["name"], attrs["active"] = name, active
		attrs["team_id"] = lookupSelectorAttribute("Current owner team UUID. When configured, constrains expected ownership for either an `id` or `name` selector; it is not an identity selector. Must be a nonzero canonical lowercase UUID. Computed from the observed service when omitted.")
	case "rbac_role":
		attrs["key"] = key
		attrs["name"] = lookupComputedString("Observed role name. Builtin and custom roles are readable.")
		attrs["description"] = lookupComputedString("Observed role description; an empty string is preserved.")
		attrs["scope_type"] = lookupComputedString("Observed native role scope: `tenant` or `team`.")
		attrs["builtin"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this is a builtin role. Use its stable key for portable configuration; builtin UUIDs belong to an installation."}
		attrs["permission_keys"] = schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Observed explicit native permission keys. Reading a role grants no permissions."}
	case "group":
		attrs["key"], attrs["name"] = key, name
		attrs["description"] = lookupComputedString("Observed group description; an empty string is preserved.")
		attrs["system"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this is a system group, including the derived All Users group. Reads never change membership."}
	case "delivery_channel":
		attrs["id"] = schema.StringAttribute{Required: true, MarkdownDescription: "Known delivery channel UUID, distinct from catalog binding UUIDs. Must be a nonzero canonical lowercase UUID. This data source performs no name search or external target discovery."}
		attrs["display_name"] = lookupComputedString("Observed channel display name.")
		attrs["kind"] = lookupComputedString("Observed channel provider kind.")
		attrs["contract_version"] = lookupComputedString("Observed channel provider contract version.")
		attrs["capabilities"] = schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Observed provider capabilities; empty when the provider is unavailable."}
		attrs["enabled"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "Observed enabled state. Disabled channels remain readable."}
		attrs["unavailable_reason"] = lookupComputedString("Observed unavailability reason, such as `provider_unavailable`; an empty string means no reason was reported. Only safe channel metadata is exposed; no credentials, config or target addresses enter state.")
	}
	resp.Schema = schema.Schema{MarkdownDescription: description, Attributes: attrs}
}

func (d *configurationDataSource) selectorNames() []string {
	switch d.kind {
	case "user":
		return []string{"id", "local_login", "email"}
	case "team", "service":
		return []string{"id", "name"}
	case "rbac_role":
		return []string{"id", "key"}
	case "group":
		return []string{"id", "key", "name"}
	default:
		return []string{"id"}
	}
}

// Configuration is the only source of selector presence: computed values from
// previous state must never become additional configured selectors.
func (d *configurationDataSource) selection(ctx context.Context, config tfsdk.Config, allowUnknown bool, diags *diag.Diagnostics) (field, value, owner string) {
	names := d.selectorNames()
	count, unknown := 0, false
	for _, name := range names {
		var configured types.String
		diags.Append(config.GetAttribute(ctx, path.Root(name), &configured)...)
		if configured.IsNull() {
			continue
		}
		if configured.IsUnknown() {
			unknown = true
			continue
		}
		count++
		field, value = name, configured.ValueString()
		validateLookupValue(name, value, diags)
	}
	if count > 1 || (count == 0 && !unknown) {
		diags.AddError("Invalid OnPager selector", "Configure exactly one of: "+strings.Join(names, ", ")+". Only explicitly configured attributes count as selectors.")
	}
	if d.kind == "service" {
		var team types.String
		diags.Append(config.GetAttribute(ctx, path.Root("team_id"), &team)...)
		if team.IsUnknown() {
			unknown = true
		} else if !team.IsNull() {
			owner = team.ValueString()
			validateLookupValue("team_id", owner, diags)
		}
	}
	if unknown && !allowUnknown {
		diags.AddError("Unknown OnPager selector", "Identity selectors and an expected service team must be known before the data source can read configuration.")
	}
	return field, value, owner
}

func validateLookupValue(name, value string, diags *diag.Diagnostics) {
	if name == "id" || name == "team_id" {
		if !validID(value) {
			diags.AddAttributeError(path.Root(name), "Invalid OnPager UUID", "Use a nonzero canonical lowercase OnPager UUID.")
		}
		return
	}
	if strings.TrimSpace(value) == "" {
		diags.AddAttributeError(path.Root(name), "Blank OnPager selector", "An exact identity selector must not be blank.")
	} else if len(value) > 4096 {
		diags.AddAttributeError(path.Root(name), "OnPager selector exceeds filter limit", "Exact selectors are limited to 4096 UTF-8 bytes. Select this object by its UUID using id instead.")
	}
}

func (d *configurationDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	d.selection(ctx, req.Config, true, &resp.Diagnostics)
}

type userDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	LocalLogin      types.String `tfsdk:"local_login"`
	Email           types.String `tfsdk:"email"`
	EmailNormalized types.String `tfsdk:"email_normalized"`
	DisplayName     types.String `tfsdk:"display_name"`
	Active          types.Bool   `tfsdk:"active"`
}

type roleDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	Key            types.String `tfsdk:"key"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	ScopeType      types.String `tfsdk:"scope_type"`
	Builtin        types.Bool   `tfsdk:"builtin"`
	PermissionKeys types.Set    `tfsdk:"permission_keys"`
}

type groupDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	System      types.Bool   `tfsdk:"system"`
}

type channelDataSourceModel struct {
	ID                types.String `tfsdk:"id"`
	DisplayName       types.String `tfsdk:"display_name"`
	Kind              types.String `tfsdk:"kind"`
	ContractVersion   types.String `tfsdk:"contract_version"`
	Capabilities      types.Set    `tfsdk:"capabilities"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	UnavailableReason types.String `tfsdk:"unavailable_reason"`
}

func (d *configurationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	field, selector, owner := d.selection(ctx, req.Config, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Unconfigured OnPager client", "Configure the OnPager provider before reading this data source.")
		return
	}
	var state any
	switch d.kind {
	case "user":
		v, err := d.client.LookupUser(ctx, field, selector)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read OnPager user", err)
			return
		}
		var m userDataSourceModel
		resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
		m.ID, m.DisplayName, m.EmailNormalized, m.Active = types.StringValue(v.ID), types.StringValue(v.DisplayName), types.StringValue(v.EmailNormalized), types.BoolValue(v.Active)
		state = m
	case "team":
		v, err := d.client.LookupTeam(ctx, field, selector)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read OnPager team", err)
			return
		}
		state = teamState(v)
	case "service":
		v, err := d.client.LookupService(ctx, field, selector, owner)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read OnPager service", err)
			return
		}
		state = serviceState(v)
	case "rbac_role":
		v, err := d.client.LookupRBACRole(ctx, field, selector)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read OnPager role", err)
			return
		}
		permissions, diagnostics := types.SetValueFrom(ctx, types.StringType, v.PermissionKeys)
		resp.Diagnostics.Append(diagnostics...)
		state = roleDataSourceModel{ID: types.StringValue(v.ID), Key: types.StringValue(v.Key), Name: types.StringValue(v.Name), Description: types.StringValue(v.Description), ScopeType: types.StringValue(v.ScopeType), Builtin: types.BoolValue(v.Builtin), PermissionKeys: permissions}
	case "group":
		v, err := d.client.LookupGroup(ctx, field, selector)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read OnPager group", err)
			return
		}
		state = groupDataSourceModel{ID: types.StringValue(v.ID), Key: types.StringValue(v.Key), Name: types.StringValue(v.Name), Description: types.StringValue(v.Description), System: types.BoolValue(v.System)}
	case "delivery_channel":
		v, err := d.client.GetDeliveryChannel(ctx, selector)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "read OnPager delivery channel", err)
			return
		}
		capabilities, diagnostics := types.SetValueFrom(ctx, types.StringType, v.Capabilities)
		resp.Diagnostics.Append(diagnostics...)
		state = channelDataSourceModel{ID: types.StringValue(v.ID), DisplayName: types.StringValue(v.DisplayName), Kind: types.StringValue(v.Kind), ContractVersion: types.StringValue(v.ContractVersion), Capabilities: capabilities, Enabled: types.BoolValue(v.Enabled), UnavailableReason: types.StringValue(v.UnavailableReason)}
	}
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}
