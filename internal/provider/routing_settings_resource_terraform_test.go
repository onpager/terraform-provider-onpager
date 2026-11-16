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

func TestRoutingSettingsTerraformLifecycle(t *testing.T) {
	binary, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	settings := client.RoutingSettings{}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.URL.RequestURI() != "/v1/settings/routing" || q.Header.Get("If-Match") != "" {
			t.Error("wrong singleton route/concurrency")
		}
		switch q.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(settings)
		case "PATCH":
			if e := json.NewDecoder(q.Body).Decode(&settings); e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			writes++
			_ = json.NewEncoder(w).Encode(settings)
		default:
			t.Error("unsupported routing operation")
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	config := func(kind, id string) string {
		route := "null"
		if kind != "" {
			route = fmt.Sprintf(`{target_kind=%q,target_id=%q}`, kind, id)
		}
		return fmt.Sprintf(`
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-only"
}
resource "onpager_routing_settings" "test" {
 default_route = %s
}
`, server.URL, route)
	}
	check := func(count int) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if s.RootModule().Resources["onpager_routing_settings.test"].Primary.ID != "tenant" || writes != count {
				return fmt.Errorf("wrong singleton identity or writes=%d want%d", writes, count)
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if settings.DefaultRoute != nil || writes != 5 {
			return fmt.Errorf("destroy did not clear its route, writes=%d", writes)
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config("team", fTeam), Check: check(1)},
		{ResourceName: "onpager_routing_settings.test", ImportState: true, ImportStateVerify: true},
		{Config: config("team", fTeam), PlanOnly: true},
		{Config: config("service", fService), Check: check(2)},
		{PreConfig: func() {
			mu.Lock()
			defer mu.Unlock()
			settings.DefaultRoute = &client.DefaultRoute{TargetKind: "team", TargetID: fOtherTeam}
		}, Config: config("service", fService), Check: check(3)},
		{Config: config("", ""), Check: resource.ComposeTestCheckFunc(check(4), resource.TestCheckNoResourceAttr("onpager_routing_settings.test", "default_route"))},
		{ResourceName: "onpager_routing_settings.test", ImportState: true, ImportStateVerify: true},
		{Config: config("", ""), PlanOnly: true},
	}})
}
