package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// A real Terraform plan/apply cycle verifies Framework computed defaults and
// local input bindings, including provider-only timezone changes. The fixture
// owns timestamp conversion to model the backend's boundary.
func TestSilenceTerraformStablePlansAndTimezoneDrift(t *testing.T) {
	var mu sync.Mutex
	var stored client.Silence
	var start, end time.Time
	writes, deletes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.URL.Path != "/v1/silences" && q.URL.Path != "/v1/silences/"+silenceUUID {
			t.Error("unexpected route", q.URL)
			w.WriteHeader(500)
			return
		}
		if q.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing fixture bearer")
		}
		zone, e := time.LoadLocation(q.Header.Get("Onpager-Timezone"))
		if e != nil {
			t.Error(e)
			w.WriteHeader(422)
			return
		}
		if q.Method == "DELETE" {
			if q.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, stored.Revision) {
				t.Error("destroy lost own revision")
			}
			stored.ID = ""
			deletes++
			w.WriteHeader(204)
			return
		}
		if q.Method == "POST" || q.Method == "PATCH" {
			var in client.SilenceInput
			if e = json.NewDecoder(q.Body).Decode(&in); e != nil {
				t.Error(e)
			}
			if q.Method == "PATCH" {
				if q.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, stored.Revision) {
					t.Error("stale own revision overwritten")
				}
				in.Scope = stored.Scope
			}
			parse := func(raw string) time.Time {
				v, e := time.Parse(time.RFC3339Nano, raw)
				if e != nil {
					v, e = time.ParseInLocation("2006-01-02T15:04:05.999999", raw, zone)
				}
				if e != nil {
					t.Error(e)
				}
				return v
			}
			start, end = parse(in.StartsAt), parse(in.EndsAt)
			// Fixed fixture clock straddles the two interval interpretations:
			// UTC is active; the +05 reinterpretation is already expired.
			observedAt := time.Date(2024, time.January, 2, 2, 0, 0, 0, time.UTC)
			status := "active"
			if observedAt.Before(start) {
				status = "scheduled"
			} else if !observedAt.Before(end) {
				status = "expired"
			}
			stored = client.Silence{ID: silenceUUID, Revision: stored.Revision + 1, SilenceInput: in, Status: status}
			writes++
			w.Header().Set("ETag", fmt.Sprintf(`"%d"`, stored.Revision))
			if q.Method == "PATCH" {
				_ = json.NewEncoder(w).Encode(client.SilenceReceipt{ID: stored.ID, Revision: stored.Revision})
				return
			}
			w.WriteHeader(201)
		} else if q.Method != "GET" {
			t.Error("unexpected method", q.Method)
			w.WriteHeader(500)
			return
		}
		if stored.ID == "" {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			return
		}
		out := stored
		out.StartsAt = start.In(zone).Format(time.RFC3339Nano)
		out.EndsAt = end.In(zone).Format(time.RFC3339Nano)
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, stored.Revision))
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	terraformPath, e := exec.LookPath("terraform")
	if e != nil {
		t.Fatal("installed Terraform CLI required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	config := func(zone string) string {
		return fmt.Sprintf(`terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-token"
 timezone = %q
}
resource "onpager_silence" "test" {
 scope = "team"
 team_ids = [%q, %q]
 expression = "false"
 starts_at = "2024-01-01T05:00:00.123456"
 ends_at = "2024-01-02T05:00:00.654321"
}
`, server.URL, zone, fTeam, fOtherTeam)
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), Steps: []resource.TestStep{
		{Config: config("UTC"), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_silence.test", "starts_at", "2024-01-01T05:00:00.123456"), resource.TestCheckResourceAttr("onpager_silence.test", "comment", ""), resource.TestCheckResourceAttr("onpager_silence.test", "status", "active"))},
		{Config: config("UTC"), PlanOnly: true},
		{Config: config("Asia/Yekaterinburg"), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_silence.test", "request_timezone", "Asia/Yekaterinburg"), resource.TestCheckResourceAttr("onpager_silence.test", "status", "expired"), resource.TestCheckResourceAttr("onpager_silence.test", "starts_at", "2024-01-01T05:00:00.123456"))},
		{Config: config("Asia/Yekaterinburg"), PlanOnly: true},
		{PreConfig: func() { mu.Lock(); stored.Comment = "external drift"; stored.Revision++; mu.Unlock() }, Config: config("Asia/Yekaterinburg"), Check: resource.TestCheckResourceAttr("onpager_silence.test", "comment", "")},
		{Config: config("Asia/Yekaterinburg"), PlanOnly: true},
	}})
	if writes != 3 || deletes != 1 {
		t.Fatal("unstable plan or duplicate mutation", writes, deletes)
	}
}
