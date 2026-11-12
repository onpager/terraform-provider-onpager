package provider_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Exercises the real plugin protocol and Terraform map/set state across reads,
// including catalog removal and a public sensitivity flag used in an output.
func TestRBACPermissionsDataSourceTerraform(t *testing.T) {
	var empty atomic.Bool
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Method != http.MethodGet || req.URL.Path != "/v1/rbac/permissions" || req.URL.RawQuery != "" {
			t.Errorf("catalog source attempted unexpected operation: %s %s", req.Method, req.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if req.Header.Get("Authorization") != "Bearer metadata-token" || req.Header.Get("Onpager-Timezone") != "UTC" {
			t.Error("catalog source lost provider configuration")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if empty.Load() {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `[{"key":"future.family.action","name":"Future action","description":"","section":"future.family","scopes":["team","tenant"],"actors":["user"],"target":"self","sensitive":true,"custom_grantable":false},{"key":"teams.read","name":"Read teams","description":"Read team metadata","section":"teams","scopes":["tenant","team"],"actors":["service_account","user"],"target":"team","sensitive":false,"custom_grantable":true}]`)
	}))
	defer server.Close()
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required for catalog fixture tests")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	header := fmt.Sprintf(`
terraform {
  required_providers { onpager = { source = "onpager/onpager" } }
}
provider "onpager" {
  endpoint = %q
  token = "metadata-token"
  timezone = "UTC"
}
`, server.URL)
	config := header + `
data "onpager_rbac_permissions" "catalog" {}
output "count" { value = length(data.onpager_rbac_permissions.catalog.permissions) }
output "sensitive_flag" { value = try(data.onpager_rbac_permissions.catalog.permissions["future.family.action"].sensitive, false) }
output "grantable_flag" { value = try(data.onpager_rbac_permissions.catalog.permissions["future.family.action"].custom_grantable, false) }
output "scope_count" { value = try(length(data.onpager_rbac_permissions.catalog.permissions["teams.read"].scopes), 0) }
output "machine_compatible" { value = try(contains(data.onpager_rbac_permissions.catalog.permissions["teams.read"].actors, "service_account"), false) }
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		Steps: []resource.TestStep{
			{Config: config, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("data.onpager_rbac_permissions.catalog", "id", "catalog"),
				resource.TestCheckOutput("count", "2"), resource.TestCheckOutput("sensitive_flag", "true"),
				resource.TestCheckOutput("grantable_flag", "false"), resource.TestCheckOutput("scope_count", "2"),
				resource.TestCheckOutput("machine_compatible", "true"),
			)},
			{Config: config, PlanOnly: true},
			{PreConfig: func() { empty.Store(true) }, Config: config, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("data.onpager_rbac_permissions.catalog", "id", "catalog"),
				resource.TestCheckOutput("count", "0"), resource.TestCheckOutput("sensitive_flag", "false"),
				resource.TestCheckOutput("scope_count", "0"),
			)},
			{Config: config, PlanOnly: true},
			{Config: header + `data "onpager_rbac_permissions" "catalog" { key = "teams.read" }`, ExpectError: regexp.MustCompile("Unsupported argument")},
			{Config: header + `data "onpager_rbac_permissions" "catalog" { id = "catalog" }`, ExpectError: regexp.MustCompile("(Invalid Configuration for Read-Only Attribute|Value for unconfigurable attribute)")},
			{Config: config},
		},
	})
	if calls.Load() == 0 {
		t.Fatal("Terraform did not read the catalog")
	}
}
