package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type serviceResource struct{ configuredResource }
type serviceModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	TeamID types.String `tfsdk:"team_id"`
	Active types.Bool   `tfsdk:"active"`
}

func NewServiceResource() resource.Resource { return &serviceResource{} }
func (*serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}
func (*serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := entityAttributes()
	attributes["team_id"] = uuidAttribute("Owning team UUID. Changes transfer the existing service without replacing its UUID.")
	resp.Schema = schema.Schema{MarkdownDescription: "Manage an OnPager service, its owner team and active state.", Attributes: attributes}
}
func serviceState(v client.Service) serviceModel {
	return serviceModel{ID: types.StringValue(v.ID), Name: types.StringValue(v.Name), TeamID: types.StringValue(v.TeamID), Active: types.BoolValue(v.Active)}
}
func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.client.CreateService(ctx, plan.TeamID.ValueString(), plan.Name.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager service", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceState(value))...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Active.ValueBool() {
		if err := r.client.DisableService(ctx, value.ID); err != nil {
			apiDiagnostic(&resp.Diagnostics, "disable newly created OnPager service", err)
			return
		}
		value.Active = false
		resp.Diagnostics.Append(resp.State.Set(ctx, serviceState(value))...)
		value, err = r.client.GetService(ctx, value.ID)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "refresh created OnPager service", err)
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, serviceState(value))...)
	}
}
func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.client.GetService(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager service", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceState(value))...)
}
func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !state.TeamID.Equal(plan.TeamID) {
		_, err := r.client.TransferService(ctx, state.ID.ValueString(), plan.TeamID.ValueString())
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "transfer OnPager service", err)
			return
		}
		state.TeamID = plan.TeamID
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !state.Name.Equal(plan.Name) {
		_, err := r.client.RenameService(ctx, state.ID.ValueString(), plan.Name.ValueString())
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "rename OnPager service", err)
			return
		}
		state.Name = plan.Name
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !state.Active.Equal(plan.Active) {
		var err error
		if plan.Active.ValueBool() {
			err = r.client.ReactivateService(ctx, state.ID.ValueString())
		} else {
			err = r.client.DisableService(ctx, state.ID.ValueString())
		}
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "change OnPager service active state", err)
			return
		}
		state.Active = plan.Active
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	value, err := r.client.GetService(ctx, state.ID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "refresh updated OnPager service", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceState(value))...)
}
func (r *serviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DisableService(ctx, state.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager service", err)
	}
}
func (*serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
