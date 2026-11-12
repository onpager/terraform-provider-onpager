package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type teamResource struct{ configuredResource }
type teamModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Active types.Bool   `tfsdk:"active"`
}

func NewTeamResource() resource.Resource { return &teamResource{} }
func (*teamResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team"
}
func (*teamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage an OnPager team and its active state.", Attributes: entityAttributes()}
}
func teamState(v client.Team) teamModel {
	return teamModel{ID: types.StringValue(v.ID), Name: types.StringValue(v.Name), Active: types.BoolValue(v.Active)}
}
func (r *teamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan teamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.client.CreateTeam(ctx, plan.Name.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager team", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamState(value))...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Active.ValueBool() {
		if err := r.client.DisableTeam(ctx, value.ID); err != nil {
			apiDiagnostic(&resp.Diagnostics, "disable newly created OnPager team", err)
			return
		}
		value.Active = false
		resp.Diagnostics.Append(resp.State.Set(ctx, teamState(value))...)
		value, err = r.client.GetTeam(ctx, value.ID)
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "refresh created OnPager team", err)
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, teamState(value))...)
	}
}
func (r *teamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state teamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.client.GetTeam(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager team", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamState(value))...)
}
func (r *teamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan teamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !state.Name.Equal(plan.Name) {
		_, err := r.client.RenameTeam(ctx, state.ID.ValueString(), plan.Name.ValueString())
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "update OnPager team", err)
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
			err = r.client.ReactivateTeam(ctx, state.ID.ValueString())
		} else {
			err = r.client.DisableTeam(ctx, state.ID.ValueString())
		}
		if err != nil {
			apiDiagnostic(&resp.Diagnostics, "change OnPager team active state", err)
			return
		}
		state.Active = plan.Active
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	value, err := r.client.GetTeam(ctx, state.ID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "refresh updated OnPager team", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamState(value))...)
}
func (r *teamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state teamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DisableTeam(ctx, state.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager team", err)
	}
}
func (*teamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
