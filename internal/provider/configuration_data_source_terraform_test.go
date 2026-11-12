package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// This fixture catches configuration/state selector confusion on a second plan,
// API-side email normalization overwriting input, and failure to defer a selector
// whose value becomes known only after Terraform creates a built-in resource.
func TestConfigurationDataSourcesTerraform(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const team = "22222222-2222-4222-8222-222222222222"
	const exactName = " Team \"東京\" \\ "
	bodies := map[string]any{
		"/v1/users/" + id:             map[string]any{"id": id, "display_name": "Alice", "email_normalized": "alice@example.test", "active": false},
		"/v1/teams/" + team:           map[string]any{"id": team, "name": exactName, "active": false},
		"/v1/services/" + id:          map[string]any{"id": id, "team_id": team, "name": "API", "active": false},
		"/v1/rbac/roles/" + id:        map[string]any{"id": id, "key": "tenant_admin", "name": "Administrator", "description": "", "scope_type": "tenant", "permission_keys": []string{"teams.read"}, "builtin": true, "revision": 1},
		"/v1/groups/" + id:            map[string]any{"id": id, "key": "all_users", "name": "All Users", "description": "", "system": true, "revision": 1},
		"/v1/delivery/channels/" + id: map[string]any{"id": id, "display_name": "Unavailable", "kind": "missing", "contract_version": "1", "capabilities": []string{}, "enabled": false, "unavailable_reason": "provider_unavailable", "config": map[string]string{"secret": "must-never-enter-state"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			t.Errorf("data source attempted mutation: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if req.Header.Get("Authorization") != "Bearer fixture-token" || req.Header.Get("Onpager-Timezone") != "UTC" {
			t.Error("data source lost the provider authentication/timezone context")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if body, ok := bodies[req.URL.Path]; ok {
			if req.URL.RawQuery != "" {
				t.Error("direct read unexpectedly included query parameters")
			}
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		var filters []struct{ Field, Op, Value string }
		if err := json.Unmarshal([]byte(req.URL.Query().Get("filter")), &filters); err != nil || len(filters) == 0 || req.URL.Query().Get("limit") != "2" || len(req.URL.Query()) != 2 {
			t.Errorf("missing exact singular filter: %s", req.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wanted := map[string][][2]string{
			"/v1/teams":      {{"name", exactName}},
			"/v1/services":   {{"name", "API"}, {"team_id", team}},
			"/v1/rbac/roles": {{"key", "tenant_admin"}},
		}
		switch req.URL.Path {
		case "/v1/users":
			if len(filters) != 1 || (filters[0].Field != "email" && filters[0].Field != "local_login") || filters[0].Value != " Alice@Example.TEST " {
				t.Error("user selector was normalized, replaced or combined")
			}
		case "/v1/groups":
			if len(filters) != 1 || !((filters[0].Field == "key" && filters[0].Value == "all_users") || (filters[0].Field == "name" && filters[0].Value == "All Users")) {
				t.Error("incorrect group selector")
			}
		default:
			pairs, ok := wanted[req.URL.Path]
			if !ok || len(pairs) != len(filters) {
				t.Errorf("unexpected catalog route/filter: %s", req.URL)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			for i, pair := range pairs {
				if filters[i].Field != pair[0] || filters[i].Value != pair[1] {
					t.Errorf("exact selector altered: %s", req.URL)
				}
			}
		}
		for _, filter := range filters {
			if filter.Op != "equal" {
				t.Error("non-exact selector emitted")
			}
		}
		selectedID := id
		if req.URL.Path == "/v1/teams" {
			selectedID = team
		}
		fmt.Fprintf(w, `{"items":[{"id":%q}],"has_more":false}`, selectedID)
	}))
	defer server.Close()
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required for data-source fixture tests")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	config := fmt.Sprintf(`
terraform {
  required_providers {
    onpager = { source = "onpager/onpager" }
  }
}
provider "onpager" {
  endpoint = %q
  token = "fixture-token"
  timezone = "UTC"
}
resource "terraform_data" "identity" {
  input = %q
}
data "onpager_user" "email" {
  email = " Alice@Example.TEST "
}
data "onpager_user" "login" {
  local_login = " Alice@Example.TEST "
}
data "onpager_team" "name" {
  name = %q
}
data "onpager_team" "deferred" {
  id = terraform_data.identity.output
}
data "onpager_service" "name" {
  name = "API"
  team_id = data.onpager_team.name.id
}
data "onpager_rbac_role" "builtin" {
  key = "tenant_admin"
}
data "onpager_group" "key" {
  key = "all_users"
}
data "onpager_group" "name" {
  name = "All Users"
}
data "onpager_delivery_channel" "unavailable" {
  id = %q
}
`, server.URL, team, exactName, id)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		Steps: []resource.TestStep{
			{Config: config, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("data.onpager_user.email", "email", " Alice@Example.TEST "),
				resource.TestCheckResourceAttr("data.onpager_user.email", "email_normalized", "alice@example.test"),
				resource.TestCheckResourceAttr("data.onpager_user.email", "active", "false"),
				resource.TestCheckResourceAttr("data.onpager_user.login", "local_login", " Alice@Example.TEST "),
				resource.TestCheckNoResourceAttr("data.onpager_user.login", "email"),
				resource.TestCheckResourceAttr("data.onpager_team.name", "name", exactName),
				resource.TestCheckResourceAttr("data.onpager_team.deferred", "id", team),
				resource.TestCheckResourceAttr("data.onpager_team.deferred", "name", exactName),
				resource.TestCheckResourceAttr("data.onpager_service.name", "team_id", team),
				resource.TestCheckResourceAttr("data.onpager_rbac_role.builtin", "builtin", "true"),
				resource.TestCheckResourceAttr("data.onpager_rbac_role.builtin", "permission_keys.#", "1"),
				resource.TestCheckResourceAttr("data.onpager_group.key", "name", "All Users"),
				resource.TestCheckResourceAttr("data.onpager_group.name", "key", "all_users"),
				resource.TestCheckResourceAttr("data.onpager_delivery_channel.unavailable", "enabled", "false"),
				resource.TestCheckResourceAttr("data.onpager_delivery_channel.unavailable", "capabilities.#", "0"),
				resource.TestCheckResourceAttr("data.onpager_delivery_channel.unavailable", "unavailable_reason", "provider_unavailable"),
				resource.TestCheckNoResourceAttr("data.onpager_delivery_channel.unavailable", "config"),
			)},
			{Config: config, PlanOnly: true},
		},
	})
}
