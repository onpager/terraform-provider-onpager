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
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Terraform must see a stable set/default plan, import disabled rules without
// writes, reconcile drift with the observed own CAS, and recreate only absence.
func TestInhibitionTerraformPlansImportDriftAndProjection(t *testing.T) {
	binary, e := exec.LookPath("terraform")
	if e != nil {
		t.Fatal("installed Terraform CLI is required for inhibition planning tests")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	records := map[string]client.InhibitionRule{}
	creates, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := ""
		if q.URL.Path != "/v1/inhibition-rules" {
			if len(q.URL.Path) <= len("/v1/inhibition-rules/") {
				t.Errorf("unexpected route %s", q.URL)
				w.WriteHeader(404)
				return
			}
			id = q.URL.Path[len("/v1/inhibition-rules/"):]
		}
		if q.Method == "POST" && id == "" {
			var in client.InhibitionInput
			if json.NewDecoder(q.Body).Decode(&in) != nil {
				t.Error("invalid create JSON")
				w.WriteHeader(422)
				return
			}
			creates++
			writes++
			id = fmt.Sprintf("%08x-1111-4111-8111-111111111111", creates)
			records[id] = client.InhibitionRule{ID: id, Revision: 1, IndexState: "ready", InhibitionInput: in}
			w.WriteHeader(201)
		}
		v, ok := records[id]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			return
		}
		if q.Method == "PUT" || q.Method == "DELETE" {
			if q.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, v.Revision) {
				t.Errorf("wrong own CAS %q wanted %d", q.Header.Get("If-Match"), v.Revision)
				w.WriteHeader(412)
				return
			}
			writes++
			if q.Method == "DELETE" {
				delete(records, id)
				w.WriteHeader(204)
				return
			}
			var in client.InhibitionInput
			if json.NewDecoder(q.Body).Decode(&in) != nil {
				t.Error("invalid update JSON")
				w.WriteHeader(422)
				return
			}
			v.InhibitionInput = in
			v.Revision++
			v.IndexState = "pending"
			records[id] = v
			json.NewEncoder(w).Encode(map[string]any{"id": id, "revision": v.Revision})
			return
		}
		// Independent projection transitions and matcher order are not configuration.
		if len(v.Matchers) == 2 {
			v.Matchers = []client.InhibitionMatcher{v.Matchers[1], v.Matchers[0]}
		}
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(server.Close)
	header := fmt.Sprintf(`
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-only"
}
`, server.URL)
	config := func(enabled string) string {
		return header + fmt.Sprintf(`
resource "onpager_inhibition_rule" "test" {
 name = "Passive"
 source_expression = "false"
 target_expression = "false"
 resolved_target_action = "keep"
 %s
 matchers = [{source = " app/name ", target = "target.name"}, {source = "cluster", target = "cluster"}]
}
`, enabled)
	}
	var id string
	capture := func(s *terraform.State) error {
		id = s.RootModule().Resources["onpager_inhibition_rule.test"].Primary.ID
		return nil
	}
	currentWrites := 0
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if len(records) != 0 {
			return fmt.Errorf("destroy retained live inhibition configuration")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config("enabled = false"), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_inhibition_rule.test", "enabled", "false"))},
		{ResourceName: "onpager_inhibition_rule.test", ImportState: true, ImportStateVerify: true},
		{Config: config("enabled = false"), PlanOnly: true},
		{Config: config(""), Check: resource.TestCheckResourceAttr("onpager_inhibition_rule.test", "enabled", "true")},
		{Config: config(""), PlanOnly: true},
		{Config: config("enabled = false"), Check: resource.TestCheckResourceAttr("onpager_inhibition_rule.test", "enabled", "false")},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			v := records[id]
			v.IndexState = "obsolete"
			records[id] = v
			currentWrites = writes
		}, Config: config("enabled = false"), PlanOnly: true},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			if writes != currentWrites {
				t.Fatal("computed projection drift caused a write")
			}
			v := records[id]
			v.Name = "Externally changed"
			v.Revision++
			records[id] = v
		}, Config: config("enabled = false"), Check: resource.TestCheckResourceAttr("onpager_inhibition_rule.test", "name", "Passive")},
		{PreConfig: func() { mu.Lock(); defer mu.Unlock(); delete(records, id) }, Config: config("enabled = false"), Check: capture},
		{ResourceName: "onpager_inhibition_rule.test", ImportState: true, ImportStateVerify: true},
		{Config: config("enabled = false"), PlanOnly: true},
	}})
	mu.Lock()
	defer mu.Unlock()
	if creates != 2 {
		t.Fatalf("create count=%d; non-absence replaced the UUID", creates)
	}
}
