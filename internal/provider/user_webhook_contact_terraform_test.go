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
)

func TestUserWebhookContactTerraformDraftAndNormalizedValues(t *testing.T) {
	const addr = "onpager_user_webhook_contact.test"
	view := userWebhookReceipt()
	view["draft"], view["verified"], view["generation"], view["values"] = true, false, 0, map[string]string{}
	seq := 7
	deleted := false
	var mu sync.Mutex
	expected := []map[string]any{
		{"display_name": "New label"},
		{"values": map[string]any{}, "expected_generation": float64(1)},
		{"values": map[string]any{"label": "one"}},
		{"display_name": "Final label"},
	}
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/v1/users/"+fOtherTeam+"/webhook-contacts/"+fTeam {
			t.Error("unexpected contact route", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		if (r.Method == "PATCH" || r.Method == "DELETE") && r.Header.Get("If-Match") != view["etag"] {
			t.Error("mutation used the wrong ETag")
			w.WriteHeader(412)
			return
		}
		switch r.Method {
		case "DELETE":
			deleted = true
			w.WriteHeader(204)
			return
		case "GET":
		case "PATCH":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if i := seq - 7; i >= len(expected) || !reflect.DeepEqual(in, expected[i]) {
				t.Errorf("unexpected PATCH at method revision %d: %v", seq, in)
				w.WriteHeader(422)
				return
			}
			if name, ok := in["display_name"]; ok {
				view["display_name"] = name
			}
			if v, ok := in["values"].(map[string]any); ok {
				stored := map[string]any{"label": "", "optional": ""}
				for key, value := range v {
					stored[key] = value
				}
				view["values"], view["verified"], view["draft"] = stored, true, false
			}
			seq++
			view["generation"], view["etag"] = seq-7, fmt.Sprintf(`"%d"`, seq)
		default:
			t.Error("unexpected method", r.Method)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("ETag", view["etag"].(string))
		_ = json.NewEncoder(w).Encode(view)
	}))
	t.Cleanup(server.Close)
	header := strings.Replace(routingAccHeader, `provider "onpager" {}`, fmt.Sprintf("provider \"onpager\" {\n endpoint = %q\n token = \"fixture-only\"\n}", server.URL), 1)
	config := func(label, fields string) string {
		return header + fmt.Sprintf(`
resource "onpager_user_webhook_contact" "test" {
 user_id = %q
 webhook_id = %q
 display_name = %q
 %s
}
`, fOtherTeam, fService, label, fields)
	}
	draft := config("Receiver", "")
	ignored := config("New label", "values = {}\n lifecycle { ignore_changes = [values] }")
	complete := config("New label", "values = {}")
	changed := config("New label", `values = { label = "one" }`)
	omitted := config("Final label", "")
	i := fmt.Sprintf("import {\n to = %s\n id = %q\n}\n", addr, fOtherTeam+"/"+fTeam)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if !deleted || seq != 11 {
			return fmt.Errorf("contact method survived destroy or unexpected write count")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: draft + i, Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(addr, "draft", "true"), resource.TestCheckNoResourceAttr(addr, "request_id"), resource.TestCheckNoResourceAttr(addr, "template_contact_id"))},
		{Config: draft, PlanOnly: true},
		{Config: ignored, Check: resource.TestCheckResourceAttr(addr, "draft", "true")},
		{Config: complete, Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(addr, "draft", "false"), resource.TestCheckResourceAttr(addr, "values.%", "0"))},
		{Config: complete, PlanOnly: true},
		{Config: changed, Check: resource.TestCheckResourceAttr(addr, "values.%", "1")},
		{Config: omitted, Check: resource.TestCheckResourceAttr(addr, "values.label", "one")},
		{Config: omitted, PlanOnly: true},
	}})
}
