package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Exercises real Terraform set planning, unknown role references, duplicate
// configuration values, import, external drift, empty ownership and destroy.
func TestTeamRoleDefaultsTerraformLifecycle(t *testing.T) {
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	revision := int64(1)
	members, leads := []string{}, []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.URL.Path != "/v1/rbac/team-defaults" || q.URL.RawQuery != "" || q.Header.Get("Authorization") != "Bearer fixture-token" || q.Header.Get("Onpager-Timezone") != "UTC" {
			t.Errorf("unexpected policy request: %s %s", q.Method, q.URL.Path)
			w.WriteHeader(400)
			return
		}
		if q.Method == "PUT" {
			if q.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, revision) {
				w.WriteHeader(412)
				fmt.Fprint(w, `{"error":{"code":"revision_mismatch"}}`)
				return
			}
			var b struct {
				Members []string `json:"members"`
				Leads   []string `json:"leads"`
			}
			if err := json.NewDecoder(q.Body).Decode(&b); err != nil || b.Members == nil || b.Leads == nil {
				t.Error("policy replacement omitted an explicit array")
				w.WriteHeader(422)
				return
			}
			members, leads = b.Members, b.Leads
			slices.Reverse(members) // Server order is unrelated to Terraform set order.
			slices.Reverse(leads)
			revision++
		} else if q.Method != "GET" || q.Header.Get("If-Match") != "" {
			t.Error("unexpected policy method or mutation header on read")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, revision))
		_ = json.NewEncoder(w).Encode(map[string]any{"members": members, "leads": leads, "revision": revision, "display": map[string]any{"roles": []any{}}})
	}))
	defer server.Close()
	config := func(mode int) string {
		memberValues, leadValues := fmt.Sprintf(`[terraform_data.role.output,terraform_data.role.output,%q]`, fService), "[]"
		if mode == 1 {
			memberValues, leadValues = "[]", fmt.Sprintf(`[%q]`, fTeam)
		} else if mode == 2 {
			memberValues, leadValues = "[]", "[]"
		}
		return fmt.Sprintf(`
terraform {
 required_providers { onpager = { source = "onpager/onpager" } }
}
provider "onpager" {
 endpoint=%q
 token="fixture-token"
 timezone="UTC"
}
resource "terraform_data" "role" { input=%q }
resource "onpager_team_role_defaults" "test" {
 members=%s
 leads=%s
}
`, server.URL, fTeam, memberValues, leadValues)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if len(members) != 0 || len(leads) != 0 {
				return fmt.Errorf("destroy retained dynamic team grants")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(0), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "id", "tenant"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "members.#", "2"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "leads.#", "0"))},
			{ResourceName: "onpager_team_role_defaults.test", ImportState: true, ImportStateId: "tenant", ImportStateVerify: true},
			{Config: config(0), PlanOnly: true},
			{PreConfig: func() {
				mu.Lock()
				defer mu.Unlock()
				members, leads = []string{fOtherTeam}, []string{}
				revision++
			}, Config: config(0), Check: resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "members.#", "2")},
			{Config: config(0), PlanOnly: true},
			{Config: config(1), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "members.#", "0"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "leads.#", "1"))},
			{Config: config(1), PlanOnly: true},
			{Config: config(2), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "id", "tenant"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "members.#", "0"), resource.TestCheckResourceAttr("onpager_team_role_defaults.test", "leads.#", "0"))},
			{Config: config(2), PlanOnly: true},
		},
	})
}
