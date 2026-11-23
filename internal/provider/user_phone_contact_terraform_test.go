package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestUserPhoneContactTerraformImportAndSameSuffixDrift(t *testing.T) {
	const addr = "onpager_user_phone_contact.test"
	collection := "/v1/users/" + fOtherTeam + "/phone-contacts"
	method := collection + "/" + fTeam
	// The employee already has a verified method. The administrative read
	// can import it and rename it without knowing its number or creation key.
	card := phoneContactCard(otherPhoneTarget)
	var mu sync.Mutex
	patchCount := 0
	deleted := false
	wantPatches := []map[string]any{
		{"display_name": "Imported"},
		{"phone": "+12025550123", "channel_id": fService, "verified": true},
		{"phone": "+12025550123", "channel_id": fService, "verified": true},
		{"display_name": "Renamed"},
	}
	header := provisionDeliveryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != method {
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		switch r.Method {
		case "GET":
		case "PATCH":
			var in map[string]any
			if json.NewDecoder(r.Body).Decode(&in) != nil || patchCount >= len(wantPatches) || !reflect.DeepEqual(in, wantPatches[patchCount]) {
				t.Errorf("unexpected PATCH %d: %v", patchCount, in)
				w.WriteHeader(422)
				return
			}
			patchCount++
			if name, ok := in["display_name"]; ok {
				card["display_name"] = name
			}
			if _, ok := in["phone"]; ok {
				card["target_id"] = phoneContactTarget
				card["generation"] = patchCount + 1
				card["verified"] = true
			}
		case "DELETE":
			deleted = true
			w.WriteHeader(204)
			return
		default:
			t.Errorf("unexpected operation %s", r.Method)
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(card)
	})
	config := func(name, channel, extra string) string {
		return header + fmt.Sprintf(`
resource "onpager_user_phone_contact" "test" {
 user_id = %q
 channel_id = %q
 display_name = %q
 %s
}
`, fOtherTeam, channel, name, extra)
	}
	imported := config("Desk", fService, "")
	importBlock := fmt.Sprintf("import {\n to = %s\n id = %q\n}\n", addr, fOtherTeam+"/"+fTeam)
	phone := "phone = \"+12025550123\"\n verified = true"
	bound := config("Imported", fService, phone)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if patchCount != len(wantPatches) || !deleted {
				return fmt.Errorf("phone lifecycle ended after %d patches, deleted=%t", patchCount, deleted)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: imported + importBlock, Check: resource.ComposeTestCheckFunc(resource.TestCheckNoResourceAttr(addr, "phone"), resource.TestCheckNoResourceAttr(addr, "request_id"), resource.TestCheckResourceAttr(addr, "verified", "true"))},
			{Config: imported, PlanOnly: true},
			{Config: config("Imported", fService, "phone = \"+13035550123\"\n lifecycle { ignore_changes = [phone] }"), Check: resource.TestCheckNoResourceAttr(addr, "phone")},
			{Config: bound, Check: resource.TestCheckResourceAttr(addr, "target_id", phoneContactTarget)},
			{Config: bound, PlanOnly: true},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); card["target_id"] = otherPhoneTarget; card["generation"] = 5 }, Config: bound, Check: resource.TestCheckResourceAttr(addr, "target_id", phoneContactTarget)},
			{Config: config("Renamed", fService, ""), Check: resource.TestCheckResourceAttr(addr, "phone", "+12025550123")},
			{Config: config("Renamed", otherPhoneTarget, "request_id = \""+fOtherTeam+"\""), ExpectError: regexp.MustCompile("(?i)phone required for creation")},
		},
	})
}
