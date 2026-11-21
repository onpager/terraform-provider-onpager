package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestProvisionDeliveryTargetTerraformLifecycle(t *testing.T) {
	const address = "onpager_delivery_target.team"
	const catalog = "onpager_team_alert_channel.warning"
	const imported = "66666666-6666-4666-8666-666666666666"
	const first = "77777777-7777-4777-8777-777777777777"
	const second = "88888888-8888-4888-8888-888888888888"
	const bindingID = "99999999-9999-4999-8999-999999999999"
	const targets = "/v1/delivery/targets"
	const bindings = "/v1/teams/" + fTeam + "/alert-channels"

	var mu sync.Mutex
	enabled := map[string]bool{imported: true}
	bound := ""
	var writes []string
	header := provisionDeliveryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		if r.Method == "POST" || r.Method == "PUT" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
		}
		target := strings.TrimPrefix(r.URL.Path, targets+"/")
		on, exists := enabled[target]
		switch {
		case r.Method == "POST" && r.URL.Path == targets:
			snapshot, ok := body["snapshot"].(map[string]any)
			if !ok || len(body) != 2 || body["channel_id"] != fService || snapshot["BotID"] != fService ||
				snapshot["Kind"] != "conversation" || !reflect.DeepEqual(snapshot["Scope"], map[string]any{"Kind": "workspace", "ID": "example"}) {
				t.Error("target creation did not send the declared snapshot object and its channel")
				w.WriteHeader(422)
				return
			}
			switch snapshot["ChannelName"] {
			case "primary":
				target = first
			case "secondary":
				target = second
			default:
				t.Error("unexpected target address")
				w.WriteHeader(422)
				return
			}
			if _, duplicate := enabled[target]; duplicate {
				w.WriteHeader(409)
				return
			}
			enabled[target] = true
			writes = append(writes, "create "+target)
			w.WriteHeader(201)
		case r.Method == "GET" && exists && r.URL.Path == targets+"/"+target:
		case r.Method == "PUT" && exists && r.URL.Path == targets+"/"+target:
			flag, ok := body["enabled"].(bool)
			if !ok || len(body) != 1 {
				t.Error("target update must only send enabled")
				w.WriteHeader(422)
				return
			}
			enabled[target] = flag
			writes = append(writes, fmt.Sprintf("set %t %s", flag, target))
			w.WriteHeader(204)
			return
		case r.Method == "DELETE" && exists && r.URL.Path == targets+"/"+target:
			enabled[target] = false
			writes = append(writes, "disable "+target)
			w.WriteHeader(204)
			return
		case (r.Method == "POST" && r.URL.Path == bindings) || (r.Method == "PUT" && r.URL.Path == bindings+"/"+bindingID):
			id, ok := body["target_id"].(string)
			if !ok || len(body) != 2 || body["channel_id"] != fService || !enabled[id] {
				t.Error("binding did not reference the enabled target of its configured channel")
				w.WriteHeader(422)
				return
			}
			bound = id
		case r.Method == "GET" && r.URL.Path == bindings+"/"+bindingID && bound != "":
		case r.Method == "DELETE" && r.URL.Path == bindings+"/"+bindingID && bound != "":
			bound = ""
			w.WriteHeader(204)
			return
		default:
			t.Errorf("unexpected operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		if strings.HasPrefix(r.URL.Path, bindings) {
			fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"target_id":%q,"unavailable":%t}`, bindingID, fService, bound, !enabled[bound])
			return
		}
		if r.Method == "POST" {
			on = true
		}
		fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"kind":"bot","contract_version":"unstable/v1","enabled":%t}`, target, fService, on)
	})
	importedConfig := header + fmt.Sprintf(`
resource "onpager_delivery_target" "team" {
 channel_id = %q
}
`, fService)
	config := func(name string, on, binding bool) string {
		hcl := header + fmt.Sprintf(`
resource "terraform_data" "sender" {
 input = %q
}
resource "terraform_data" "availability" {
 input = %t
}
resource "terraform_data" "destination" { input = %q }
resource "onpager_delivery_target" "team" {
 channel_id = %q
 snapshot_json = jsonencode({
  BotID = terraform_data.sender.output
  Kind = "conversation"
  Scope = {Kind = "workspace", ID = "example"}
  ChannelName = terraform_data.destination.output
 })
 enabled = terraform_data.availability.output
}
`, fService, on, name, fService)
		if binding {
			hcl += fmt.Sprintf(`
resource "onpager_team_alert_channel" "warning" {
 team_id = %q
 delivery_channel_id = onpager_delivery_target.team.channel_id
 target_id = onpager_delivery_target.team.id
}
`, fTeam)
		}
		return hcl
	}
	checkImport := func(states []*terraform.InstanceState) error {
		if len(states) != 1 || states[0].ID != first || states[0].Attributes["snapshot_json"] != "" {
			return fmt.Errorf("import must retain target identity without synthesizing its address")
		}
		return nil
	}
	replace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(address, plancheck.ResourceActionReplace),
	}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			want := []string{"disable " + imported, "create " + first, "set false " + first, "set true " + first,
				"set true " + first, "disable " + first, "create " + second, "disable " + second}
			if bound != "" || !reflect.DeepEqual(writes, want) || !reflect.DeepEqual(enabled, map[string]bool{imported: false, first: false, second: false}) {
				return fmt.Errorf("catalog binding %q and target availability %v after %v", bound, enabled, writes)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: importedConfig + fmt.Sprintf("import {\n to = %s\n id = %q\n}\n", address, imported), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "id", imported), resource.TestCheckNoResourceAttr(address, "snapshot_json"))},
			{Config: importedConfig, PlanOnly: true},
			{Config: config("primary", false, false), ConfigPlanChecks: replace, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "id", first), resource.TestCheckResourceAttr(address, "enabled", "false"),
				resource.TestCheckResourceAttrSet(address, "snapshot_json"))},
			{ResourceName: address, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"snapshot_json"}, ImportStateCheck: checkImport},
			{Config: config("primary", false, false), PlanOnly: true},
			{Config: config("primary", true, true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(catalog, "id", bindingID),
				resource.TestCheckResourceAttrPair(catalog, "target_id", address, "id"))},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); enabled[first] = false }, Config: config("primary", true, true),
				Check: resource.TestCheckResourceAttr(address, "enabled", "true")},
			{Config: config("secondary", true, true), ConfigPlanChecks: replace, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "id", second), resource.TestCheckResourceAttr(catalog, "target_id", second))},
			{Config: config("secondary", true, true), PlanOnly: true},
		},
	})
}
