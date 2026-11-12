package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// The local API fixture implements tuple CAS, so Terraform must update the same
// UUID after configuration changes and an external UI edit, without replacement.
func TestRoleAssignmentTerraformUpdatesAndDriftKeepUUID(t *testing.T) {
	binary, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	records := map[string]client.RoleAssignment{}
	creates, updates, deletes := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.Header.Get("If-Match") != "" || q.URL.RawQuery != "" {
			t.Error("assignment API has no header CAS or discovery queries")
		}
		if q.Method == "POST" && q.URL.Path == "/v1/rbac/assignments" {
			var in client.RoleAssignmentInput
			if err := json.NewDecoder(q.Body).Decode(&in); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			creates++
			id := fmt.Sprintf("%08x-1111-4111-8111-111111111111", creates)
			records[id] = client.RoleAssignment{ID: id, RoleAssignmentInput: in}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(records[id])
			return
		}
		id := strings.TrimPrefix(q.URL.Path, "/v1/rbac/assignments/")
		v, exists := records[id]
		if !exists {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			return
		}
		switch q.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(v)
		case "PUT":
			var in struct {
				client.RoleAssignmentInput
				Expected client.RoleAssignmentInput `json:"expected"`
			}
			if err := json.NewDecoder(q.Body).Decode(&in); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if !reflect.DeepEqual(in.Expected, v.RoleAssignmentInput) {
				t.Error("PUT ignored refreshed state or used desired tuple as CAS")
				w.WriteHeader(412)
				fmt.Fprint(w, `{"error":{"code":"revision_mismatch"}}`)
				return
			}
			updates++
			v.RoleAssignmentInput = in.RoleAssignmentInput
			records[id] = v
			_ = json.NewEncoder(w).Encode(v)
		case "DELETE":
			deletes++
			delete(records, id)
			w.WriteHeader(204)
		default:
			t.Error("unexpected assignment method", q.Method)
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	config := func(changed bool) string {
		role, kind, recipient, target := fService, "service_account", scheduleID, "null"
		if changed {
			role, kind, recipient, target = fOtherTeam, "group", fService, fmt.Sprintf("%q", fOtherTeam)
		}
		return fmt.Sprintf(`
terraform {
 required_providers { onpager = { source = "onpager/onpager" } }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-only"
}
resource "onpager_role_assignment" "test" {
 role_id = %q
 recipient = { kind = %q, id = %q }
 target_team_id = %s
}
`, server.URL, role, kind, recipient, target)
	}
	var id string
	check := func(wantUpdates int) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			current := s.RootModule().Resources["onpager_role_assignment.test"].Primary.ID
			if id == "" {
				id = current
			}
			if current != id || creates != 1 || updates != wantUpdates || deletes != 0 {
				return fmt.Errorf("assignment replaced or not updated: id=%s creates=%d updates=%d deletes=%d", current, creates, updates, deletes)
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if len(records) != 0 || deletes != 1 {
			return fmt.Errorf("assignment destroy did not remove exactly its UUID")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: check(0)},
		{ResourceName: "onpager_role_assignment.test", ImportState: true, ImportStateVerify: true},
		{Config: config(false), PlanOnly: true},
		{Config: config(true), Check: check(1)},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			v := records[id]
			v.RoleID, v.Recipient.Kind, v.Recipient.ID, v.TargetTeamID = fService, "user", scheduleID, nil
			records[id] = v
		}, Config: config(true), Check: check(2)},
		{ResourceName: "onpager_role_assignment.test", ImportState: true, ImportStateVerify: true},
		{Config: config(false), Check: check(3)},
		{Config: config(false), PlanOnly: true},
	}})
}
