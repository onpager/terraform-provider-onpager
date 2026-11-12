package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type silenceResource struct{ configuredResource }
type silenceModel struct {
	ID              types.String `tfsdk:"id"`
	Scope           types.String `tfsdk:"scope"`
	TeamIDs         types.Set    `tfsdk:"team_ids"`
	Expression      types.String `tfsdk:"expression"`
	Comment         types.String `tfsdk:"comment"`
	StartsAt        types.String `tfsdk:"starts_at"`
	EndsAt          types.String `tfsdk:"ends_at"`
	Revision        types.Int64  `tfsdk:"revision"`
	Status          types.String `tfsdk:"status"`
	RequestTimezone types.String `tfsdk:"request_timezone"`
}

func NewSilenceResource() resource.Resource { return &silenceResource{} }
func (*silenceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_silence"
}
func (*silenceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one silence, including its complete team set, CEL expression and fixed time interval.", Attributes: map[string]schema.Attribute{
		"id": idAttribute(), "request_timezone": temporalTimezoneAttribute(),
		"scope":      schema.StringAttribute{Required: true, MarkdownDescription: "Immutable scope: `team` or `tenant`. Changing it replaces the whole silence. Tenant scope affects the whole tenant and requires an empty team set.", Validators: []validator.String{enumRule{"team", "tenant"}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"team_ids":   schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, nil)), MarkdownDescription: "Complete set of canonical team UUIDs. Nonempty for team scope; empty for tenant scope (default). A multiteam silence is one shared UUID. Updates replace the full set and require update authority in every current team plus create authority for newly added active teams."},
		"expression": schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty CEL predicate, at most 4096 UTF-8 bytes. Use canonical text without leading/trailing whitespace or NUL. The backend validates and compiles the expression."},
		"comment":    schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Canonical comment, at most 4096 UTF-8 bytes, without leading/trailing whitespace or NUL. Omission or null defaults to an empty string; explicit empty clears the saved comment."},
		"starts_at":  schema.StringAttribute{Required: true, MarkdownDescription: "Fixed inclusive start, local `YYYY-MM-DDTHH:mm:ss` or RFC3339 with an explicit offset, at most six fractional digits. Must precede ends_at. Local input is sent unchanged for backend timezone/DST validation. No relative duration or current-time default is used. Equivalent configured instants retain their spelling in state."},
		"ends_at":    schema.StringAttribute{Required: true, MarkdownDescription: "Fixed exclusive end, local `YYYY-MM-DDTHH:mm:ss` or RFC3339 with an explicit offset, at most six fractional digits. Must follow starts_at. Local input is interpreted in the provider request timezone; the backend rejects ambiguous/nonexistent local times. Changing an explicit provider timezone plans an update for local input."},
		"revision":   schema.Int64Attribute{Computed: true, MarkdownDescription: "This silence's own numeric revision, used in strong `If-Match` for update and DELETE. Stale writes report conflicts and retain state; concurrent changes are never overwritten automatically."},
		"status":     schema.StringAttribute{Computed: true, MarkdownDescription: "Observed API status: `scheduled`, `active`, `expired` or `cancelled`. Expired and cancelled objects remain in state. Operational cancellation is terminal and cannot be undone through an update; explicitly replace the resource if a new silence is desired."},
	}}
}
func silenceInput(ctx context.Context, m silenceModel, partial bool) (client.SilenceInput, error) {
	in := client.SilenceInput{Scope: m.Scope.ValueString(), TeamIDs: []string{}, Expression: m.Expression.ValueString(), Comment: m.Comment.ValueString(), StartsAt: m.StartsAt.ValueString(), EndsAt: m.EndsAt.ValueString()}
	for name, v := range map[string]types.String{"scope": m.Scope, "expression": m.Expression, "starts_at": m.StartsAt, "ends_at": m.EndsAt} {
		if !partial && (v.IsNull() || v.IsUnknown()) {
			return in, fmt.Errorf("%s must be known and non-null", name)
		}
	}
	if !m.Scope.IsUnknown() && !m.Scope.IsNull() && in.Scope != "team" && in.Scope != "tenant" {
		return in, errors.New("scope must be team or tenant")
	}
	for name, v := range map[string]types.String{"expression": m.Expression, "comment": m.Comment} {
		if v.IsUnknown() {
			continue
		}
		value := v.ValueString()
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > 4096 || strings.TrimSpace(value) != value || name == "expression" && !v.IsNull() && value == "" {
			return in, fmt.Errorf("%s must contain canonical trimmed UTF-8 text of at most 4096 bytes without NUL", name)
		}
	}
	if !partial && (m.Comment.IsUnknown() || m.TeamIDs.IsUnknown()) {
		return in, errors.New("comment and team_ids must be known")
	}
	unknownTeams := m.TeamIDs.IsUnknown()
	if !m.TeamIDs.IsNull() && !m.TeamIDs.IsUnknown() {
		for _, value := range m.TeamIDs.Elements() {
			if value.IsUnknown() && partial {
				unknownTeams = true
				continue
			}
			id, ok := value.(types.String)
			if !ok || id.IsNull() || id.IsUnknown() || !validID(id.ValueString()) {
				return in, errors.New("team_ids must contain known canonical nonzero UUIDs")
			}
			in.TeamIDs = append(in.TeamIDs, id.ValueString())
		}
	}
	if !m.Scope.IsUnknown() && !unknownTeams {
		if in.Scope == "team" && len(in.TeamIDs) == 0 || in.Scope == "tenant" && len(in.TeamIDs) > 0 {
			return in, errors.New("team scope requires a nonempty team_ids set; tenant scope requires an empty set")
		}
	}
	for name, v := range map[string]types.String{"starts_at": m.StartsAt, "ends_at": m.EndsAt} {
		if v.IsUnknown() || v.IsNull() {
			continue
		}
		at, _, e := temporalParse(v.ValueString())
		if e != nil || !workCalendarInstantPattern.MatchString(v.ValueString()) || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
			return in, fmt.Errorf("%s must use a finite local datetime or RFC3339 with at most microsecond precision", name)
		}
	}
	if !m.StartsAt.IsUnknown() && !m.EndsAt.IsUnknown() && !m.StartsAt.IsNull() && !m.EndsAt.IsNull() {
		a, ae, _ := temporalParse(in.StartsAt)
		b, be, _ := temporalParse(in.EndsAt)
		if ae == be && !a.Before(b) {
			return in, errors.New("starts_at must precede ends_at")
		}
	}
	return in, nil
}
func silenceState(ctx context.Context, prior silenceModel, v client.Silence, b temporalBindings, written bool) (silenceModel, error) {
	teams, d := types.SetValueFrom(ctx, types.StringType, v.TeamIDs)
	if d.HasError() {
		return prior, errors.New("could not store silence teams")
	}
	out := silenceModel{ID: types.StringValue(v.ID), Scope: types.StringValue(v.Scope), TeamIDs: teams, Expression: types.StringValue(v.Expression), Comment: types.StringValue(v.Comment), Revision: types.Int64Value(v.Revision), Status: types.StringValue(v.Status), RequestTimezone: prior.RequestTimezone}
	out.StartsAt = b.value("StartsAt", prior.StartsAt, v.StartsAt, written)
	out.EndsAt = b.value("EndsAt", prior.EndsAt, v.EndsAt, written)
	return out, nil
}
func (r *silenceResource) read(ctx context.Context, m silenceModel, b temporalBindings, written bool) (silenceModel, error) {
	v, e := r.client.GetSilence(ctx, m.ID.ValueString())
	if e != nil {
		return m, e
	}
	if written && v.Revision != m.Revision.ValueInt64() {
		return m, errors.New("OnPager silence changed concurrently after the committed write; the committed identity and revision remain in state. Run a fresh plan before retrying")
	}
	return silenceState(ctx, m, v, b, written)
}
func (r *silenceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan silenceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, e := silenceInput(ctx, plan, false)
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager silence", e)
		return
	}
	v, e := r.client.CreateSilence(ctx, in)
	if v.Committed && validID(v.ID) {
		plan.ID = types.StringValue(v.ID)
		plan.Revision = types.Int64Null()
		if v.Revision > 0 {
			plan.Revision = types.Int64Value(v.Revision)
		}
		plan.Status = types.StringNull()
		if v.Status != "" {
			plan.Status = types.StringValue(v.Status)
		}
		plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), plan.StartsAt, plan.EndsAt)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		if resp.Private != nil {
			temporalBindings{}.store(ctx, resp.Private, &resp.Diagnostics)
		}
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager silence", e)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	b := temporalBindings{}
	next, e := r.read(ctx, plan, b, true)
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read newly created OnPager silence", e)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		b.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *silenceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state silenceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b := temporalBindings{}
	if req.Private != nil {
		b = temporalLoad(ctx, req.Private, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	next, e := r.read(ctx, state, b, false)
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager silence", e)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		b.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *silenceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state silenceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Status.ValueString() == "cancelled" {
		resp.Diagnostics.AddError("Cancelled OnPager silence cannot be edited", "Operational cancellation is terminal. Explicitly replace this Terraform resource to create a new silence; no restore or implicit replacement is performed.")
		return
	}
	in, e := silenceInput(ctx, plan, false)
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager silence", e)
		return
	}
	v, e := r.client.UpdateSilence(ctx, state.ID.ValueString(), state.Revision.ValueInt64(), in)
	if v.Committed {
		plan.ID = state.ID
		plan.Revision = types.Int64Null()
		if v.Revision > 0 {
			plan.Revision = types.Int64Value(v.Revision)
		}
		plan.Status = state.Status
		plan.RequestTimezone = temporalRequestTimezone(r.client.Timezone(), plan.StartsAt, plan.EndsAt)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		if resp.Private != nil {
			temporalBindings{}.store(ctx, resp.Private, &resp.Diagnostics)
		}
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager silence", e)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	b := temporalBindings{}
	next, e := r.read(ctx, plan, b, true)
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read updated OnPager silence", e)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	if resp.Private != nil {
		b.store(ctx, resp.Private, &resp.Diagnostics)
	}
}
func (r *silenceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state silenceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.DeleteSilence(ctx, state.ID.ValueString(), state.Revision.ValueInt64()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager silence", e)
	}
}
func (*silenceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !validID(req.ID) {
		resp.Diagnostics.AddError("Invalid silence import ID", "Use the canonical silence UUID; a multiteam silence is one whole resource.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
func (*silenceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config silenceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, e := silenceInput(ctx, config, true); e != nil {
		apiDiagnostic(&resp.Diagnostics, "configure OnPager silence", e)
	}
}
func (r *silenceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var m silenceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone := temporalRequestTimezone(r.client.Timezone(), m.StartsAt, m.EndsAt)
	temporalPlanTimezone(ctx, req, resp, zone, "revision")
	if req.State.Raw.Type() == nil || req.State.Raw.IsNull() || resp.Diagnostics.HasError() {
		return
	}
	var previous types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("request_timezone"), &previous)...)
	if !resp.Diagnostics.HasError() && !previous.Equal(zone) {
		// Reinterpreting the interval can change its observed status. Framework
		// unknown marking ran before this provider-only change introduced a write.
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	}
}
