package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type routingBindingResource struct {
	configuredResource
	kind, owner string
}

func NewTeamAlertChannelResource() resource.Resource {
	return &routingBindingResource{kind: "team_alert", owner: "team_id"}
}
func NewServiceAlertChannelResource() resource.Resource {
	return &routingBindingResource{kind: "service_alert", owner: "service_id"}
}
func NewTeamOncallChannelResource() resource.Resource {
	return &routingBindingResource{kind: "team_oncall", owner: "team_id"}
}
func (r *routingBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind + "_channel"
}
func (r *routingBindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	channel := uuidAttribute("Explicit existing delivery-channel UUID. Distinct from the catalog binding's `id`.")
	target := uuidAttribute("Explicit existing delivery-target UUID in the selected channel. No discovery or delivery is performed.")
	if r.kind == "team_oncall" {
		channel.MarkdownDescription += " Changing it replaces the binding."
		target.MarkdownDescription += " Changing it replaces the binding."
		channel.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
		target.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	}
	id := idAttribute()
	id.MarkdownDescription = "Stable catalog binding UUID, distinct from the delivery-channel UUID. Import uses owner_uuid/binding_uuid."
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one team or service catalog binding to an existing delivery channel and target.", Attributes: map[string]schema.Attribute{
		"id": id, r.owner: routingOwnerAttribute(r.owner[:len(r.owner)-3]), "delivery_channel_id": channel, "target_id": target, "unavailable": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the retained binding's delivery channel or target is currently unavailable."},
	}}
}

type routingBindingModel struct {
	id, owner, channel, target types.String
	unavailable                types.Bool
}

func bindingRead(ctx context.Context, from routingAttributeReader, owner string) (routingBindingModel, diag.Diagnostics) {
	var m routingBindingModel
	var d diag.Diagnostics
	d.Append(from.GetAttribute(ctx, path.Root("id"), &m.id)...)
	d.Append(from.GetAttribute(ctx, path.Root(owner), &m.owner)...)
	d.Append(from.GetAttribute(ctx, path.Root("delivery_channel_id"), &m.channel)...)
	d.Append(from.GetAttribute(ctx, path.Root("target_id"), &m.target)...)
	d.Append(from.GetAttribute(ctx, path.Root("unavailable"), &m.unavailable)...)
	return m, d
}
func bindingSet(ctx context.Context, state *tfsdk.State, owner, ownerID string, v client.ChannelBinding) diag.Diagnostics {
	var d diag.Diagnostics
	d.Append(state.SetAttribute(ctx, path.Root("id"), v.ID)...)
	d.Append(state.SetAttribute(ctx, path.Root(owner), ownerID)...)
	d.Append(state.SetAttribute(ctx, path.Root("delivery_channel_id"), v.ChannelID)...)
	d.Append(state.SetAttribute(ctx, path.Root("target_id"), v.TargetID)...)
	d.Append(state.SetAttribute(ctx, path.Root("unavailable"), v.Unavailable)...)
	return d
}
func bindingPayload(p routingBindingModel) client.ChannelBindingInput {
	return client.ChannelBindingInput{ChannelID: p.channel.ValueString(), TargetID: p.target.ValueString()}
}
func (r *routingBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	p, d := bindingRead(ctx, &req.Plan, r.owner)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.CreateChannelBinding(ctx, r.kind, p.owner.ValueString(), bindingPayload(p))
	if e != nil {
		// Preserve a known binding UUID from an incomplete successful response.
		// The client does not decode HTTP error bodies into resource fields.
		if validID(v.ID) {
			resp.Diagnostics.Append(bindingPartialSet(ctx, &resp.State, r.owner, p, v)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager channel binding", e)
		return
	}
	resp.Diagnostics.Append(bindingSet(ctx, &resp.State, r.owner, p.owner.ValueString(), v)...)
}
func (r *routingBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	p, d := bindingRead(ctx, &req.State, r.owner)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetChannelBinding(ctx, r.kind, p.owner.ValueString(), p.id.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager channel binding", e)
		return
	}
	resp.Diagnostics.Append(bindingSet(ctx, &resp.State, r.owner, p.owner.ValueString(), v)...)
}
func (r *routingBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	p, d := bindingRead(ctx, &req.Plan, r.owner)
	resp.Diagnostics.Append(d...)
	s, d := bindingRead(ctx, &req.State, r.owner)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.UpdateChannelBinding(ctx, r.kind, s.owner.ValueString(), s.id.ValueString(), bindingPayload(p))
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "replace OnPager channel binding", e)
		return
	}
	resp.Diagnostics.Append(bindingSet(ctx, &resp.State, r.owner, s.owner.ValueString(), v)...)
}
func (r *routingBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	s, d := bindingRead(ctx, &req.State, r.owner)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.DeleteChannelBinding(ctx, r.kind, s.owner.ValueString(), s.id.ValueString()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager channel binding", e)
	}
}
func (r *routingBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	routingScopedImport(ctx, req, resp, r.owner)
}

// Preserve planned values when a successful create omitted their returned fields.
// Unknown computed availability is null until a later direct read can resolve it.
func bindingPartialSet(ctx context.Context, state *tfsdk.State, owner string, p routingBindingModel, v client.ChannelBinding) diag.Diagnostics {
	channel, target := p.channel, p.target
	if validID(v.ChannelID) {
		channel = types.StringValue(v.ChannelID)
	}
	if validID(v.TargetID) {
		target = types.StringValue(v.TargetID)
	}
	availability := types.BoolNull()
	if v.UnavailablePresent {
		availability = types.BoolValue(v.Unavailable)
	}
	var d diag.Diagnostics
	d.Append(state.SetAttribute(ctx, path.Root("id"), v.ID)...)
	d.Append(state.SetAttribute(ctx, path.Root(owner), p.owner)...)
	d.Append(state.SetAttribute(ctx, path.Root("delivery_channel_id"), channel)...)
	d.Append(state.SetAttribute(ctx, path.Root("target_id"), target)...)
	d.Append(state.SetAttribute(ctx, path.Root("unavailable"), availability)...)
	return d
}
