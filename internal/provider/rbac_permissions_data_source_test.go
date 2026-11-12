package provider

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const permissionsMetadataFixture = `[{"key":"future.family.action","name":"Future action","description":"Public metadata","section":"future.family","scopes":["team","tenant"],"actors":["user"],"target":"self","sensitive":true,"custom_grantable":false},{"key":"teams.read","name":"Read teams","description":"","section":"teams","scopes":["tenant","team"],"actors":["user","service_account"],"target":"team","sensitive":false,"custom_grantable":true}]`

// Catches selector/config ownership being introduced into the catalog source,
// or public sensitivity flags being marked as secret Terraform values.
func TestRBACPermissionsDataSourceSchema(t *testing.T) {
	source := NewRBACPermissionsDataSource()
	var metadata datasource.MetadataResponse
	source.Metadata(t.Context(), datasource.MetadataRequest{ProviderTypeName: "onpager"}, &metadata)
	if metadata.TypeName != "onpager_rbac_permissions" {
		t.Fatalf("wrong type name %q", metadata.TypeName)
	}
	var response datasource.SchemaResponse
	source.Schema(t.Context(), datasource.SchemaRequest{}, &response)
	if len(response.Schema.Attributes) != 2 || !response.Schema.Attributes["id"].IsComputed() {
		t.Fatal("catalog source must have only computed id and permissions")
	}
	permissions, ok := response.Schema.Attributes["permissions"].(schema.MapNestedAttribute)
	if !ok || !permissions.IsComputed() || permissions.IsSensitive() || len(permissions.NestedObject.Attributes) != 8 {
		t.Fatal("catalog must expose a computed public metadata map")
	}
	for name, field := range permissions.NestedObject.Attributes {
		if !field.IsComputed() || field.IsRequired() || field.IsOptional() || field.IsSensitive() {
			t.Errorf("%s is configurable or marked sensitive", name)
		}
	}
}

func TestRBACPermissionsDataSourceRead(t *testing.T) {
	calls := 0
	response := lookupRead(t, NewRBACPermissionsDataSource(), nil, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Method != http.MethodGet || req.URL.Path != "/v1/rbac/permissions" || req.URL.RawQuery != "" || req.Header.Get("If-Match") != "" {
			t.Errorf("catalog read made unexpected request %s %s", req.Method, req.URL)
		}
		fmt.Fprint(w, permissionsMetadataFixture)
	})
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	if calls != 1 || lookupString(t, response, "id") != "catalog" {
		t.Fatalf("catalog identity/calls changed: %d", calls)
	}
	var permissions types.Map
	if d := response.State.GetAttribute(t.Context(), path.Root("permissions"), &permissions); d.HasError() || permissions.IsNull() || permissions.IsUnknown() || len(permissions.Elements()) != 2 {
		t.Fatalf("incomplete catalog map: %v: %v", permissions, d)
	}
	for _, tc := range []struct{ key, field, want string }{
		{"future.family.action", "name", "Future action"}, {"future.family.action", "description", "Public metadata"},
		{"future.family.action", "section", "future.family"}, {"future.family.action", "target", "self"},
		{"teams.read", "description", ""},
	} {
		var got types.String
		if d := response.State.GetAttribute(t.Context(), path.Root("permissions").AtMapKey(tc.key).AtName(tc.field), &got); d.HasError() || got.ValueString() != tc.want {
			t.Errorf("%s.%s = %v, want %q: %v", tc.key, tc.field, got, tc.want, d)
		}
	}
	for _, tc := range []struct {
		field string
		want  bool
	}{{"sensitive", true}, {"custom_grantable", false}} {
		var got types.Bool
		if d := response.State.GetAttribute(t.Context(), path.Root("permissions").AtMapKey("future.family.action").AtName(tc.field), &got); d.HasError() || got.IsNull() || got.IsUnknown() || got.ValueBool() != tc.want {
			t.Errorf("%s = %v, want %t: %v", tc.field, got, tc.want, d)
		}
	}
	var actors types.Set
	if d := response.State.GetAttribute(t.Context(), path.Root("permissions").AtMapKey("teams.read").AtName("actors"), &actors); d.HasError() || len(actors.Elements()) != 2 {
		t.Fatalf("actors metadata lost: %v %v", actors, d)
	}
}

func TestRBACPermissionsDataSourceEmptyAndFailures(t *testing.T) {
	response := lookupRead(t, NewRBACPermissionsDataSource(), nil, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `[]`) })
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	var permissions types.Map
	if d := response.State.GetAttribute(t.Context(), path.Root("permissions"), &permissions); d.HasError() || permissions.IsNull() || permissions.IsUnknown() || len(permissions.Elements()) != 0 {
		t.Fatalf("empty catalog not a known empty map: %v %v", permissions, d)
	}
	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			response := lookupRead(t, NewRBACPermissionsDataSource(), nil, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"forbidden"},"secret":"fixture-token"}`)
			})
			if !response.Diagnostics.HasError() || strings.Contains(fmt.Sprint(response.Diagnostics), "fixture-token") {
				t.Fatalf("missing or unsafe catalog diagnostic: %v", response.Diagnostics)
			}
		})
	}
	if response := lookupRead(t, NewRBACPermissionsDataSource(), nil, nil); !response.Diagnostics.HasError() {
		t.Fatal("unconfigured catalog read did not fail")
	}
	source := NewRBACPermissionsDataSource().(datasource.DataSourceWithConfigure)
	var configured datasource.ConfigureResponse
	source.Configure(t.Context(), datasource.ConfigureRequest{ProviderData: struct{}{}}, &configured)
	if !configured.Diagnostics.HasError() {
		t.Fatal("unexpected provider client type was accepted")
	}
}
