package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type teamRoleDefaultsResource struct{ configuredResource }

type teamRoleDefaultsModel struct {
	ID       types.String `tfsdk:"id"`
	Members  types.Set    `tfsdk:"members"`
	Leads    types.Set    `tfsdk:"leads"`
	Revision types.Int64  `tfsdk:"revision"`
}

func NewTeamRoleDefaultsResource() resource.Resource { return &teamRoleDefaultsResource{} }
func (*teamRoleDefaultsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_role_defaults"
}
func (*teamRoleDefaultsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage the tenant's complete live team member and additional lead role policy. Configure one resource per tenant. Destroy clears both sets and removes these dynamic grants tenant-wide.", Attributes: map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Stable singleton identity and import ID: `tenant`."},
		"revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Current positive whole-policy revision, validated against its strong numeric ETag and used in If-Match for replacement and destroy. Independent of role and assignment revisions. Stale revisions return HTTP 412; run a new plan before retrying."},
		"members":  schema.SetAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Complete set of team-scope role UUIDs granted live to all current and future active team members, including leads, in each member's team. Builtin or custom roles must already exist. Explicit [] is valid and grants no member roles; omitted/null and null elements are invalid. Unknown UUIDs are deferred until apply; UUIDs must be nonzero canonical lowercase values.", Validators: []validator.Set{teamRoleDefaultsSetRule{}}},
		"leads":    schema.SetAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Complete set of additional team-scope role UUIDs granted live to active team leads in their teams. Leads also receive members roles. Explicit [] is valid and grants no additional lead roles; omitted/null and null elements are invalid. Unknown UUIDs are deferred until apply; UUIDs must be nonzero canonical lowercase values.", Validators: []validator.Set{teamRoleDefaultsSetRule{}}},
	}}
}

type teamRoleDefaultsSetRule struct{}

func (teamRoleDefaultsSetRule) Description(context.Context) string {
	return "An explicit set of nonzero canonical lowercase role UUIDs; [] is valid, null and null elements are invalid."
}
func (v teamRoleDefaultsSetRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v teamRoleDefaultsSetRule) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() {
		resp.Diagnostics.AddAttributeError(req.Path, "Missing team role set", v.Description(ctx))
		return
	}
	if req.ConfigValue.IsUnknown() {
		return
	}
	for _, element := range req.ConfigValue.Elements() {
		id, ok := element.(types.String)
		if !ok || id.IsNull() || !id.IsUnknown() && !validID(id.ValueString()) {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid team role UUID", v.Description(ctx))
			return
		}
	}
}

func teamRoleDefaultsPayload(m teamRoleDefaultsModel) (client.TeamRoleDefaultsInput, error) {
	out := client.TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}}
	for i, set := range []types.Set{m.Members, m.Leads} {
		if set.IsNull() || set.IsUnknown() {
			return out, errors.New("members and leads must be explicit known UUID sets before apply; use [] for an empty set")
		}
		ids := make([]string, 0, len(set.Elements()))
		for _, element := range set.Elements() {
			id, ok := element.(types.String)
			if !ok || id.IsNull() || id.IsUnknown() || !validID(id.ValueString()) {
				return out, errors.New("members and leads must contain only known nonzero canonical lowercase role UUIDs before apply")
			}
			ids = append(ids, id.ValueString())
		}
		if i == 0 {
			out.Members = ids
		} else {
			out.Leads = ids
		}
	}
	return out, nil
}

func teamRoleDefaultsState(ctx context.Context, v client.TeamRoleDefaults) teamRoleDefaultsModel {
	// The client validated complete string arrays before this conversion.
	members, _ := types.SetValueFrom(ctx, types.StringType, v.Members)
	leads, _ := types.SetValueFrom(ctx, types.StringType, v.Leads)
	return teamRoleDefaultsModel{ID: types.StringValue("tenant"), Members: members, Leads: leads, Revision: types.Int64Value(v.Revision)}
}
func teamRoleDefaultsReceipt(m teamRoleDefaultsModel, v client.TeamRoleDefaults) teamRoleDefaultsModel {
	m.ID = types.StringValue("tenant")
	m.Revision = types.Int64Null()
	if v.Revision > 0 {
		m.Revision = types.Int64Value(v.Revision)
	}
	return m
}
func teamRoleDefaultsSavedRevision(m teamRoleDefaultsModel) (int64, error) {
	if m.ID.IsNull() || m.ID.IsUnknown() || m.ID.ValueString() != "tenant" || m.Revision.IsNull() || m.Revision.IsUnknown() || m.Revision.ValueInt64() <= 0 {
		return 0, errors.New("refresh the tenant team role defaults to obtain their own current positive revision before retrying")
	}
	return m.Revision.ValueInt64(), nil
}

func (r *teamRoleDefaultsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p teamRoleDefaultsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := teamRoleDefaultsPayload(p)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager team role defaults", err)
		return
	}
	current, err := r.client.GetTeamRoleDefaults(ctx)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read unowned OnPager team role defaults", err)
		return
	}
	if len(current.Members) > 0 || len(current.Leads) > 0 {
		resp.Diagnostics.AddError("Existing team role defaults require import", "The tenant policy is nonempty and is not owned by this resource. Import it with the literal ID `tenant` before applying, even if the configured sets match. This resource owns both complete sets and never merges existing grants during create.")
		return
	}
	if len(in.Members) == 0 && len(in.Leads) == 0 {
		resp.Diagnostics.Append(resp.State.Set(ctx, teamRoleDefaultsState(ctx, current))...)
		return
	}
	v, err := r.client.ReplaceTeamRoleDefaults(ctx, current.Revision, in)
	if v.Committed {
		resp.Diagnostics.Append(resp.State.Set(ctx, teamRoleDefaultsReceipt(p, v))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "publish initial OnPager team role defaults", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamRoleDefaultsState(ctx, v))...)
}
func (r *teamRoleDefaultsResource) Read(ctx context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	v, err := r.client.GetTeamRoleDefaults(ctx)
	if err != nil {
		// This always-existing policy has no absence/404 lifecycle.
		apiDiagnostic(&resp.Diagnostics, "read OnPager team role defaults", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamRoleDefaultsState(ctx, v))...)
}
func (r *teamRoleDefaultsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s teamRoleDefaultsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := teamRoleDefaultsPayload(p)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager team role defaults", err)
		return
	}
	revision, err := teamRoleDefaultsSavedRevision(s)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager team role defaults", err)
		return
	}
	v, err := r.client.ReplaceTeamRoleDefaults(ctx, revision, in)
	if v.Committed {
		resp.Diagnostics.Append(resp.State.Set(ctx, teamRoleDefaultsReceipt(p, v))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager team role defaults", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, teamRoleDefaultsState(ctx, v))...)
}
func (r *teamRoleDefaultsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s teamRoleDefaultsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	revision, err := teamRoleDefaultsSavedRevision(s)
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "clear OnPager team role defaults", err)
		return
	}
	v, err := r.client.ReplaceTeamRoleDefaults(ctx, revision, client.TeamRoleDefaultsInput{Members: []string{}, Leads: []string{}})
	if v.Committed && err != nil {
		// Keep the committed empty intent even if the response cannot supply a
		// usable own revision. A subsequent refresh can recover authority safely.
		s.Members, s.Leads = types.SetValueMust(types.StringType, nil), types.SetValueMust(types.StringType, nil)
		resp.Diagnostics.Append(resp.State.Set(ctx, teamRoleDefaultsReceipt(s, v))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "clear OnPager team role defaults", err)
	}
}
func (*teamRoleDefaultsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != "tenant" {
		resp.Diagnostics.AddError("Invalid team role defaults import ID", "Use the literal ID `tenant`; this resource owns the tenant's complete live member and lead role policy.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
