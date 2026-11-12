// Package provider implements the OnPager Terraform provider.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Address is the canonical public Registry address, also used for local overrides.
const Address = "registry.terraform.io/onpager/onpager"

var _ provider.Provider = (*OnPagerProvider)(nil)

// OnPagerProvider configures clients shared by resources and data sources.
type OnPagerProvider struct {
	version string
}

type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
	Timezone types.String `tfsdk:"timezone"`
}

// New returns a fresh provider instance for each Terraform server.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &OnPagerProvider{version: version}
	}
}

func (p *OnPagerProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "onpager"
	resp.Version = p.version
}

func (p *OnPagerProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage OnPager configuration through the Control API using bearer authentication.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "OnPager Control base URL, including an optional reverse-proxy path prefix. Accepts a literal, input variable or data-source expression known during planning. Omission or `null` uses `ONPAGER_ENDPOINT`; an empty string is invalid. Explicit values override the environment. Use an HTTP(S) URL. HTTPS verifies TLS certificates by default.",
			},
			"token": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "OnPager bearer token, preferably for a service account with explicit native role assignments. Accepts a literal, input variable or data-source expression known during planning, including a Vault secret. Omission or `null` uses `ONPAGER_TOKEN`; an empty string is invalid. Explicit values override the environment. Marked sensitive to hide normal display.",
			},
			"timezone": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional IANA request timezone, such as `Europe/Moscow` or `UTC`. Sends `Onpager-Timezone` for both user PATs and service accounts, overriding the profile for this request without changing stored preferences. Omission or `null` sends no header: user tokens use their profile/tenant default, service accounts use UTC. Empty, invalid and unknown values are errors. The backend interprets local datetime input and formats responses; the provider does not convert time zones. Explicit timestamp offsets still identify absolute instants. Recurrence, source weekdays and storage retain their UTC semantics.",
			},
		},
	}
}

func (p *OnPagerProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, value := range map[string]types.String{"endpoint": data.Endpoint, "token": data.Token, "timezone": data.Timezone} {
		if value.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown OnPager Configuration", "The provider requires a known value for this attribute before it can configure the API client.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	endpoint, token := data.Endpoint.ValueString(), data.Token.ValueString()
	if data.Endpoint.IsNull() {
		endpoint = os.Getenv("ONPAGER_ENDPOINT")
	}
	if data.Token.IsNull() {
		token = os.Getenv("ONPAGER_TOKEN")
	}
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing OnPager Endpoint", "Set endpoint in the provider configuration or set ONPAGER_ENDPOINT.")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing OnPager Token", "Set token in the provider configuration or set ONPAGER_TOKEN.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var options []client.Option
	if !data.Timezone.IsNull() {
		options = append(options, client.WithTimezone(data.Timezone.ValueString()))
	}
	c, err := client.New(endpoint, token, options...)
	if err != nil {
		resp.Diagnostics.AddError("Invalid OnPager Configuration", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (*OnPagerProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewTeamResource, NewServiceResource, NewTeamMembershipResource, NewEscalationChainResource,
		NewIntegrationResource, NewTeamAlertFilterResource, NewServiceAlertFilterResource,
		NewEscalationRuleResource, NewTeamAlertChannelResource, NewServiceAlertChannelResource,
		NewTeamOncallChannelResource, NewWorkCalendarResource, NewWorkCalendarEntryResource,
		NewScheduleResource, NewScheduleRotationResource, NewScheduleOverrideResource,
		NewServiceAccountResource, NewRBACRoleResource,
		NewGroupResource, NewGroupMembershipResource, NewOIDCGroupMappingResource,
		NewNotificationTemplateResource,
		NewRoleAssignmentResource, NewScheduleHandoverResource,
		NewTeamNotificationTemplateResource, NewOutgoingWebhookResource,
		NewTeamOutgoingWebhookResource,
		NewSilenceResource, NewInhibitionRuleResource,
		NewEnrichmentDatasetResource, NewEnrichmentRulesResource,
		NewBotResource, NewVoiceProviderResource,
		NewTeamRoleDefaultsResource,
	}
}

func (*OnPagerProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewUserDataSource, NewTeamDataSource, NewServiceDataSource,
		NewRBACRoleDataSource, NewGroupDataSource, NewDeliveryChannelDataSource,
		NewRBACPermissionsDataSource,
	}
}
