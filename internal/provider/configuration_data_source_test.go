package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const lookupFixtureID = "11111111-1111-4111-8111-111111111111"
const lookupFixtureTeam = "22222222-2222-4222-8222-222222222222"

func lookupConfig(t *testing.T, source datasource.DataSource, fields map[string]any) tfsdk.Config {
	t.Helper()
	var response datasource.SchemaResponse
	source.Schema(t.Context(), datasource.SchemaRequest{}, &response)
	s := response.Schema
	objectType := s.Type().TerraformType(t.Context()).(tftypes.Object)
	values := make(map[string]tftypes.Value, len(objectType.AttributeTypes))
	for name, typ := range objectType.AttributeTypes {
		values[name] = tftypes.NewValue(typ, fields[name])
	}
	return tfsdk.Config{Schema: s, Raw: tftypes.NewValue(objectType, values)}
}

func lookupSources() map[string]func() datasource.DataSource {
	return map[string]func() datasource.DataSource{
		"user": NewUserDataSource, "team": NewTeamDataSource, "service": NewServiceDataSource,
		"rbac_role": NewRBACRoleDataSource, "group": NewGroupDataSource, "delivery_channel": NewDeliveryChannelDataSource,
	}
}

// These cases catch accepting invalid selectors before HTTP, counting computed
// state as configuration, or rejecting unknown selectors during graph validation.
func TestConfigurationDataSourceSelectors(t *testing.T) {
	cases := []struct {
		name, kind string
		fields     map[string]any
		wantError  bool
	}{
		{"user none", "user", nil, true},
		{"user two", "user", map[string]any{"id": lookupFixtureID, "email": "alice@example.test"}, true},
		{"user unknown", "user", map[string]any{"email": tftypes.UnknownValue}, false},
		{"user known and unknown", "user", map[string]any{"id": lookupFixtureID, "email": tftypes.UnknownValue}, false},
		{"user two known and unknown", "user", map[string]any{"id": lookupFixtureID, "email": "alice@example.test", "local_login": tftypes.UnknownValue}, true},
		{"user blank", "user", map[string]any{"local_login": " \t "}, true},
		{"user normalized by API", "user", map[string]any{"email": " Alice@Example.TEST "}, false},
		{"team UUID", "team", map[string]any{"id": lookupFixtureID}, false},
		{"team invalid UUID", "team", map[string]any{"id": "not-a-uuid"}, true},
		{"team nil UUID", "team", map[string]any{"id": "00000000-0000-0000-0000-000000000000"}, true},
		{"team exact spaces", "team", map[string]any{"name": " Team \"東京\" \\ "}, false},
		{"team blank", "team", map[string]any{"name": " \t "}, true},
		{"team too many UTF8 bytes", "team", map[string]any{"name": strings.Repeat("界", 1366)}, true},
		{"team maximum bytes", "team", map[string]any{"name": strings.Repeat("a", 4096)}, false},
		{"service owner only", "service", map[string]any{"team_id": lookupFixtureTeam}, true},
		{"service owner", "service", map[string]any{"name": "API", "team_id": lookupFixtureTeam}, false},
		{"service invalid owner", "service", map[string]any{"name": "API", "team_id": "bad"}, true},
		{"role builtin key", "rbac_role", map[string]any{"key": "tenant_admin"}, false},
		{"role two", "rbac_role", map[string]any{"id": lookupFixtureID, "key": "tenant_admin"}, true},
		{"group name", "group", map[string]any{"name": "Group"}, false},
		{"group two", "group", map[string]any{"name": "Group", "key": "group"}, true},
		{"channel absent", "delivery_channel", nil, true},
		{"channel unknown", "delivery_channel", map[string]any{"id": tftypes.UnknownValue}, false},
		{"channel UUID", "delivery_channel", map[string]any{"id": lookupFixtureID}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := lookupSources()[tc.kind]()
			var response datasource.ValidateConfigResponse
			source.(datasource.DataSourceWithValidateConfig).ValidateConfig(t.Context(), datasource.ValidateConfigRequest{Config: lookupConfig(t, source, tc.fields)}, &response)
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("error = %t, want %t: %v", response.Diagnostics.HasError(), tc.wantError, response.Diagnostics)
			}
		})
	}
}

func lookupRead(t *testing.T, source datasource.DataSource, fields map[string]any, handler http.HandlerFunc) datasource.ReadResponse {
	t.Helper()
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		c, err := client.New(server.URL, "fixture-token")
		if err != nil {
			t.Fatal(err)
		}
		var configured datasource.ConfigureResponse
		source.(datasource.DataSourceWithConfigure).Configure(t.Context(), datasource.ConfigureRequest{ProviderData: c}, &configured)
		if configured.Diagnostics.HasError() {
			t.Fatal(configured.Diagnostics)
		}
	}
	config := lookupConfig(t, source, fields)
	response := datasource.ReadResponse{State: tfsdk.State{Schema: config.Schema.(dsschema.Schema)}}
	source.Read(t.Context(), datasource.ReadRequest{Config: config}, &response)
	return response
}

func lookupString(t *testing.T, response datasource.ReadResponse, name string) string {
	t.Helper()
	var value types.String
	if d := response.State.GetAttribute(t.Context(), path.Root(name), &value); d.HasError() {
		t.Fatal(d)
	}
	return value.ValueString()
}

func TestConfigurationDataSourceByIDObservedMetadata(t *testing.T) {
	cases := []struct {
		kind, route, body string
		strings           map[string]string
		boolName          string
		boolValue         bool
	}{
		{"user", "/v1/users/", `{"id":%q,"display_name":"Alice","email_normalized":"alice@example.test","active":false}`, map[string]string{"display_name": "Alice", "email_normalized": "alice@example.test"}, "active", false},
		{"team", "/v1/teams/", `{"id":%q,"name":" Team ","active":false}`, map[string]string{"name": " Team "}, "active", false},
		{"service", "/v1/services/", `{"id":%q,"name":"API","team_id":"22222222-2222-4222-8222-222222222222","active":false}`, map[string]string{"name": "API", "team_id": lookupFixtureTeam}, "active", false},
		{"rbac_role", "/v1/rbac/roles/", `{"id":%q,"key":"tenant_admin","name":"Administrator","description":"Builtin role","scope_type":"tenant","permission_keys":["teams.read"],"builtin":true,"revision":1}`, map[string]string{"key": "tenant_admin", "scope_type": "tenant", "description": "Builtin role"}, "builtin", true},
		{"group", "/v1/groups/", `{"id":%q,"key":"all_users","name":"All Users","description":"Derived group","system":true,"revision":1}`, map[string]string{"key": "all_users", "name": "All Users", "description": "Derived group"}, "system", true},
		{"delivery_channel", "/v1/delivery/channels/", `{"id":%q,"display_name":"Disabled channel","kind":"unknown","contract_version":"1","capabilities":[],"enabled":false,"unavailable_reason":"provider_unavailable","config":{"must_not_expose":"secret"}}`, map[string]string{"display_name": "Disabled channel", "kind": "unknown", "unavailable_reason": "provider_unavailable", "contract_version": "1"}, "enabled", false},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			response := lookupRead(t, lookupSources()[tc.kind](), map[string]any{"id": lookupFixtureID}, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != tc.route+lookupFixtureID || req.URL.RawQuery != "" {
					t.Errorf("UUID read made unexpected request: %s %s", req.Method, req.URL)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("ETag", `"1"`)
				fmt.Fprintf(w, tc.body, lookupFixtureID)
			})
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			if got := lookupString(t, response, "id"); got != lookupFixtureID {
				t.Fatalf("id = %q", got)
			}
			for name, want := range tc.strings {
				if got := lookupString(t, response, name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			var observed types.Bool
			if d := response.State.GetAttribute(t.Context(), path.Root(tc.boolName), &observed); d.HasError() || observed.ValueBool() != tc.boolValue {
				t.Fatalf("observed %s = %v: %v", tc.boolName, observed, d)
			}
		})
	}
}

func TestUserDataSourcePreservesNormalizedSelectorInputs(t *testing.T) {
	for _, selector := range []string{"email", "local_login"} {
		t.Run(selector, func(t *testing.T) {
			input := " Alice@Example.TEST "
			response := lookupRead(t, NewUserDataSource(), map[string]any{selector: input}, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					t.Fatal("data source attempted a mutation")
				}
				switch req.URL.Path {
				case "/v1/users":
					var filter []struct{ Field, Op, Value string }
					if err := json.Unmarshal([]byte(req.URL.Query().Get("filter")), &filter); err != nil || len(filter) != 1 || filter[0].Field != selector || filter[0].Op != "equal" || filter[0].Value != input || req.URL.Query().Get("limit") != "2" {
						t.Errorf("incorrect exact selector: %s", req.URL.RawQuery)
					}
					fmt.Fprintf(w, `{"items":[{"id":%q}],"has_more":false}`, lookupFixtureID)
				case "/v1/users/" + lookupFixtureID:
					fmt.Fprintf(w, `{"id":%q,"display_name":"Alice","email_normalized":"alice@example.test","active":true}`, lookupFixtureID)
				default:
					t.Error("unexpected user route")
					w.WriteHeader(404)
				}
			})
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			if got := lookupString(t, response, selector); got != input {
				t.Errorf("configured %s changed to %q", selector, got)
			}
			if got := lookupString(t, response, "email_normalized"); got != "alice@example.test" {
				t.Errorf("normalized observed email = %q", got)
			}
		})
	}
}

func TestConfigurationDataSourceReadFailures(t *testing.T) {
	for kind, constructor := range lookupSources() {
		for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
			t.Run(fmt.Sprintf("%s/%d", kind, status), func(t *testing.T) {
				response := lookupRead(t, constructor(), map[string]any{"id": lookupFixtureID}, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":{"code":"forbidden"},"secret":"fixture-token"}`)
				})
				if !response.Diagnostics.HasError() {
					t.Fatal("failed read did not produce an error")
				}
				if strings.Contains(fmt.Sprint(response.Diagnostics), "fixture-token") {
					t.Fatal("error exposed raw response secret")
				}
			})
		}
		t.Run(kind+"/unconfigured", func(t *testing.T) {
			response := lookupRead(t, constructor(), map[string]any{"id": lookupFixtureID}, nil)
			if !response.Diagnostics.HasError() {
				t.Fatal("unconfigured client did not produce an error")
			}
		})
	}
}

func TestServiceDataSourceRejectsUnexpectedOwner(t *testing.T) {
	response := lookupRead(t, NewServiceDataSource(), map[string]any{"id": lookupFixtureID, "team_id": lookupFixtureID}, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/services/"+lookupFixtureID {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL)
		}
		fmt.Fprintf(w, `{"id":%q,"name":"API","team_id":%q,"active":true}`, lookupFixtureID, lookupFixtureTeam)
	})
	if !response.Diagnostics.HasError() {
		t.Fatal("service owner constraint was ignored for UUID lookup")
	}
}
