package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Control returns a canonical URL. Preserve equivalent HCL spelling across
// apply and refresh, and adopt the server value on import or endpoint drift.
func providerBaseURLState(previous types.Object, observed string) types.String {
	if prior, ok := previous.Attributes()["base_url"].(types.String); ok && !prior.IsNull() && !prior.IsUnknown() {
		if canonical, err := client.NormalizeProviderBaseURL(prior.ValueString()); err == nil && canonical == observed {
			return prior
		}
	}
	return types.StringValue(observed)
}
