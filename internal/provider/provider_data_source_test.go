package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Exercise Terraform's dependency graph: credentials read by a built-in data
// source must configure the real provider before its first resource request.
// The fixture needs neither OnPager nor an external secrets service.
func TestProviderConfigurationFromDataSource(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const token = "data-source-fixture-token"
	var mu sync.Mutex
	active, requests := true, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if req.Header.Get("Onpager-Timezone") != "Europe/Moscow" {
			t.Error("provider did not use the timezone read from the data source")
		}
		if req.Header.Get("Authorization") != "Bearer "+token {
			t.Error("provider did not use the token read from the data source")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "POST" && req.URL.Path == "/v1/teams":
			w.WriteHeader(http.StatusCreated)
		case req.Method == "GET" && req.URL.Path == "/v1/teams/"+id:
		case req.Method == "POST" && req.URL.Path == "/v1/teams/"+id+"/disable":
			active = false
		default:
			t.Error("unexpected provider request")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "data-source-fixture", "active": active})
	}))
	defer server.Close()
	state := map[string]any{
		"version": 4, "serial": 1,
		"lineage": "22222222-2222-4222-8222-222222222222",
		"outputs": map[string]any{
			"timezone": map[string]any{"value": "Europe/Moscow", "type": "string"},
			"endpoint": map[string]any{"value": server.URL, "type": "string"},
			"token":    map[string]any{"value": token, "type": "string", "sensitive": true},
		},
		"resources": []any{},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "credentials.tfstate")
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONPAGER_ENDPOINT", "invalid-environment-endpoint")
	t.Setenv("ONPAGER_TOKEN", "unused-environment-token")
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required for the provider configuration test")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	config := fmt.Sprintf(`
terraform {
  required_providers {
    onpager = { source = "onpager/onpager" }
  }
}
data "terraform_remote_state" "credentials" {
  backend = "local"
  config = { path = %q }
}
provider "onpager" {
  endpoint = data.terraform_remote_state.credentials.outputs.endpoint
  token    = data.terraform_remote_state.credentials.outputs.token
  timezone = data.terraform_remote_state.credentials.outputs.timezone
}
resource "onpager_team" "example" {
  name = "data-source-fixture"
}
`, statePath)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttr("onpager_team.example", "id", id)},
			{Config: config, PlanOnly: true},
		},
	})
	mu.Lock()
	defer mu.Unlock()
	if requests == 0 || active {
		t.Fatal("Terraform did not complete the fixture resource lifecycle")
	}
}
