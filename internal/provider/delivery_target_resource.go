package provider

import (
	"context"
	"encoding/json"
	"maps"
	"math/big"
	"slices"
	"strings"

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

type deliveryTargetResource struct{ configuredResource }

type deliveryTargetModel struct {
	ID              types.String `tfsdk:"id"`
	ChannelID       types.String `tfsdk:"channel_id"`
	SnapshotJSON    types.String `tfsdk:"snapshot_json"`
	Enabled         types.Bool   `tfsdk:"enabled"`
	Kind            types.String `tfsdk:"kind"`
	ContractVersion types.String `tfsdk:"contract_version"`
}

func NewDeliveryTargetResource() resource.Resource { return &deliveryTargetResource{} }
func (*deliveryTargetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_delivery_target"
}
func (*deliveryTargetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	channel := uuidAttribute("UUID of the delivery channel. It must be enabled when creating a target. Changing the channel replaces the target.")
	channel.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one delivery target in an existing OnPager delivery channel. Catalog bindings use the target UUID.", Attributes: map[string]schema.Attribute{
		"id":               idAttribute(),
		"channel_id":       channel,
		"snapshot_json":    schema.StringAttribute{Optional: true, Computed: true, Sensitive: true, MarkdownDescription: "Immutable provider-specific destination as a JSON object, typically supplied with `jsonencode`. Required on creation and replacement. Control validates the object for the selected channel and limits the whole encoded request to 8192 bytes. Terraform stores the sensitive configured string in state. Refresh preserves it because the API hides the destination. Omit it after import to manage enabled state. Supplying it after import or changing the destination requests replacement."},
		"enabled":          schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether this target is enabled. Defaults to true. OnPager creates the target enabled. For false, the provider then disables it with a separate update. Disabled targets remain readable and can be re-enabled by UUID. Destroy disables the target and preserves its canonical destination."},
		"kind":             schema.StringAttribute{Computed: true, MarkdownDescription: "Delivery provider kind reported by the channel, such as bot or zvonok."},
		"contract_version": schema.StringAttribute{Computed: true, MarkdownDescription: "Delivery provider contract version reported by the channel."},
	}}
}

func (m deliveryTargetModel) input() client.DeliveryTargetInput {
	return client.DeliveryTargetInput{ChannelID: m.ChannelID.ValueString(), Snapshot: json.RawMessage(m.SnapshotJSON.ValueString())}
}

func (*deliveryTargetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m deliveryTargetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.SnapshotJSON.IsNull() || m.SnapshotJSON.IsUnknown() || m.ChannelID.IsUnknown() || m.ChannelID.IsNull() {
		return
	}
	if err := m.input().Validate(); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("snapshot_json"), "Invalid delivery target configuration", err.Error())
	}
}

// Compare decoded JSON without converting numeric destination IDs to float64.
func equalTargetJSONValues(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		return ok && maps.EqualFunc(x, y, equalTargetJSONValues)
	case []any:
		y, ok := b.([]any)
		return ok && slices.EqualFunc(x, y, equalTargetJSONValues)
	case json.Number:
		y, ok := b.(json.Number)
		if !ok || x == y {
			return ok
		}
		left, okLeft := new(big.Rat).SetString(x.String())
		right, okRight := new(big.Rat).SetString(y.String())
		return okLeft && okRight && left.Cmp(right) == 0
	default:
		return a == b
	}
}

func sameTargetSnapshot(a, b types.String) bool {
	if a.Equal(b) {
		return true
	}
	if a.IsNull() || a.IsUnknown() || b.IsNull() || b.IsUnknown() {
		return false
	}
	decode := func(v types.String) (map[string]any, error) {
		dec := json.NewDecoder(strings.NewReader(v.ValueString()))
		dec.UseNumber()
		var obj map[string]any
		err := dec.Decode(&obj)
		return obj, err
	}
	left, err := decode(a)
	right, otherErr := decode(b)
	return err == nil && otherErr == nil && left != nil && right != nil && maps.EqualFunc(left, right, equalTargetJSONValues)
}

func (*deliveryTargetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	snapshotPath := path.Root("snapshot_json")
	var configured types.String
	var plan deliveryTargetModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, snapshotPath, &configured)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var prior deliveryTargetModel
	creating := req.State.Raw.IsNull()
	if !creating {
		resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if configured.IsNull() {
		if creating || !prior.ChannelID.Equal(plan.ChannelID) {
			resp.Diagnostics.AddAttributeError(snapshotPath, "Destination required for creation", "Supply snapshot_json when creating or replacing a delivery target. An imported target can omit it while managing the same channel.")
		} else {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, snapshotPath, prior.SnapshotJSON)...)
		}
		return
	}
	if !creating && !sameTargetSnapshot(prior.SnapshotJSON, plan.SnapshotJSON) {
		resp.RequiresReplace = append(resp.RequiresReplace, snapshotPath)
	}
}

func (r *deliveryTargetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan deliveryTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Enabled.IsNull() || plan.Enabled.IsUnknown() || plan.SnapshotJSON.IsNull() || plan.SnapshotJSON.IsUnknown() {
		resp.Diagnostics.AddError("Incomplete delivery target configuration", "Supply snapshot_json and a known enabled setting before creating the target.")
		return
	}
	v, err := r.client.CreateDeliveryTarget(ctx, plan.input())
	if err != nil {
		if validID(v.ID) {
			partial := plan
			partial.ID, partial.Kind, partial.ContractVersion = types.StringValue(v.ID), types.StringNull(), types.StringNull()
			partial.Enabled = types.BoolValue(true)
			resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager delivery target", err)
		return
	}
	state := plan
	state.setTarget(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if !resp.Diagnostics.HasError() && !plan.Enabled.ValueBool() {
		r.setEnabled(ctx, state, plan.Enabled, &resp.State, &resp.Diagnostics)
	}
}

func (m *deliveryTargetModel) setTarget(v client.DeliveryTarget) {
	m.ID, m.ChannelID = types.StringValue(v.ID), types.StringValue(v.ChannelID)
	m.Kind, m.ContractVersion = types.StringValue(v.Kind), types.StringValue(v.ContractVersion)
	m.Enabled = types.BoolValue(v.Enabled)
}

func (r *deliveryTargetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state deliveryTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetDeliveryTarget(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager delivery target", err)
		return
	}
	if !state.ChannelID.IsNull() && state.ChannelID.ValueString() != v.ChannelID {
		resp.Diagnostics.AddError("Inconsistent delivery target channel", "The detail endpoint returned a different immutable channel for this target UUID.")
		return
	}
	state.setTarget(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *deliveryTargetResource) setEnabled(ctx context.Context, state deliveryTargetModel, enabled types.Bool, destination *tfsdk.State, diags *diag.Diagnostics) {
	committed, err := r.client.UpdateDeliveryTarget(ctx, state.ID.ValueString(), enabled.ValueBool())
	if committed {
		state.Enabled = enabled
		diags.Append(destination.Set(ctx, state)...)
	}
	if err != nil {
		apiDiagnostic(diags, "update OnPager delivery target", err)
	}
}

func (r *deliveryTargetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state deliveryTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !state.ChannelID.Equal(plan.ChannelID) || !sameTargetSnapshot(state.SnapshotJSON, plan.SnapshotJSON) {
		resp.Diagnostics.AddError("Delivery target requires replacement", "A change to channel_id or the immutable destination must be applied as a replacement.")
		return
	}
	if plan.Enabled.IsNull() || plan.Enabled.IsUnknown() {
		resp.Diagnostics.AddError("Unknown delivery target enabled state", "Resolve enabled before applying the update.")
		return
	}
	state.SnapshotJSON = plan.SnapshotJSON
	if !state.Enabled.Equal(plan.Enabled) {
		r.setEnabled(ctx, state, plan.Enabled, &resp.State, &resp.Diagnostics)
		return
	}
	// Store equivalent configured spelling without submitting any API mutation.
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *deliveryTargetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state deliveryTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DisableDeliveryTarget(ctx, state.ID.ValueString()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "disable OnPager delivery target", err)
	}
}
func (*deliveryTargetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
