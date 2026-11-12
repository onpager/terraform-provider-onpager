package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type enrichmentDatasetResource struct{ configuredResource }

func NewEnrichmentDatasetResource() resource.Resource { return &enrichmentDatasetResource{} }
func (*enrichmentDatasetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_enrichment_dataset"
}
func (*enrichmentDatasetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one enrichment dataset, including its metadata, enabled state and complete row set.", Attributes: map[string]schema.Attribute{
		"id":                    idAttribute(),
		"name":                  schema.StringAttribute{Required: true, MarkdownDescription: "Immutable dataset name, 1–128 ASCII characters matching `[A-Za-z_][A-Za-z0-9_-]*`. Changing it replaces the dataset; a deleted name can be reused for a new UUID.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{enrichmentStringValidator{name: true}}},
		"description":           schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Description of at most 4096 UTF-8 bytes without NUL. Omitted/null defaults to an empty string; an explicit empty string clears it.", Validators: []validator.String{enrichmentStringValidator{max: 4096}}},
		"enabled":               schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether runtime enrichment may use this dataset. Defaults to `true`; `false` is atomic on create. Content publication while disabled preserves `false`. Current rules referencing the dataset can block disabling."},
		"rows":                  schema.SetAttribute{Required: true, Sensitive: true, ElementType: types.MapType{ElemType: types.StringType}, MarkdownDescription: "Complete set of string maps. An explicit empty set clears all rows; null is invalid. The server sorts and deduplicates rows, so order has no meaning. Up to 100,000 rows, 256 distinct nonempty field names (at most 256 UTF-8 bytes each), 4096 UTF-8 bytes per value, no NUL, and 10 MiB encoded request. Empty maps and empty string values are supported. Reading requires `enrichment_dataset_rows.read`; contents persist in Terraform state."},
		"configuration_version": schema.Int64Attribute{Computed: true, MarkdownDescription: "Dataset management CAS version. Metadata, content and publisher-token writes advance this version independently of the row revision; stale writes return HTTP 409 and require a new plan."},
		"current_revision":      schema.Int64Attribute{Computed: true, MarkdownDescription: "Immutable row-content revision selected by the current dataset. Refresh reads every page of this exact revision, without scanning revision history."},
	}}
}

type enrichmentDatasetModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	Description          types.String `tfsdk:"description"`
	Enabled              types.Bool   `tfsdk:"enabled"`
	Rows                 types.Set    `tfsdk:"rows"`
	ConfigurationVersion types.Int64  `tfsdk:"configuration_version"`
	CurrentRevision      types.Int64  `tfsdk:"current_revision"`
}

func (*enrichmentDatasetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m enrichmentDatasetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Rows.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("rows"), "Missing dataset rows", "Supply an explicit row set; use [] for an empty dataset.")
		return
	}
	raw, e := m.Rows.ToTerraformValue(ctx)
	if e != nil || !raw.IsFullyKnown() {
		return
	}
	rows := enrichmentRows(ctx, m, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := client.ValidateEnrichmentRows(rows); e != nil {
		resp.Diagnostics.AddAttributeError(path.Root("rows"), "Invalid dataset rows", e.Error())
	}
}

func enrichmentRows(ctx context.Context, m enrichmentDatasetModel, d *diag.Diagnostics) []map[string]string {
	var rows []map[string]string
	d.Append(m.Rows.ElementsAs(ctx, &rows, false)...)
	if rows == nil && !m.Rows.IsNull() && !m.Rows.IsUnknown() {
		rows = []map[string]string{}
	}
	return rows
}
func enrichmentDatasetModelFrom(ctx context.Context, v client.EnrichmentDataset, rows []map[string]string, d *diag.Diagnostics) enrichmentDatasetModel {
	s, ds := types.SetValueFrom(ctx, types.MapType{ElemType: types.StringType}, rows)
	d.Append(ds...)
	return enrichmentDatasetModel{ID: types.StringValue(v.ID), Name: types.StringValue(v.Name), Description: types.StringValue(v.Description), Enabled: types.BoolValue(v.Enabled), Rows: s, ConfigurationVersion: types.Int64Value(v.ConfigurationVersion), CurrentRevision: types.Int64Value(v.CurrentRevision)}
}

// receipt preserves a committed desired snapshot before any later write/read.
// Invalid success bodies never authorize a new CAS with an unconfirmed version.
func enrichmentDatasetReceipt(m enrichmentDatasetModel, v client.EnrichmentDataset, version, revision int64) enrichmentDatasetModel {
	m.ConfigurationVersion = types.Int64Null()
	m.CurrentRevision = types.Int64Null()
	if v.ID == m.ID.ValueString() {
		if v.ConfigurationVersion == version {
			m.ConfigurationVersion = types.Int64Value(version)
		}
		if v.CurrentRevision == revision {
			m.CurrentRevision = types.Int64Value(revision)
		}
	}
	return m
}
func (r *enrichmentDatasetResource) finishWrite(ctx context.Context, m enrichmentDatasetModel, d *diag.Diagnostics) enrichmentDatasetModel {
	v, e := r.client.GetEnrichmentDataset(ctx, m.ID.ValueString())
	if e != nil {
		apiDiagnostic(d, "read committed OnPager enrichment dataset", e)
		return m
	}
	if m.ConfigurationVersion.IsNull() || m.CurrentRevision.IsNull() || v.ConfigurationVersion != m.ConfigurationVersion.ValueInt64() || v.CurrentRevision != m.CurrentRevision.ValueInt64() {
		apiDiagnostic(d, "confirm committed OnPager enrichment dataset", errors.New("dataset changed concurrently after the committed write; committed state is retained, run a new plan"))
		return m
	}
	rows, e := r.client.GetEnrichmentDatasetRows(ctx, v.ID, v.CurrentRevision, *v.Revision.RowCount)
	if e != nil {
		apiDiagnostic(d, "read committed OnPager enrichment dataset rows", e)
		return m
	}
	return enrichmentDatasetModelFrom(ctx, v, rows, d)
}
func (r *enrichmentDatasetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p enrichmentDatasetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	rows := enrichmentRows(ctx, p, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.CreateEnrichmentDataset(ctx, client.EnrichmentDatasetInput{Name: p.Name.ValueString(), Description: p.Description.ValueString(), Enabled: p.Enabled.ValueBool(), Rows: rows})
	if v.Committed && validID(v.ID) {
		p.ID = types.StringValue(v.ID)
		p = enrichmentDatasetReceipt(p, v, 1, 1)
		resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "create OnPager enrichment dataset", e)
		return
	}
	p = r.finishWrite(ctx, p, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
}
func (r *enrichmentDatasetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s enrichmentDatasetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetEnrichmentDataset(ctx, s.ID.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager enrichment dataset", e)
		return
	}
	rows, e := r.client.GetEnrichmentDatasetRows(ctx, v.ID, v.CurrentRevision, *v.Revision.RowCount)
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager enrichment dataset rows", e)
		return
	}
	m := enrichmentDatasetModelFrom(ctx, v, rows, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *enrichmentDatasetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s enrichmentDatasetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	rows := enrichmentRows(ctx, p, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if s.ConfigurationVersion.IsNull() || s.ConfigurationVersion.IsUnknown() || s.ConfigurationVersion.ValueInt64() <= 0 || s.CurrentRevision.IsNull() || s.CurrentRevision.ValueInt64() <= 0 {
		apiDiagnostic(&resp.Diagnostics, "update OnPager enrichment dataset", errors.New("refresh the dataset to obtain its current configuration_version and row revision before retrying"))
		return
	}
	m := s
	if !p.Description.Equal(s.Description) || !p.Enabled.Equal(s.Enabled) {
		in := client.EnrichmentDatasetUpdate{}
		if !p.Description.Equal(s.Description) {
			v := p.Description.ValueString()
			in.Description = &v
		}
		if !p.Enabled.Equal(s.Enabled) {
			v := p.Enabled.ValueBool()
			in.Enabled = &v
		}
		v, e := r.client.UpdateEnrichmentDataset(ctx, s.ID.ValueString(), m.ConfigurationVersion.ValueInt64(), in)
		if v.Committed {
			m.Description = p.Description
			m.Enabled = p.Enabled
			m = enrichmentDatasetReceipt(m, v, s.ConfigurationVersion.ValueInt64()+1, s.CurrentRevision.ValueInt64())
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		}
		if e != nil {
			apiDiagnostic(&resp.Diagnostics, "update OnPager enrichment dataset metadata", e)
			return
		}
		if v.Name != s.Name.ValueString() || v.CurrentRevision != s.CurrentRevision.ValueInt64() {
			apiDiagnostic(&resp.Diagnostics, "confirm OnPager enrichment dataset metadata", errors.New("OnPager returned unexpected dataset identity or row revision; committed state is retained"))
			return
		}
	}
	if !p.Rows.Equal(s.Rows) {
		version := m.ConfigurationVersion.ValueInt64()
		v, e := r.client.PublishEnrichmentDataset(ctx, s.ID.ValueString(), version, rows)
		if v.Committed {
			m.Rows = p.Rows
			m = enrichmentDatasetReceipt(m, v, version+1, s.CurrentRevision.ValueInt64()+1)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		}
		if e != nil {
			apiDiagnostic(&resp.Diagnostics, "publish OnPager enrichment dataset rows", e)
			return
		}
		if v.Name != s.Name.ValueString() || v.Description != m.Description.ValueString() || v.Enabled != m.Enabled.ValueBool() || v.CurrentRevision != s.CurrentRevision.ValueInt64()+1 {
			apiDiagnostic(&resp.Diagnostics, "confirm OnPager enrichment dataset publication", errors.New("OnPager returned unexpected dataset metadata or row revision; committed state is retained"))
			return
		}
	}
	m = r.finishWrite(ctx, m, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *enrichmentDatasetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s enrichmentDatasetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := r.client.DeleteEnrichmentDataset(ctx, s.ID.ValueString(), s.ConfigurationVersion.ValueInt64()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "delete OnPager enrichment dataset", e)
	}
}
func (*enrichmentDatasetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}
