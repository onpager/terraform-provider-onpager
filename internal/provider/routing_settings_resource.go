package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type routingSettingsResource struct{ configuredResource }
type routingSettingsModel struct {
	ID           types.String `tfsdk:"id"`
	DefaultRoute types.Object `tfsdk:"default_route"`
}

var routingSettingsRouteTypes = map[string]attr.Type{"target_kind": types.StringType, "target_id": types.StringType}

func NewRoutingSettingsResource() resource.Resource { return &routingSettingsResource{} }
func (*routingSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_routing_settings"
}
func (*routingSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage the tenant's complete default routing recipient for alerts unmatched by all team/service filters.", Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Singleton identity and import ID: literal tenant. Configure only one owner per tenant.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"default_route": schema.SingleNestedAttribute{Optional: true, MarkdownDescription: "Complete fallback recipient. Used after enrichment only when no team or service filter matched. Omission or null clears the default; unmatched alerts then remain unassigned. Setting a service also assigns its owner team. The target must be active in this tenant when written; saved inactive targets remain readable.", Attributes: map[string]schema.Attribute{
			"target_kind": schema.StringAttribute{Required: true, MarkdownDescription: "Recipient kind: team or service.", Validators: []validator.String{enumRule{"team", "service"}}},
			"target_id":   uuidAttribute("Known team or service UUID; names are not looked up and targets are not discovered."),
		}},
	}}
}
func routingSettingsInput(m routingSettingsModel) (client.RoutingSettings, error) {
	var out client.RoutingSettings
	if m.DefaultRoute.IsUnknown() {
		return out, errors.New("default_route must be known before apply")
	}
	if m.DefaultRoute.IsNull() {
		return out, nil
	}
	fields := m.DefaultRoute.Attributes()
	kind, ok := fields["target_kind"].(types.String)
	id, idOK := fields["target_id"].(types.String)
	if !ok || !idOK || kind.IsNull() || kind.IsUnknown() || id.IsNull() || id.IsUnknown() || !validID(id.ValueString()) || kind.ValueString() != "team" && kind.ValueString() != "service" {
		return out, errors.New("default_route requires a known team/service target_kind and canonical nonzero target_id UUID")
	}
	out.DefaultRoute = &client.DefaultRoute{TargetKind: kind.ValueString(), TargetID: id.ValueString()}
	return out, nil
}
func routingSettingsState(v client.RoutingSettings) routingSettingsModel {
	route := types.ObjectNull(routingSettingsRouteTypes)
	if v.DefaultRoute != nil {
		route = types.ObjectValueMust(routingSettingsRouteTypes, map[string]attr.Value{"target_kind": types.StringValue(v.DefaultRoute.TargetKind), "target_id": types.StringValue(v.DefaultRoute.TargetID)})
	}
	return routingSettingsModel{ID: types.StringValue("tenant"), DefaultRoute: route}
}
func (r *routingSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan routingSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := routingSettingsInput(plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager routing settings", err)
		return
	}
	current, err := r.client.GetRoutingSettings(ctx)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read unowned OnPager routing settings", err)
		return
	}
	if current.DefaultRoute != nil {
		resp.Diagnostics.AddError("Existing routing settings require import", "The tenant already has a default route. Import the literal ID tenant before applying, even if the configured recipient matches. This resource owns the complete default route and never adopts unowned configuration implicitly.")
		return
	}
	if in.DefaultRoute == nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, routingSettingsState(current))...)
		return
	}
	v, err := r.client.UpdateRoutingSettings(ctx, in)
	if v.Committed {
		resp.Diagnostics.Append(resp.State.Set(ctx, routingSettingsState(in))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "save initial OnPager routing settings", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, routingSettingsState(v))...)
}
func (r *routingSettingsResource) Read(ctx context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	v, err := r.client.GetRoutingSettings(ctx)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager routing settings", err)
		return
	}
	// An empty route is owned settings, not absence. This singleton has no 404
	// lifecycle; all HTTP failures must retain its saved state.
	resp.Diagnostics.Append(resp.State.Set(ctx, routingSettingsState(v))...)
}
func (r *routingSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan routingSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := routingSettingsInput(plan)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager routing settings", err)
		return
	}
	v, err := r.client.UpdateRoutingSettings(ctx, in)
	if v.Committed {
		resp.Diagnostics.Append(resp.State.Set(ctx, routingSettingsState(in))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager routing settings", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, routingSettingsState(v))...)
}
func (r *routingSettingsResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	v, err := r.client.UpdateRoutingSettings(ctx, client.RoutingSettings{})
	if v.Committed && err != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, routingSettingsState(client.RoutingSettings{}))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "clear OnPager default route", err)
	}
}
func (*routingSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != "tenant" {
		resp.Diagnostics.AddError("Invalid routing settings import ID", "Use the literal singleton ID tenant.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "tenant")...)
}
