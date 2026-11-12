package provider_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func TestProtocolSchemaProtectsToken(t *testing.T) {
	server := providerserver.NewProtocol6(onpager.New("test")())()
	resp, err := server.GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range resp.Diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("protocol schema error: %s: %s", diagnostic.Summary, diagnostic.Detail)
		}
	}
	if resp.Provider == nil || resp.Provider.Block == nil {
		t.Fatal("provider schema is missing")
	}
	for _, attribute := range resp.Provider.Block.Attributes {
		if attribute.Name == "token" {
			if !attribute.Sensitive {
				t.Fatal("token is not sensitive in the Terraform protocol schema")
			}
			return
		}
	}
	t.Fatal("token attribute is missing from the Terraform protocol schema")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func configure(t *testing.T, endpoint, token types.String) provider.ConfigureResponse {
	return configureWithTimezone(t, endpoint, token, types.StringNull())
}

func configureWithTimezone(t *testing.T, endpoint, token, timezone types.String) provider.ConfigureResponse {
	t.Helper()
	p := onpager.New("test")()
	var schema provider.SchemaResponse
	p.Schema(t.Context(), provider.SchemaRequest{}, &schema)
	value := types.ObjectValueMust(
		map[string]attr.Type{"endpoint": types.StringType, "token": types.StringType, "timezone": types.StringType},
		map[string]attr.Value{"endpoint": endpoint, "token": token, "timezone": timezone},
	)
	raw, err := value.ToTerraformValue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var resp provider.ConfigureResponse
	p.Configure(t.Context(), provider.ConfigureRequest{Config: tfsdk.Config{Raw: raw, Schema: schema.Schema}}, &resp)
	return resp
}

func TestConfigureRequestTimezone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   types.String
		zone    string
		invalid bool
	}{
		{"omitted", types.StringNull(), "", false},
		{"explicit UTC", types.StringValue("UTC"), "UTC", false},
		{"explicit IANA", types.StringValue("Europe/Moscow"), "Europe/Moscow", false},
		{"unknown", types.StringUnknown(), "", true},
		{"empty", types.StringValue(""), "", true},
		{"offset", types.StringValue("+03:00"), "", true},
		{"bad IANA", types.StringValue("Invalid/Zone"), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := configureWithTimezone(t, types.StringValue("https://unused.invalid"), types.StringValue("fixture-token"), tc.value)
			if resp.Diagnostics.HasError() != tc.invalid {
				t.Fatal(resp.Diagnostics)
			}
			if tc.invalid {
				if resp.ResourceData != nil || resp.DataSourceData != nil {
					t.Fatal("invalid timezone configured a client")
				}
				return
			}
			c, ok := resp.ResourceData.(*client.Client)
			if !ok || c.Timezone() != tc.zone {
				t.Fatal("configured request timezone differs")
			}
		})
	}
}

func TestConfigureCredentialsAndPrecedence(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "environment fallback"
		if explicit {
			name = "explicit config overrides environment"
		}
		t.Run(name, func(t *testing.T) {
			requests := 0
			previousTransport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previousTransport })
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				requests++
				if r.URL.String() != "https://chosen.example/v1/teams" || r.Header.Get("Authorization") != "Bearer chosen-token" {
					t.Error("provider configured the wrong endpoint or credentials")
				}
				w.WriteHeader(http.StatusNoContent)
				return w.Result(), nil
			})
			endpoint, token := types.StringNull(), types.StringNull()
			if explicit {
				t.Setenv("ONPAGER_ENDPOINT", "invalid-environment-endpoint")
				t.Setenv("ONPAGER_TOKEN", "wrong-token")
				endpoint, token = types.StringValue("https://chosen.example"), types.StringValue("chosen-token")
			} else {
				t.Setenv("ONPAGER_ENDPOINT", "https://chosen.example")
				t.Setenv("ONPAGER_TOKEN", "chosen-token")
			}
			resp := configure(t, endpoint, token)
			if resp.Diagnostics.HasError() {
				t.Fatalf("configure failed: %s", resp.Diagnostics)
			}
			if requests != 0 {
				t.Fatal("Configure called the backend")
			}
			c, ok := resp.ResourceData.(*client.Client)
			if !ok || resp.DataSourceData != c {
				t.Fatal("resources and data sources did not receive the configured client")
			}
			result, err := c.Do(t.Context(), http.MethodGet, "v1/teams", nil)
			if err != nil {
				t.Fatal(err)
			}
			result.Body.Close()
		})
	}
}

func TestConfigureRejectsMissingEmptyUnknownAndInvalidInputs(t *testing.T) {
	for _, tt := range []struct {
		name            string
		endpoint, token types.String
		environment     bool
	}{
		{"missing endpoint", types.StringNull(), types.StringValue("secret-token"), false},
		{"missing token", types.StringValue("https://localhost"), types.StringNull(), false},
		{"explicit empty endpoint", types.StringValue(""), types.StringValue("secret-token"), true},
		{"explicit empty token", types.StringValue("https://localhost"), types.StringValue(""), true},
		{"unknown endpoint", types.StringUnknown(), types.StringValue("secret-token"), true},
		{"unknown token", types.StringValue("https://localhost"), types.StringUnknown(), true},
		{"invalid endpoint", types.StringValue("https://user:secret-password@localhost"), types.StringValue("secret-token"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ONPAGER_ENDPOINT", "")
			t.Setenv("ONPAGER_TOKEN", "")
			if tt.environment {
				t.Setenv("ONPAGER_ENDPOINT", "https://localhost")
				t.Setenv("ONPAGER_TOKEN", "environment-token")
			}
			resp := configure(t, tt.endpoint, tt.token)
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected configuration diagnostic")
			}
			if resp.ResourceData != nil || resp.DataSourceData != nil {
				t.Fatal("invalid configuration produced a usable client")
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Summary()+d.Detail(), "secret-") {
					t.Fatal("diagnostic discloses sensitive input")
				}
			}
		})
	}
}
