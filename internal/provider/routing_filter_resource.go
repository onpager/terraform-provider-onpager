package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type routingFilterResource struct {
	configuredResource
	kind string
}

func NewTeamAlertFilterResource() resource.Resource { return &routingFilterResource{kind: "team"} }
func NewServiceAlertFilterResource() resource.Resource {
	return &routingFilterResource{kind: "service"}
}
func (r *routingFilterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind + "_alert_filter"
}
func routingOwnerAttribute(owner string) schema.StringAttribute {
	a := uuidAttribute("Owning " + owner + " UUID. Changing it replaces this resource.")
	a.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	return a
}
func routingActiveAttribute() schema.BoolAttribute {
	return schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Desired active state, published atomically with content. Defaults to `true`. Inactive definitions remain readable and in state; a later apply reconciles this setting."}
}
func (r *routingFilterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	id := idAttribute()
	id.MarkdownDescription = "Owning " + r.kind + " UUID, also used as this filter's identity and import ID."
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one team or service CEL alert filter and its active state.", Attributes: map[string]schema.Attribute{
		"id": id, r.kind + "_id": routingOwnerAttribute(r.kind), "cel_source": schema.StringAttribute{Required: true, MarkdownDescription: "CEL filter expression. Sent as typed CEL source, without JSON encoding."}, "active": routingActiveAttribute(), "revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Latest published revision; publication and disable do not use If-Match."},
	}}
}

// Routing owner attribute names follow their exact API scope (team_id/service_id).
// Read individual attributes so the two resource schemas keep those names.
type routingAttributeReader interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}
type routingFilterModel struct {
	owner, id, source types.String
	active            types.Bool
	revision          types.Int64
}

func routingFilterRead(ctx context.Context, from routingAttributeReader, owner string) (routingFilterModel, diag.Diagnostics) {
	var v routingFilterModel
	var d diag.Diagnostics
	d.Append(from.GetAttribute(ctx, path.Root(owner), &v.owner)...)
	d.Append(from.GetAttribute(ctx, path.Root("id"), &v.id)...)
	d.Append(from.GetAttribute(ctx, path.Root("cel_source"), &v.source)...)
	d.Append(from.GetAttribute(ctx, path.Root("active"), &v.active)...)
	d.Append(from.GetAttribute(ctx, path.Root("revision"), &v.revision)...)
	return v, d
}
func routingFilterSet(ctx context.Context, state *tfsdk.State, owner string, v client.AlertFilter) diag.Diagnostics {
	var d diag.Diagnostics
	d.Append(state.SetAttribute(ctx, path.Root("id"), v.TargetID)...)
	d.Append(state.SetAttribute(ctx, path.Root(owner), v.TargetID)...)
	d.Append(state.SetAttribute(ctx, path.Root("cel_source"), v.CELSource)...)
	d.Append(state.SetAttribute(ctx, path.Root("active"), v.Active)...)
	d.Append(state.SetAttribute(ctx, path.Root("revision"), v.Revision)...)
	return d
}
func (r *routingFilterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	p, d := routingFilterRead(ctx, &req.Plan, r.kind+"_id")
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.PublishAlertFilter(ctx, r.kind, p.owner.ValueString(), client.AlertFilterInput{CELSource: p.source.ValueString(), Active: p.active.ValueBool()})
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager alert filter", e)
		return
	}
	resp.Diagnostics.Append(routingFilterSet(ctx, &resp.State, r.kind+"_id", v)...)
}
func (r *routingFilterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	p, d := routingFilterRead(ctx, &req.State, r.kind+"_id")
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetAlertFilter(ctx, r.kind, p.owner.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager alert filter", e)
		return
	}
	resp.Diagnostics.Append(routingFilterSet(ctx, &resp.State, r.kind+"_id", v)...)
}
func (r *routingFilterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	p, d := routingFilterRead(ctx, &req.Plan, r.kind+"_id")
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.PublishAlertFilter(ctx, r.kind, p.owner.ValueString(), client.AlertFilterInput{CELSource: p.source.ValueString(), Active: p.active.ValueBool()})
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager alert filter", e)
		return
	}
	resp.Diagnostics.Append(routingFilterSet(ctx, &resp.State, r.kind+"_id", v)...)
}
func (r *routingFilterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	p, d := routingFilterRead(ctx, &req.State, r.kind+"_id")
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.DisableAlertFilter(ctx, r.kind, p.owner.ValueString()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager alert filter", e)
	}
}
func (r *routingFilterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !validID(req.ID) {
		resp.Diagnostics.AddError("Invalid filter import ID", "Use the owner's canonical nonzero lowercase UUID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(r.kind+"_id"), req.ID)...)
}
