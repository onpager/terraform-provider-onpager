package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type oidcGroupMappingResource struct{ configuredResource }
type groupMappingModel struct {
	ID           types.String `tfsdk:"id"`
	GroupID      types.String `tfsdk:"group_id"`
	ProviderSlug types.String `tfsdk:"provider_slug"`
	Issuer       types.String `tfsdk:"issuer"`
	ClientID     types.String `tfsdk:"client_id"`
	ExternalKey  types.String `tfsdk:"external_key"`
	Revision     types.Int64  `tfsdk:"revision"`
}

func NewOIDCGroupMappingResource() resource.Resource { return &oidcGroupMappingResource{} }
func (*oidcGroupMappingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_oidc_group_mapping"
}
func (*oidcGroupMappingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one local OIDC-to-group mapping for an existing group.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "group_id": groupOwnerAttribute(),
		"provider_slug": schema.StringAttribute{Required: true, MarkdownDescription: "Literal deployment provider slug: 1–64 letters, digits, `_` or `-`. Updates replace this mapping in place; no IdP lookup is performed.", Validators: []validator.String{groupStringRule{kind: "provider"}}},
		"issuer":        schema.StringAttribute{Required: true, MarkdownDescription: "Nonblank literal OIDC issuer without NUL. Case and whitespace are preserved. Must match the configured deployment binding for eligibility.", Validators: []validator.String{groupStringRule{kind: "issuer"}}},
		"client_id":     schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty literal OIDC client ID without NUL. Case and whitespace are preserved; null and empty values are invalid.", Validators: []validator.String{groupStringRule{kind: "literal"}}},
		"external_key":  schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty external group key without NUL. Exact case and whitespace are significant and preserved. No external group discovery or membership mutation is performed.", Validators: []validator.String{groupStringRule{kind: "literal"}}},
		"revision":      schema.Int64Attribute{Computed: true, MarkdownDescription: "Independent mapping revision used in strong numeric If-Match for replacement and deletion. Parent and other child revisions are never used."},
	}}
}
func groupMappingPayload(m groupMappingModel) client.OIDCGroupMappingInput {
	return client.OIDCGroupMappingInput{ProviderSlug: m.ProviderSlug.ValueString(), Issuer: m.Issuer.ValueString(), ClientID: m.ClientID.ValueString(), ExternalKey: m.ExternalKey.ValueString()}
}
func groupMappingState(v client.OIDCGroupMapping) groupMappingModel {
	return groupMappingModel{ID: types.StringValue(v.ID), GroupID: types.StringValue(v.GroupID), ProviderSlug: types.StringValue(v.ProviderSlug), Issuer: types.StringValue(v.Issuer), ClientID: types.StringValue(v.ClientID), ExternalKey: types.StringValue(v.ExternalKey), Revision: types.Int64Value(v.Revision)}
}
func (r *oidcGroupMappingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p groupMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.CreateOIDCGroupMapping(ctx, p.GroupID.ValueString(), groupMappingPayload(p))
	if e != nil {
		if validID(v.ID) && (v.GroupID == "" || v.GroupID == p.GroupID.ValueString()) {
			p.ID = types.StringValue(v.ID)
			p.Revision = types.Int64Null()
			if v.Revision > 0 {
				p.Revision = types.Int64Value(v.Revision)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager OIDC group mapping", e)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupMappingState(v))...)
}
func (r *oidcGroupMappingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s groupMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetOIDCGroupMapping(ctx, s.GroupID.ValueString(), s.ID.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager OIDC group mapping", e)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupMappingState(v))...)
}
func (r *oidcGroupMappingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s groupMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.UpdateOIDCGroupMapping(ctx, s.GroupID.ValueString(), s.ID.ValueString(), s.Revision.ValueInt64(), groupMappingPayload(p))
	if e != nil {
		if v.ID == s.ID.ValueString() && (v.GroupID == "" || v.GroupID == s.GroupID.ValueString()) && v.Revision > 0 {
			p.ID = s.ID
			p.GroupID = s.GroupID
			p.Revision = types.Int64Value(v.Revision)
			resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
		}
		apiDiagnostic(&resp.Diagnostics, "replace OnPager OIDC group mapping", e)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupMappingState(v))...)
}
func (r *oidcGroupMappingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s groupMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.DeleteOIDCGroupMapping(ctx, s.GroupID.ValueString(), s.ID.ValueString(), s.Revision.ValueInt64()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager OIDC group mapping", e)
	}
}
func (*oidcGroupMappingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	routingScopedImport(ctx, req, resp, "group_id")
}
