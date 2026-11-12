package provider

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type enrichmentRulesResource struct{ configuredResource }

func NewEnrichmentRulesResource() resource.Resource { return &enrichmentRulesResource{} }
func (*enrichmentRulesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_enrichment_rules"
}
func (*enrichmentRulesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage the tenant's complete ordered enrichment rule snapshot. Configure one resource per tenant.", Attributes: map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Stable aggregate identity and import ID: `tenant`."},
		"revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Current whole-snapshot CAS revision, including initial revision zero. Stale publication returns HTTP 409; run a new plan before retrying."},
		"rules": schema.ListNestedAttribute{Required: true, MarkdownDescription: "Complete ordered rule list, at most 20 rules and 256 KiB encoded JSON. An explicit empty list owns an empty configuration; omitted/null is invalid. Rule and lookup alternative order are preserved. All rules reference enabled datasets. Repeated IDs, duplicate emitted label producers and dependency cycles are rejected by the API.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"id":         uuidAttribute("Explicit stable rule UUID. Keep it unchanged to preserve rule identity; the provider never generates a random ID during planning."),
			"dataset_id": uuidAttribute("Explicit dataset UUID. No target discovery is performed."),
			"lookup":     schema.ListAttribute{Required: true, ElementType: types.MapType{ElemType: types.StringType}, MarkdownDescription: "Ordered list of 1–16 lookup alternatives. Each map contains 1–8 dataset-field keys pointing to `labels.key` or `enriched_labels.key`. Dataset field and referenced label names are nonempty, at most 256 UTF-8 bytes without NUL. Maps within the list retain their alternative priority."},
			"emit":       schema.MapAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "1–16 emitted label names mapped to dataset field names. Both are nonempty, at most 256 UTF-8 bytes without NUL; one rule may produce a given label across the entire snapshot."},
		}}},
	}}
}

type enrichmentRuleModel struct {
	ID        types.String `tfsdk:"id"`
	DatasetID types.String `tfsdk:"dataset_id"`
	Lookup    types.List   `tfsdk:"lookup"`
	Emit      types.Map    `tfsdk:"emit"`
}
type enrichmentRulesModel struct {
	ID       types.String `tfsdk:"id"`
	Revision types.Int64  `tfsdk:"revision"`
	Rules    types.List   `tfsdk:"rules"`
}

func (*enrichmentRulesResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m enrichmentRulesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Rules.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("rules"), "Missing tenant rules", "Supply the complete ordered rule list; use [] to own an empty configuration.")
		return
	}
	raw, e := m.Rules.ToTerraformValue(ctx)
	if e != nil || !raw.IsFullyKnown() {
		return
	}
	rules := enrichmentRulePayload(ctx, m, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := client.ValidateEnrichmentRules(rules); e != nil {
		resp.Diagnostics.AddAttributeError(path.Root("rules"), "Invalid tenant enrichment rules", e.Error())
	}
}

var enrichmentRuleAttrTypes = map[string]attr.Type{"id": types.StringType, "dataset_id": types.StringType, "lookup": types.ListType{ElemType: types.MapType{ElemType: types.StringType}}, "emit": types.MapType{ElemType: types.StringType}}

func enrichmentRulePayload(ctx context.Context, m enrichmentRulesModel, d *diag.Diagnostics) []client.EnrichmentRule {
	var models []enrichmentRuleModel
	d.Append(m.Rules.ElementsAs(ctx, &models, false)...)
	rules := make([]client.EnrichmentRule, len(models))
	for i, v := range models {
		rules[i].ID = v.ID.ValueString()
		rules[i].DatasetID = v.DatasetID.ValueString()
		d.Append(v.Lookup.ElementsAs(ctx, &rules[i].Lookup, false)...)
		d.Append(v.Emit.ElementsAs(ctx, &rules[i].Emit, false)...)
	}
	return rules
}
func enrichmentRulesModelFrom(ctx context.Context, v client.EnrichmentRules, d *diag.Diagnostics) enrichmentRulesModel {
	models := make([]enrichmentRuleModel, len(v.Rules))
	for i, r := range v.Rules {
		lookup, ds := types.ListValueFrom(ctx, types.MapType{ElemType: types.StringType}, r.Lookup)
		d.Append(ds...)
		emit, ds := types.MapValueFrom(ctx, types.StringType, r.Emit)
		d.Append(ds...)
		models[i] = enrichmentRuleModel{ID: types.StringValue(r.ID), DatasetID: types.StringValue(r.DatasetID), Lookup: lookup, Emit: emit}
	}
	rules, ds := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: enrichmentRuleAttrTypes}, models)
	d.Append(ds...)
	return enrichmentRulesModel{ID: types.StringValue("tenant"), Revision: types.Int64Value(v.Revision), Rules: rules}
}
func enrichmentRulesReceipt(p enrichmentRulesModel, v client.EnrichmentRules, expected int64) enrichmentRulesModel {
	p.ID = types.StringValue("tenant")
	p.Revision = types.Int64Null()
	if v.RevisionPresent && v.Revision == expected+1 {
		p.Revision = types.Int64Value(v.Revision)
	}
	return p
}
func (r *enrichmentRulesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p enrichmentRulesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	rules := enrichmentRulePayload(ctx, p, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := client.ValidateEnrichmentRules(rules); e != nil {
		apiDiagnostic(&resp.Diagnostics, "validate OnPager enrichment rules", e)
		return
	}
	current, e := r.client.GetEnrichmentRules(ctx)
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read unowned OnPager enrichment rules", e)
		return
	}
	if len(current.Rules) > 0 {
		resp.Diagnostics.AddError("Existing tenant enrichment rules require import", "The current tenant snapshot is nonempty and is not owned by this resource. Import it with ID `tenant` before applying. This resource owns the complete ordered list and never merges or adopts existing rules during create.")
		return
	}
	v, e := r.client.PublishEnrichmentRules(ctx, current.Revision, rules)
	if v.Committed {
		p = enrichmentRulesReceipt(p, v, current.Revision)
		resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "publish initial OnPager enrichment rules", e)
		return
	}
	p = enrichmentRulesModelFrom(ctx, v, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
}
func (r *enrichmentRulesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	v, e := r.client.GetEnrichmentRules(ctx)
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager enrichment rules", e)
		return
	}
	m := enrichmentRulesModelFrom(ctx, v, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *enrichmentRulesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s enrichmentRulesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	rules := enrichmentRulePayload(ctx, p, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if s.Revision.IsNull() || s.Revision.IsUnknown() {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager enrichment rules", errors.New("refresh the tenant rules to obtain the current revision before retrying"))
		return
	}
	v, e := r.client.PublishEnrichmentRules(ctx, s.Revision.ValueInt64(), rules)
	if v.Committed {
		p = enrichmentRulesReceipt(p, v, s.Revision.ValueInt64())
		resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "publish OnPager enrichment rules", e)
		return
	}
	p = enrichmentRulesModelFrom(ctx, v, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
}
func (r *enrichmentRulesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s enrichmentRulesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if s.Revision.IsNull() || s.Revision.IsUnknown() {
		apiDiagnostic(&resp.Diagnostics, "clear OnPager enrichment rules", errors.New("refresh the tenant rules to obtain the current revision before retrying"))
		return
	}
	v, e := r.client.PublishEnrichmentRules(ctx, s.Revision.ValueInt64(), []client.EnrichmentRule{})
	if v.Committed && e != nil {
		// A committed empty publication must never leave state describing the
		// preceding nonempty snapshot, even when the success body is malformed.
		s.Rules = types.ListValueMust(types.ObjectType{AttrTypes: enrichmentRuleAttrTypes}, []attr.Value{})
		s = enrichmentRulesReceipt(s, v, s.Revision.ValueInt64())
		resp.Diagnostics.Append(resp.State.Set(ctx, &s)...)
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "clear OnPager enrichment rules", e)
	}
}
func (*enrichmentRulesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != "tenant" {
		resp.Diagnostics.AddError("Invalid enrichment rules import ID", "Use the literal ID `tenant`; this resource owns the whole tenant snapshot.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

type enrichmentStringValidator struct {
	name bool
	max  int
}

func (v enrichmentStringValidator) Description(context.Context) string {
	if v.name {
		return "A dataset name matching [A-Za-z_][A-Za-z0-9_-]{0,127}."
	}
	return "Valid UTF-8 text within the documented byte limit, without NUL."
}
func (v enrichmentStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v enrichmentStringValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	ok := utf8.ValidString(s) && !strings.ContainsRune(s, 0)
	if v.name {
		ok = ok && regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`).MatchString(s)
	} else {
		ok = ok && len(s) <= v.max
	}
	if !ok {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid enrichment text", v.Description(ctx))
	}
}
