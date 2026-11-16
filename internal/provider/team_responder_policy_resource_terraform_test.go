package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Exercises the real protocol planner: unknown references, two independent
// slots, canonical snapshot metadata, trigger-only republish, import and drift.
func TestTeamResponderPolicyTerraformLifecycle(t *testing.T) {
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	slots := map[string]map[string]any{}
	revisions := map[string]int{"default": 0, "important": 0}
	publications := map[string]int{"default": 0, "important": 0}
	resets := map[string]int{}
	chainWait := 60
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.URL.Path != "/v1/teams/"+fTeam+"/responder-policies" || q.Header.Get("Authorization") != "Bearer fixture-token" || q.Header.Get("Onpager-Timezone") != "UTC" || q.Header.Get("If-Match") != "" {
			t.Error("policy used discovery, operational API, or fake CAS")
			w.WriteHeader(400)
			return
		}
		importance := q.URL.Query().Get("importance")
		switch q.Method {
		case "GET":
			if q.URL.RawQuery != "importance=default" && q.URL.RawQuery != "importance=important" {
				t.Error("read omitted explicit exact importance")
				w.WriteHeader(400)
				return
			}
			if slots[importance] == nil {
				fmt.Fprint(w, responderPolicyZero)
				return
			}
		case "POST":
			var body map[string]json.RawMessage
			if json.NewDecoder(q.Body).Decode(&body) != nil || q.URL.RawQuery != "" {
				t.Error("malformed publication")
				w.WriteHeader(400)
				return
			}
			var strategy, chain string
			_ = json.Unmarshal(body["importance"], &importance)
			_ = json.Unmarshal(body["strategy"], &strategy)
			_ = json.Unmarshal(body["chain_id"], &chain)
			expectedFields := 2
			steps := []map[string]any{}
			chainID := "00000000-0000-0000-0000-000000000000"
			if strategy == "team_chain" {
				expectedFields = 3
				chainID = fService
				steps = []map[string]any{{"kind": "notify_user", "target_user_id": fOtherTeam, "importance": "important"}, {"kind": "wait", "wait_seconds": chainWait}}
				if chain != fService {
					t.Error("unknown chain reference lost")
				}
			} else if strategy != "team_fanout" || chain != "" {
				t.Error("invalid strategy")
			}
			if len(body) != expectedFields || importance != "default" && importance != "important" {
				t.Error("publication sent compatibility steps, trigger or revision")
				w.WriteHeader(422)
				return
			}
			revisions[importance]++
			publications[importance]++
			slots[importance] = map[string]any{"team_id": fTeam, "importance": importance, "strategy": strategy, "chain_id": chainID, "revision": revisions[importance], "chain_steps": steps}
			w.WriteHeader(201)
		case "DELETE":
			raw, _ := io.ReadAll(q.Body)
			if len(raw) != 0 || q.URL.RawQuery != "importance=default" && q.URL.RawQuery != "importance=important" {
				t.Error("reset did not isolate the exact slot")
				w.WriteHeader(400)
				return
			}
			delete(slots, importance)
			resets[importance]++
			w.WriteHeader(204)
			return
		default:
			t.Error("unexpected operational request")
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(slots[importance])
	}))
	defer server.Close()
	config := func(phase int) string {
		strategy := "team_chain"
		chainAndTrigger := `chain_id=terraform_data.chain.output
 publication_trigger=terraform_data.publication.output`
		if phase == 2 {
			strategy = "team_fanout"
			chainAndTrigger = ""
		}
		trigger := "version-1"
		if phase > 0 {
			trigger = "version-2"
		}
		return fmt.Sprintf(`
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint=%q
 token="fixture-token"
 timezone="UTC"
}
resource "terraform_data" "chain" { input=%q }
resource "terraform_data" "publication" { input=%q }
resource "onpager_team_responder_policy" "other" {
 team_id=%q
 importance="default"
 strategy="team_fanout"
}
resource "onpager_team_responder_policy" "test" {
 team_id=%q
 importance="important"
 strategy=%q
 %s
}
`, server.URL, fService, trigger, fTeam, fTeam, strategy, chainAndTrigger)
	}
	checkCounts := func(important int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if publications["important"] != important || publications["default"] != 1 {
				return fmt.Errorf("unexpected publication counts: important=%d default=%d", publications["important"], publications["default"])
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if len(slots) != 0 || resets["default"] != 1 || resets["important"] != 1 || revisions["important"] != 5 {
				return fmt.Errorf("destroy failed to isolate slots or lost history: slots=%v resets=%v revisions=%v", slots, resets, revisions)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(0), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "id", fTeam+"/important"), resource.TestCheckResourceAttr("onpager_team_responder_policy.other", "id", fTeam+"/default"), resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "chain_steps.#", "2"), resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "chain_steps.1.wait_seconds", "60"), resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "publication_trigger", "version-1"), checkCounts(1))},
			{ResourceName: "onpager_team_responder_policy.test", ImportState: true, ImportStateId: fTeam + "/important", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"publication_trigger"}},
			{Config: config(0), PlanOnly: true},
			{PreConfig: func() { mu.Lock(); chainWait = 120; mu.Unlock() }, Config: config(0), PlanOnly: true},
			{Config: config(1), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "chain_steps.1.wait_seconds", "120"), resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "publication_trigger", "version-2"), checkCounts(2))},
			{Config: config(1), PlanOnly: true},
			{PreConfig: func() {
				mu.Lock()
				defer mu.Unlock()
				revisions["important"]++
				slots["important"] = map[string]any{"team_id": fTeam, "importance": "important", "strategy": "team_fanout", "revision": revisions["important"], "chain_id": "00000000-0000-0000-0000-000000000000", "chain_steps": []any{}}
			}, Config: config(1), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "strategy", "team_chain"), checkCounts(3))},
			{Config: config(2), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_team_responder_policy.test", "chain_steps.#", "0"), resource.TestCheckNoResourceAttr("onpager_team_responder_policy.test", "chain_id"), resource.TestCheckNoResourceAttr("onpager_team_responder_policy.test", "publication_trigger"), checkCounts(4))},
			{Config: config(2), PlanOnly: true},
		},
	})
}
