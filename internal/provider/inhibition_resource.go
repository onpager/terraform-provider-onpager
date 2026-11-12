package provider

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type inhibitionRuleResource struct{ configuredResource }
type inhibitionRuleModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	SourceExpression     types.String `tfsdk:"source_expression"`
	TargetExpression     types.String `tfsdk:"target_expression"`
	Matchers             types.Set    `tfsdk:"matchers"`
	ResolvedTargetAction types.String `tfsdk:"resolved_target_action"`
	Enabled              types.Bool   `tfsdk:"enabled"`
	Revision             types.Int64  `tfsdk:"revision"`
	IndexState           types.String `tfsdk:"index_state"`
}
type inhibitionMatcherModel struct {
	Source types.String `tfsdk:"source"`
	Target types.String `tfsdk:"target"`
}

var inhibitionMatcherType = types.ObjectType{AttrTypes: map[string]attr.Type{"source": types.StringType, "target": types.StringType}}

func NewInhibitionRuleResource() resource.Resource { return &inhibitionRuleResource{} }
func (*inhibitionRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_inhibition_rule"
}
func (*inhibitionRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	text := func(description string, limit int, literal bool) schema.StringAttribute {
		return schema.StringAttribute{Required: true, MarkdownDescription: description, Validators: []validator.String{inhibitionTextRule{limit: limit, literal: literal}}}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one tenant inhibition rule, including its complete matcher set and enabled state.", Attributes: map[string]schema.Attribute{
		"id":                idAttribute(),
		"name":              text("Rule name of 1–128 UTF-8 bytes, without NUL or surrounding whitespace. Required without a default; null and empty strings are invalid. Names need not be unique.", 128, false),
		"source_expression": text("Source CEL expression of 1–4096 UTF-8 bytes, without NUL or surrounding whitespace. Required without a default; null and empty strings are invalid. API validation enforces CEL syntax, types and cost limits.", 4096, false),
		"target_expression": text("Target CEL expression of 1–4096 UTF-8 bytes, without NUL or surrounding whitespace. Required without a default; null and empty strings are invalid. Expressions operate on normalized alert context.", 4096, false),
		"matchers": schema.SetNestedAttribute{Required: true, MarkdownDescription: "Complete unordered set of 1–16 distinct source/target literal label-key pairs. Required without a default; null and empty sets are invalid. Pair order has no meaning; dots, slashes and whitespace within each key are preserved.", Validators: []validator.Set{inhibitionMatcherCountRule{}}, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"source": text("Exact source label key: 1–256 UTF-8 bytes without NUL. Required and non-null. Whitespace, dots and slashes are literal and preserved; this is not a JSON path.", 256, true),
			"target": text("Exact target label key: 1–256 UTF-8 bytes without NUL. Required and non-null. Whitespace, dots and slashes are literal and preserved; this is not a JSON path.", 256, true),
		}}},
		"resolved_target_action": schema.StringAttribute{Required: true, MarkdownDescription: "Action for a resolved target: `release` or `keep`. Required without a default; null and empty values are invalid. Existing holds retain their captured policy after later edits or deletion.", Validators: []validator.String{enumRule{"release", "keep"}}},
		"enabled":                schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Desired enabled state, default `true`. Omitted/null selects the default. Explicit `false` is stored atomically with content during creation and updates; disabled rules remain directly readable and retain history. Updating to false requires the native disable grant in addition to update; disabled creation uses create authority. The provider never temporarily enables a disabled rule."},
		"revision":               schema.Int64Attribute{Computed: true, MarkdownDescription: "This rule's positive configuration revision, used as the strong numeric If-Match value for PUT and DELETE. Conflicts retain state and require a fresh plan; the provider never substitutes a newer revision automatically."},
		"index_state":            schema.StringAttribute{Computed: true, MarkdownDescription: "Observed source-index projection: `pending`, `ready` or `obsolete`. May change independently of configuration. It is never sent in writes or used as the configuration revision."},
	}}
}

type inhibitionTextRule struct {
	limit   int
	literal bool
}

func (v inhibitionTextRule) Description(context.Context) string {
	return "Nonempty valid UTF-8 text within the documented byte limit, without NUL; names and expressions must have no surrounding whitespace."
}
func (v inhibitionTextRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v inhibitionTextRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	s := req.ConfigValue.ValueString()
	if s == "" || len(s) > v.limit || !utf8.ValidString(s) || strings.ContainsRune(s, 0) || !v.literal && strings.TrimSpace(s) != s {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid inhibition text", v.Description(ctx))
	}
}

type inhibitionMatcherCountRule struct{}

func (inhibitionMatcherCountRule) Description(context.Context) string {
	return "Supply one through sixteen distinct matcher pairs."
}
func (v inhibitionMatcherCountRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v inhibitionMatcherCountRule) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := len(req.ConfigValue.Elements()); n < 1 || n > 16 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid inhibition matcher count", v.Description(ctx))
	}
}
func inhibitionInput(ctx context.Context, p inhibitionRuleModel) (client.InhibitionInput, diag.Diagnostics) {
	var d diag.Diagnostics
	in := client.InhibitionInput{Name: p.Name.ValueString(), SourceExpression: p.SourceExpression.ValueString(), TargetExpression: p.TargetExpression.ValueString(), ResolvedTargetAction: p.ResolvedTargetAction.ValueString(), Enabled: p.Enabled.ValueBool()}
	for _, v := range []types.String{p.Name, p.SourceExpression, p.TargetExpression, p.ResolvedTargetAction} {
		if v.IsNull() || v.IsUnknown() {
			d.AddError("Unknown inhibition configuration", "Name, source/target expressions and resolved_target_action must be known and non-null before apply.")
			return in, d
		}
	}
	if p.Enabled.IsNull() || p.Enabled.IsUnknown() || p.Matchers.IsNull() || p.Matchers.IsUnknown() {
		d.AddError("Unknown inhibition configuration", "enabled and the complete matcher set must be known and non-null before apply.")
		return in, d
	}
	var pairs []inhibitionMatcherModel
	d.Append(p.Matchers.ElementsAs(ctx, &pairs, false)...)
	if d.HasError() {
		return in, d
	}
	for _, pair := range pairs {
		if pair.Source.IsNull() || pair.Source.IsUnknown() || pair.Target.IsNull() || pair.Target.IsUnknown() {
			d.AddError("Unknown inhibition matcher", "Every source and target label key must be known and non-null before apply.")
			return in, d
		}
		in.Matchers = append(in.Matchers, client.InhibitionMatcher{Source: pair.Source.ValueString(), Target: pair.Target.ValueString()})
	}
	if err := client.ValidateInhibitionInput(in); err != nil {
		apiDiagnostic(&d, "configure OnPager inhibition rule", err)
	}
	return in, d
}
func inhibitionState(ctx context.Context, v client.InhibitionRule) (inhibitionRuleModel, diag.Diagnostics) {
	out := inhibitionRuleModel{ID: types.StringValue(v.ID), Name: types.StringValue(v.Name), SourceExpression: types.StringValue(v.SourceExpression), TargetExpression: types.StringValue(v.TargetExpression), ResolvedTargetAction: types.StringValue(v.ResolvedTargetAction), Enabled: types.BoolValue(v.Enabled), Revision: types.Int64Value(v.Revision), IndexState: types.StringValue(v.IndexState)}
	pairs := make([]inhibitionMatcherModel, 0, len(v.Matchers))
	for _, pair := range v.Matchers {
		pairs = append(pairs, inhibitionMatcherModel{Source: types.StringValue(pair.Source), Target: types.StringValue(pair.Target)})
	}
	var d diag.Diagnostics
	out.Matchers, d = types.SetValueFrom(ctx, inhibitionMatcherType, pairs)
	return out, d
}
func inhibitionIntentEqual(a, b inhibitionRuleModel) bool {
	return a.Name.Equal(b.Name) && a.SourceExpression.Equal(b.SourceExpression) && a.TargetExpression.Equal(b.TargetExpression) && a.Matchers.Equal(b.Matchers) && a.ResolvedTargetAction.Equal(b.ResolvedTargetAction) && a.Enabled.Equal(b.Enabled)
}
func inhibitionPartial(p inhibitionRuleModel, id string, revision int64) inhibitionRuleModel {
	p.ID = types.StringValue(id)
	p.IndexState = types.StringNull()
	p.Revision = types.Int64Null()
	if revision > 0 {
		p.Revision = types.Int64Value(revision)
	}
	return p
}
func (r *inhibitionRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p inhibitionRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, d := inhibitionInput(ctx, p)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.CreateInhibitionRule(ctx, in)
	if v.Committed && validID(v.ID) {
		resp.Diagnostics.Append(resp.State.Set(ctx, inhibitionPartial(p, v.ID, v.Revision))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager inhibition rule", err)
		return
	}
	state, d := inhibitionState(ctx, v)
	resp.Diagnostics.Append(d...)
	if !inhibitionIntentEqual(p, state) {
		resp.Diagnostics.AddError("Unexpected inhibition create result", "The API did not confirm the requested inhibition configuration. The committed UUID, revision and intended settings remain in state for refresh.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
func (r *inhibitionRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var p inhibitionRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetInhibitionRule(ctx, p.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager inhibition rule", err)
		return
	}
	state, d := inhibitionState(ctx, v)
	resp.Diagnostics.Append(d...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}
func (r *inhibitionRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, state inhibitionRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, d := inhibitionInput(ctx, p)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validID(state.ID.ValueString()) || !p.ID.IsUnknown() && !p.ID.IsNull() && !p.ID.Equal(state.ID) || state.Revision.IsUnknown() || state.Revision.IsNull() || state.Revision.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Invalid inhibition identity or revision", "Refresh the exact saved UUID to obtain its own positive revision before updating; identity cannot change during update.")
		return
	}
	receipt, err := r.client.UpdateInhibitionRule(ctx, state.ID.ValueString(), state.Revision.ValueInt64(), in)
	if receipt.Committed {
		resp.Diagnostics.Append(resp.State.Set(ctx, inhibitionPartial(p, state.ID.ValueString(), receipt.Revision))...)
	}
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "update OnPager inhibition rule", err)
		return
	}
	v, err := r.client.GetInhibitionRule(ctx, state.ID.ValueString())
	if err != nil {
		apiDiagnostic(&resp.Diagnostics, "read committed OnPager inhibition rule", err)
		return
	}
	if v.Revision != receipt.Revision {
		resp.Diagnostics.AddError("Inhibition changed after update", "The follow-up read did not return the committed rule revision. The committed UUID, revision and intended settings remain in state; run a fresh plan.")
		return
	}
	updated, d := inhibitionState(ctx, v)
	resp.Diagnostics.Append(d...)
	if !inhibitionIntentEqual(p, updated) {
		resp.Diagnostics.AddError("Unexpected inhibition update result", "The API did not confirm the committed inhibition configuration. The UUID, revision and intended settings remain in state for refresh.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, updated)...)
}
func (r *inhibitionRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var p inhibitionRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if p.Revision.IsNull() || p.Revision.IsUnknown() || p.Revision.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Missing inhibition revision", "Refresh the exact saved UUID before deletion to obtain its own positive revision.")
		return
	}
	if err := r.client.DeleteInhibitionRule(ctx, p.ID.ValueString(), p.Revision.ValueInt64()); err != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager inhibition rule", err)
	}
}
func (*inhibitionRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
