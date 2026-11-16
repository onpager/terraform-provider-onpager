package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// This exercises the real protocol with Terraform, especially write-only
// configuration and server normalization that direct resource calls cannot prove.
func TestLocalUserTerraformLifecycle(t *testing.T) {
	binary, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	name, email, login, active := "", "", "", false
	creates, patches, commands := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.Header.Get("If-Match") != "" || q.URL.RawQuery != "" || strings.Contains(q.URL.Path, "password") {
			t.Error("unexpected revision, discovery or password reset")
			w.WriteHeader(500)
			return
		}
		write := func(directory bool) {
			v := map[string]any{"id": fTeam, "display_name": name, "email_normalized": email, "active": active, "revision": 1, "created_at": "2026-11-16T00:00:00Z"}
			if directory {
				v["display"] = map[string]any{"local_login": login, "auth_methods": []string{"local_password"}, "protected": false, "can_update": true, "can_deactivate": active, "can_reactivate": !active, "can_reset_password": active}
			}
			_ = json.NewEncoder(w).Encode(v)
		}
		if q.Method == "POST" && q.URL.Path == "/v1/users" {
			var in map[string]any
			if err := json.NewDecoder(q.Body).Decode(&in); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if in["password"] != "first-initial-password" || in["password_change_required"] != false {
				t.Error("wrong initial password/policy")
			}
			creates++
			name, login, active = in["display_name"].(string), strings.ToLower(in["login"].(string)), true
			email = ""
			if in["email"] != nil {
				email = strings.ToLower(strings.TrimSpace(in["email"].(string)))
			}
			w.WriteHeader(201)
			write(false)
			return
		}
		if q.URL.Path == "/v1/users/"+fTeam {
			switch q.Method {
			case "GET":
				write(true)
			case "PATCH":
				var in map[string]any
				if err := json.NewDecoder(q.Body).Decode(&in); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				patches++
				for k, v := range in {
					switch k {
					case "display_name":
						name = v.(string)
					case "email":
						email = ""
						if v != nil {
							email = strings.ToLower(strings.TrimSpace(v.(string)))
						}
					default:
						t.Error("unsupported metadata field", k)
					}
				}
				fmt.Fprintf(w, `{"id":%q}`, fTeam)
			default:
				t.Error("unexpected user operation")
				w.WriteHeader(405)
			}
			return
		}
		if q.Method == "POST" && (q.URL.Path == "/v1/users/"+fTeam+"/deactivate" || q.URL.Path == "/v1/users/"+fTeam+"/reactivate") {
			commands++
			active = strings.HasSuffix(q.URL.Path, "/reactivate")
			w.WriteHeader(204)
			return
		}
		t.Error("unexpected route", q.Method, q.URL.Path)
		w.WriteHeader(404)
	}))
	t.Cleanup(server.Close)
	config := func(display, emailExpr string, enabled bool, password string) string {
		initial := ""
		if password != "" {
			initial = fmt.Sprintf("initial_password = %q\ninitial_password_change_required = false", password)
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
resource "onpager_user" "test" {
 display_name = %q
 login = "example"
 email = %s
 active = %t
 %s
}
`, server.URL, display, emailExpr, enabled, initial)
	}
	check := func(wantPatches, wantCommands int) resource.TestCheckFunc {
		return resource.ComposeTestCheckFunc(resource.TestCheckNoResourceAttr("onpager_user.test", "initial_password"), resource.TestCheckNoResourceAttr("onpager_user.test", "initial_password_change_required"), func(s *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if creates != 1 || patches != wantPatches || commands != wantCommands || s.RootModule().Resources["onpager_user.test"].Primary.ID != fTeam {
				return fmt.Errorf("user replaced or unexpected mutation: creates=%d patches=%d commands=%d", creates, patches, commands)
			}
			return nil
		})
	}
	first := config("Example user", `" User@Example.TEST "`, true, "first-initial-password")
	changedPassword := config("Example user", `" User@Example.TEST "`, true, "ignored-second-password")
	inactive := config("Changed", `null`, false, "")
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if active || creates != 1 || commands != 4 {
			return fmt.Errorf("destroy did not deactivate only its user: active=%t creates=%d commands=%d", active, creates, commands)
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: first, Check: resource.ComposeTestCheckFunc(check(0, 0), resource.TestCheckResourceAttr("onpager_user.test", "email", " User@Example.TEST "), resource.TestCheckResourceAttr("onpager_user.test", "email_normalized", "user@example.test"))},
		{Config: changedPassword, PlanOnly: true},
		{Config: inactive, Check: check(1, 1)},
		{ResourceName: "onpager_user.test", ImportState: true, ImportStateVerify: true},
		{Config: inactive, PlanOnly: true},
		{PreConfig: func() { mu.Lock(); defer mu.Unlock(); name, email, active = "Outside", "outside@example.test", true }, Config: inactive, Check: check(2, 2)},
		{Config: config("Changed", `" Other@Example.TEST "`, true, "ignored-third-password"), Check: check(3, 3)},
		{Config: config("Changed", `" Other@Example.TEST "`, true, ""), PlanOnly: true},
	}})
}

func TestLocalUserTerraformNormalizedLoginAndEmptyEmail(t *testing.T) {
	binary, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", binary)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "POST" && strings.HasSuffix(q.URL.Path, "deactivate") {
			w.WriteHeader(204)
			return
		}
		if q.Method == "POST" {
			w.WriteHeader(201)
		}
		fmt.Fprint(w, strings.Replace(localUserView(true), `"email_normalized":"user@example.test"`, `"email_normalized":""`, 1))
	}))
	t.Cleanup(server.Close)
	config := fmt.Sprintf(`terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-only"
}
resource "onpager_user" "test" {
 display_name = "Example user"
 login = "Example"
 email = ""
 initial_password = "first-initial-password"
}`, server.URL)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), Steps: []resource.TestStep{
		{Config: config, Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_user.test", "login", "Example"), resource.TestCheckResourceAttr("onpager_user.test", "login_normalized", "example"), resource.TestCheckResourceAttr("onpager_user.test", "email", ""))},
		{Config: config, PlanOnly: true},
	}})
}
