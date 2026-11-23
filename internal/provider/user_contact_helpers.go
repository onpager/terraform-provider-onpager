package provider

import (
	"context"
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func contactRequestIDAttribute() schema.StringAttribute {
	a := optionalUUID("Create request UUID. Supply a stable UUID before creation, for example from terraform_data. Keep it for retries of the same create and supply a fresh UUID when recreating a deleted contact. Changing the configured value replaces the contact. May be omitted on import.")
	a.Computed = true
	a.PlanModifiers = []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}
	return a
}

func contactTemplateSlotAttribute() schema.StringAttribute {
	a := optionalUUID("Contact UUID in the tenant user notification defaults template. Set it to complete the matching user's untouched draft in place and preserve its policy references. Omit to create a separate method. Changing the configured slot replaces the resource. The API does not return this value on import.")
	a.Computed = true
	a.PlanModifiers = []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}
	return a
}

// planUserContactCreate also returns whether creation inputs must be supplied.
func planUserContactCreate(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, bindingKey string) bool {
	if req.Plan.Raw.IsNull() {
		return false
	}
	creating := req.State.Raw.IsNull()
	requestPath := path.Root("request_id")
	var configuredKey types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, requestPath, &configuredKey)...)

	// Neither creation input is returned by Control. Preserve the previous
	// value when omitted, including the null values of an imported contact.
	for _, key := range []string{"template_contact_id", "request_id"} {
		p := path.Root(key)
		var config, prior types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, p, &config)...)
		if config.IsNull() {
			if !creating {
				resp.Diagnostics.Append(req.State.GetAttribute(ctx, p, &prior)...)
			}
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, p, prior)...)
		}
	}
	if !creating {
		changed := func(key string) bool {
			p := path.Root(key)
			var before, after types.String
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, p, &before)...)
			resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, p, &after)...)
			return !before.Equal(after)
		}
		userChanged := changed("user_id")
		bindingChanged := changed(bindingKey) || changed("template_contact_id")
		keyChanged := changed("request_id")
		creating = userChanged || bindingChanged || keyChanged
		// The receipt is scoped to the user and method kind. DELETE retains
		// the key, so a replacement for this user needs a fresh request UUID.
		if !userChanged && bindingChanged && !keyChanged && !configuredKey.IsNull() {
			resp.Diagnostics.AddAttributeError(requestPath, "Fresh create request UUID required", "Set a new request_id when replacing this contact's channel, webhook or template slot for the same user. Control retains the previous request key after deletion.")
		}
	}
	if creating && configuredKey.IsNull() {
		resp.Diagnostics.AddAttributeError(requestPath, "Create request UUID required", "Supply request_id when creating or replacing a contact. Keep the same UUID and initial inputs when retrying a create. It may be omitted when managing an imported contact in place.")
	}
	return creating
}

func importUserContact(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	user, method, ok := splitImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid user contact import ID", "Use user_uuid/method_uuid with two nonzero canonical lowercase UUIDs.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), types.StringValue(user))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.StringValue(method))...)
}

func contactCreateDiagnostic(diags *diag.Diagnostics, operation string, err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		diags.AddError("Unable to "+operation, err.Error()+". Retry a create with its original request_id and inputs. If the contact was deleted, supply a fresh request_id. If a template slot was specified, check that it matches the user's eligible draft.")
		return
	}
	apiDiagnostic(diags, operation, err)
}
