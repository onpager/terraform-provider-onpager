package provider

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type configuredResource struct{ client *client.Client }

func (r *configuredResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid OnPager client", "Provider configuration supplied an unexpected client type.")
		return
	}
	r.client = c
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validID(id string) bool {
	return uuidPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

type stringRule struct{ uuid bool }

func (v stringRule) Description(context.Context) string {
	if v.uuid {
		return "A nonzero canonical lowercase UUID."
	}
	return "A nonempty name without leading or trailing whitespace."
}
func (v stringRule) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v stringRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if (v.uuid && !validID(s)) || (!v.uuid && (s == "" || strings.TrimSpace(s) != s)) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid OnPager attribute", v.Description(ctx))
	}
}

func idAttribute() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: "Stable nonzero canonical lowercase OnPager UUID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}
func nameAttribute() schema.StringAttribute {
	return schema.StringAttribute{Required: true, MarkdownDescription: "Nonempty name without leading or trailing whitespace. Unique case-insensitively within the tenant, including inactive records.", Validators: []validator.String{stringRule{}}}
}
func uuidAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{Required: true, MarkdownDescription: description + " Must be a nonzero canonical lowercase UUID.", Validators: []validator.String{stringRule{uuid: true}}}
}
func entityAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": idAttribute(), "name": nameAttribute(),
		"active": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Whether the object is active. Defaults to `true`. Changes disable or reactivate the existing UUID. Disabled objects remain in state; refresh and import never reactivate them."},
	}
}
func apiDiagnostic(diags *diag.Diagnostics, operation string, err error) {
	message := err.Error()
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 412:
			message += ". Configuration changed concurrently; run a new plan before retrying."
		case apiErr.StatusCode == 403:
			message += ". Check the operation's native RBAC grants; existing state is retained."
		case apiErr.StatusCode == 409:
			message += ". Check current dependencies and unique names, including inactive records; no object was implicitly adopted."
		}
	}
	diags.AddError("Unable to "+operation, message)
}
func importEntity(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !validID(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", "Use a nonzero canonical lowercase OnPager UUID.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
