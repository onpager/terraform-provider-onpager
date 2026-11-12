package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type rbacPermissionsDataSource struct{ client *client.Client }

var (
	_ datasource.DataSource              = (*rbacPermissionsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*rbacPermissionsDataSource)(nil)
)

var rbacPermissionObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name": types.StringType, "description": types.StringType, "section": types.StringType,
	"scopes": types.SetType{ElemType: types.StringType}, "actors": types.SetType{ElemType: types.StringType},
	"target": types.StringType, "sensitive": types.BoolType, "custom_grantable": types.BoolType,
}}

func NewRBACPermissionsDataSource() datasource.DataSource { return &rbacPermissionsDataSource{} }

func (*rbacPermissionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rbac_permissions"
}

func (*rbacPermissionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Read the complete native permission catalog as public metadata. Reading the catalog grants no permissions and does not describe any principal's effective access.", Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Literal catalog identity: `catalog`. This data source has no selectors."},
		"permissions": schema.MapNestedAttribute{Computed: true, MarkdownDescription: "Complete catalog keyed by each exact native permission key. An empty API array produces an empty map. Future permission keys and sections are preserved without a provider allowlist.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":             schema.StringAttribute{Computed: true, MarkdownDescription: "API permission name."},
			"description":      schema.StringAttribute{Computed: true, MarkdownDescription: "API permission description; an empty string is preserved."},
			"section":          schema.StringAttribute{Computed: true, MarkdownDescription: "API catalog section, including sections introduced by later catalog releases."},
			"scopes":           schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Compatible role scopes: `tenant` and/or `team`. This metadata assigns no scope or grant."},
			"actors":           schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Compatible actor kinds: `user` and/or `service_account`. Absence of `service_account` identifies a permission unavailable to machine roles."},
			"target":           schema.StringAttribute{Computed: true, MarkdownDescription: "Authorization target kind: `tenant`, `team` or `self`."},
			"sensitive":        schema.BoolAttribute{Computed: true, MarkdownDescription: "Public metadata flag identifying a sensitive action. This boolean is not a secret and is not marked sensitive in Terraform."},
			"custom_grantable": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the API permits this permission in a custom role, subject to its scope and actor constraints. A true value grants no authority."},
		}}},
	}}
}

func (d *rbacPermissionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *rbacPermissionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Unconfigured OnPager client", "Configure the OnPager provider before reading the permission catalog.")
		return
	}
	permissions, err := d.client.ListRBACPermissions(ctx)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager permission catalog", err)
		return
	}
	values := make(map[string]attr.Value, len(permissions))
	for _, permission := range permissions {
		scopes, diagnostics := types.SetValueFrom(ctx, types.StringType, permission.Scopes)
		resp.Diagnostics.Append(diagnostics...)
		actors, diagnostics := types.SetValueFrom(ctx, types.StringType, permission.Actors)
		resp.Diagnostics.Append(diagnostics...)
		value, diagnostics := types.ObjectValue(rbacPermissionObjectType.AttrTypes, map[string]attr.Value{
			"name": types.StringValue(permission.Name), "description": types.StringValue(permission.Description),
			"section": types.StringValue(permission.Section), "scopes": scopes, "actors": actors,
			"target": types.StringValue(permission.Target), "sensitive": types.BoolValue(permission.Sensitive),
			"custom_grantable": types.BoolValue(permission.CustomGrantable),
		})
		resp.Diagnostics.Append(diagnostics...)
		values[permission.Key] = value
	}
	metadata, diagnostics := types.MapValue(rbacPermissionObjectType, values)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	state := struct {
		ID          types.String `tfsdk:"id"`
		Permissions types.Map    `tfsdk:"permissions"`
	}{ID: types.StringValue("catalog"), Permissions: metadata}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
