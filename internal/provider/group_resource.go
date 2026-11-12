package provider

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type groupResource struct{ configuredResource }
type groupModel struct {
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Revision    types.Int64  `tfsdk:"revision"`
}

func NewGroupResource() resource.Resource { return &groupResource{} }
func (*groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}
func (*groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage one custom tenant user group and its metadata.", Attributes: map[string]schema.Attribute{
		"id":          idAttribute(),
		"key":         schema.StringAttribute{Required: true, MarkdownDescription: "Unique immutable key: 1–63 lowercase letters, digits, `_` or `-`, beginning with a letter or digit. `all_users` is reserved. Changing it replaces the group.", Validators: []validator.String{groupStringRule{kind: "key"}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty display name without leading/trailing whitespace or NUL. Display names may repeat; only the key is unique.", Validators: []validator.String{groupStringRule{kind: "name"}}},
		"description": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Literal description without NUL. Omitted or null defaults to an empty string; an empty string clears the description. Metadata PATCH sends only changed fields.", Validators: []validator.String{groupStringRule{kind: "text"}}},
		"revision":    schema.Int64Attribute{Computed: true, MarkdownDescription: "Current group metadata revision, used only for group deletion. Manual memberships and OIDC mappings do not advance it."},
	}}
}
func groupState(v client.Group) groupModel {
	return groupModel{ID: types.StringValue(v.ID), Key: types.StringValue(v.Key), Name: types.StringValue(v.Name), Description: types.StringValue(v.Description), Revision: types.Int64Value(v.Revision)}
}
func groupSystemDiagnostic(v client.Group) bool { return v.System || v.Key == "all_users" }
func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var p groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.CreateGroup(ctx, client.GroupInput{Key: p.Key.ValueString(), Name: p.Name.ValueString(), Description: p.Description.ValueString()})
	if e != nil {
		if validID(v.ID) && !groupSystemDiagnostic(v) && (v.Key == "" || v.Key == p.Key.ValueString()) {
			p.ID = types.StringValue(v.ID)
			p.Revision = types.Int64Null()
			if v.Revision > 0 {
				p.Revision = types.Int64Value(v.Revision)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
		}
		apiDiagnostic(&resp.Diagnostics, "create OnPager group", e)
		return
	}
	if groupSystemDiagnostic(v) {
		resp.Diagnostics.AddError("Unsupported system group", "The derived All Users group cannot be managed.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupState(v))...)
}
func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.client.GetGroup(ctx, s.ID.ValueString())
	if client.IsNotFound(e) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		apiDiagnostic(&resp.Diagnostics, "read OnPager group", e)
		return
	}
	if groupSystemDiagnostic(v) {
		resp.Diagnostics.AddError("Unsupported system group", "The derived All Users group cannot be managed or imported.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupState(v))...)
}
func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var p, s groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &p)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if s.Key.ValueString() == "all_users" {
		resp.Diagnostics.AddError("Unsupported system group", "The derived All Users group cannot be modified.")
		return
	}
	in := client.GroupUpdate{}
	if !p.Name.Equal(s.Name) {
		v := p.Name.ValueString()
		in.Name = &v
	}
	if !p.Description.Equal(s.Description) {
		v := p.Description.ValueString()
		in.Description = &v
	}
	if in.Name == nil && in.Description == nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, &s)...)
		return
	}
	v, e := r.client.UpdateGroup(ctx, s.ID.ValueString(), in)
	if e != nil {
		if v.ID == s.ID.ValueString() && !groupSystemDiagnostic(v) && (v.Key == "" || v.Key == s.Key.ValueString()) && v.Revision > 0 {
			p.ID = s.ID
			p.Revision = types.Int64Value(v.Revision)
			resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
		}
		apiDiagnostic(&resp.Diagnostics, "update OnPager group", e)
		return
	}
	if groupSystemDiagnostic(v) {
		resp.Diagnostics.AddError("Unsupported system group", "The derived All Users group cannot be managed.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupState(v))...)
}
func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var s groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if s.Key.ValueString() == "all_users" {
		resp.Diagnostics.AddError("Unsupported system group", "The derived All Users group cannot be deleted.")
		return
	}
	if e := r.client.DeleteGroup(ctx, s.ID.ValueString(), s.Revision.ValueInt64()); e != nil {
		apiDiagnostic(&resp.Diagnostics, "delete unused OnPager group", e)
	}
}
func (*groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importEntity(ctx, req, resp)
}

var groupKeyRule = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var groupProviderRule = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type groupStringRule struct{ kind string }

func (v groupStringRule) Description(context.Context) string {
	switch v.kind {
	case "key":
		return "Use 1–63 lowercase letters, digits, underscores or hyphens, beginning with a letter or digit; all_users is reserved."
	case "name":
		return "Use a nonempty name without leading/trailing whitespace or NUL."
	case "provider":
		return "Use 1–64 letters, digits, underscores or hyphens."
	case "literal":
		return "Use a nonempty literal string without NUL; case and whitespace are preserved."
	case "issuer":
		return "Use a nonblank issuer without NUL; its literal value is preserved."
	default:
		return "Use valid text without NUL."
	}
}
func (v groupStringRule) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v groupStringRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	valid := utf8.ValidString(s) && !strings.ContainsRune(s, 0)
	switch v.kind {
	case "key":
		valid = valid && groupKeyRule.MatchString(s) && s != "all_users"
	case "name":
		valid = valid && s != "" && strings.TrimSpace(s) == s
	case "provider":
		valid = valid && groupProviderRule.MatchString(s)
	case "literal":
		valid = valid && s != ""
	case "issuer":
		valid = valid && strings.TrimSpace(s) != ""
	}
	if !valid {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid OnPager group attribute", v.Description(ctx))
	}
}
