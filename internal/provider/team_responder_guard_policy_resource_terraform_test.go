package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Exercises real Terraform planning, unknown team references, publication
// receipts, read-only import, external drift/reset, stable plans and destroy.
func TestTeamResponderGuardPolicyTerraformLifecycle(t *testing.T) {
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	var revision, counter int64
	limit := int64(357)
	isDefault := true
	publications, resets := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.URL.Path != "/v1/teams/"+fTeam+"/responder-guard-policies" || q.URL.RawQuery != "" || q.Header.Get("If-Match") != "" || q.Header.Get("Authorization") != "Bearer fixture-token" || q.Header.Get("Onpager-Timezone") != "UTC" {
			t.Errorf("wrong guard policy request: %s %s", q.Method, q.URL)
			w.WriteHeader(400)
			return
		}
		switch q.Method {
		case "POST":
			var b map[string]int64
			if json.NewDecoder(q.Body).Decode(&b) != nil || len(b) != 1 || b["max_open_requests_per_subject"] < 1 || b["max_open_requests_per_subject"] > 1000 {
				t.Error("publication omitted explicit bounded limit or added fake CAS")
				w.WriteHeader(422)
				return
			}
			limit, isDefault = b["max_open_requests_per_subject"], false
			counter++
			revision = counter
			publications++
			w.WriteHeader(201)
		case "DELETE":
			if q.ContentLength != 0 {
				t.Error("reset sent a guessed default")
			}
			isDefault, revision, limit = true, 0, 271
			resets++
			w.WriteHeader(204)
			return
		case "GET":
		default:
			t.Error("unexpected policy method")
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"max_open_requests_per_subject": limit, "revision": revision, "is_default": isDefault})
	}))
	defer server.Close()
	config := func(limit int64) string {
		return fmt.Sprintf(`
terraform {
 required_providers { onpager = { source = "onpager/onpager" } }
}
provider "onpager" {
 endpoint=%q
 token="fixture-token"
 timezone="UTC"
}
resource "terraform_data" "team" { input=%q }
resource "onpager_team_responder_guard_policy" "test" {
 team_id=terraform_data.team.output
 max_open_requests_per_subject=%d
}
`, server.URL, fTeam, limit)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if !isDefault || revision != 0 || resets != 1 || publications != 4 {
				return fmt.Errorf("unexpected publication/reset lifecycle: default=%t revision=%d publications=%d resets=%d", isDefault, revision, publications, resets)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(1), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.test", "id", fTeam), resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.test", "team_id", fTeam), resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.test", "max_open_requests_per_subject", "1"), resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.test", "revision", "1"))},
			{ResourceName: "onpager_team_responder_guard_policy.test", ImportState: true, ImportStateId: fTeam, ImportStateVerify: true},
			{Config: config(1), PlanOnly: true},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); limit = 9; counter++; revision = counter }, Config: config(1), Check: resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.test", "max_open_requests_per_subject", "1")},
			{Config: config(1), PlanOnly: true},
			{Config: config(1000), Check: resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.test", "max_open_requests_per_subject", "1000")},
			{Config: config(1000), PlanOnly: true},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); isDefault, revision, limit = true, 0, 271 }, Config: config(1000), Check: resource.TestCheckResourceAttr("onpager_team_responder_guard_policy.test", "max_open_requests_per_subject", "1000")},
			{Config: config(1000), PlanOnly: true},
		},
	})
}
