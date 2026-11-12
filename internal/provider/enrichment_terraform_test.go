package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Real Terraform catches schema/default/set/order/import plan inconsistencies;
// the HTTP fixture implements the independent management CAS contracts only.
func TestEnrichmentTerraformCanonicalRowsAndOrderedRules(t *testing.T) {
	terraformPath, e := exec.LookPath("terraform")
	if e != nil {
		t.Fatal("installed Terraform CLI is required")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	var mu sync.Mutex
	exists := false
	version, revision := int64(0), int64(0)
	description := ""
	enabled := true
	rowRevisions := map[int64][]map[string]string{}
	rulesRevision := int64(0)
	rules := []client.EnrichmentRule{}
	metadata := func(detail bool) map[string]any {
		m := map[string]any{"id": fTeam, "name": "inventory", "description": description, "enabled": enabled, "configuration_version": version, "current_revision": revision, "has_publisher_token": false}
		if detail {
			m["revision"] = map[string]any{"dataset_id": fTeam, "revision": revision, "row_count": len(rowRevisions[revision])}
		}
		return m
	}
	canonical := func(input []map[string]string) []map[string]string {
		unique := map[string]map[string]string{}
		keys := []string{}
		for _, row := range input {
			b, _ := json.Marshal(row)
			k := string(b)
			if _, ok := unique[k]; !ok {
				keys = append(keys, k)
			}
			unique[k] = row
		}
		sort.Strings(keys)
		out := []map[string]string{}
		for _, k := range keys {
			out = append(out, unique[k])
		}
		return out
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.Header.Get("If-Match") != "" || q.Header.Get("Authorization") != "Bearer fixture-token" || q.Header.Get("Onpager-Timezone") != "UTC" {
			t.Error("wrong enrichment request context")
			w.WriteHeader(400)
			return
		}
		respond := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		conflict := func() { w.WriteHeader(409); respond(map[string]any{"error": map[string]string{"code": "conflict"}}) }
		if strings.HasPrefix(q.URL.Path, "/v1/enrichment/rules") {
			if q.Method == "GET" && q.URL.Path == "/v1/enrichment/rules" {
				respond(map[string]any{"revision": rulesRevision, "rules": rules})
				return
			}
			if q.Method == "POST" && q.URL.Path == "/v1/enrichment/rules/revisions" {
				var b struct {
					ExpectedRevision int64                   `json:"expected_revision"`
					Rules            []client.EnrichmentRule `json:"rules"`
				}
				if e := json.NewDecoder(q.Body).Decode(&b); e != nil {
					t.Error(e)
				}
				if b.ExpectedRevision != rulesRevision {
					conflict()
					return
				}
				rulesRevision++
				rules = b.Rules
				respond(map[string]any{"revision": rulesRevision, "rules": rules})
				return
			}
		}
		if q.Method == "POST" && q.URL.Path == "/v1/enrichment/datasets" {
			var b client.EnrichmentDatasetInput
			if e := json.NewDecoder(q.Body).Decode(&b); e != nil {
				t.Error(e)
			}
			if exists {
				conflict()
				return
			}
			exists = true
			version = 1
			revision = 1
			description = b.Description
			enabled = b.Enabled
			rowRevisions[1] = canonical(b.Rows)
			respond(metadata(true))
			return
		}
		if !exists {
			w.WriteHeader(404)
			respond(map[string]any{"error": map[string]string{"code": "not_found"}})
			return
		}
		if q.URL.Path == "/v1/enrichment/datasets/"+fTeam {
			switch q.Method {
			case "GET":
				respond(metadata(true))
				return
			case "PATCH":
				var b struct {
					ExpectedVersion int64   `json:"expected_version"`
					Description     *string `json:"description"`
					Enabled         *bool   `json:"enabled"`
				}
				if e := json.NewDecoder(q.Body).Decode(&b); e != nil {
					t.Error(e)
				}
				if b.ExpectedVersion != version {
					conflict()
					return
				}
				version++
				if b.Description != nil {
					description = *b.Description
				}
				if b.Enabled != nil {
					enabled = *b.Enabled
				}
				respond(metadata(false))
				return
			case "DELETE":
				var b struct {
					ExpectedVersion int64 `json:"expected_version"`
				}
				_ = json.NewDecoder(q.Body).Decode(&b)
				if b.ExpectedVersion != version || len(rules) > 0 {
					conflict()
					return
				}
				exists = false
				w.WriteHeader(204)
				return
			}
		}
		if q.Method == "POST" && q.URL.Path == "/v1/enrichment/datasets/"+fTeam+"/revisions" {
			var b struct {
				ExpectedVersion int64               `json:"expected_version"`
				Rows            []map[string]string `json:"rows"`
			}
			_ = json.NewDecoder(q.Body).Decode(&b)
			if b.ExpectedVersion != version {
				conflict()
				return
			}
			version++
			revision++
			rowRevisions[revision] = canonical(b.Rows)
			respond(metadata(true))
			return
		}
		if q.Method == "GET" && strings.HasSuffix(q.URL.Path, "/rows") {
			var selected int64
			_, e := fmt.Sscanf(q.URL.Path, "/v1/enrichment/datasets/"+fTeam+"/revisions/%d/rows", &selected)
			if e != nil {
				t.Error(e)
			}
			items := []any{}
			for i, row := range rowRevisions[selected] {
				items = append(items, map[string]any{"row_position": i, "data": row})
			}
			respond(map[string]any{"items": items, "has_more": false})
			return
		}
		t.Errorf("unexpected enrichment operation %s %s", q.Method, q.URL.Path)
		w.WriteHeader(400)
	}))
	defer server.Close()
	config := func(updated, active, withRules bool) string {
		desc := ""
		rows := `[{host="b",owner="B"},{host="a",owner="A"},{host="a",owner="A"}]`
		if updated {
			desc = "updated"
			rows = `[{host="c",owner="C"},{host="a",owner="A"}]`
		}
		c := fmt.Sprintf(`
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" {
 endpoint = %q
 token = "fixture-token"
 timezone = "UTC"
}
resource "onpager_enrichment_dataset" "test" {
 name = "inventory"
 description = %q
 enabled = %t
 rows = %s
}
`, server.URL, desc, active, rows)
		if withRules {
			c += fmt.Sprintf(`resource "onpager_enrichment_rules" "test" {
 rules = [{id=%q,dataset_id=onpager_enrichment_dataset.test.id,lookup=[{host="labels.host"},{host="labels.fallback"}],emit={owner="owner"}}]
}
`, fService)
		}
		return c
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), Steps: []resource.TestStep{
		{Config: config(false, false, false), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "enabled", "false"), resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "description", ""), resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "rows.#", "2"))},
		{ResourceName: "onpager_enrichment_dataset.test", ImportState: true, ImportStateVerify: true},
		{Config: config(false, false, false), PlanOnly: true},
		{Config: config(true, false, false), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "configuration_version", "3"), resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "current_revision", "2"), resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "enabled", "false"))},
		{Config: config(true, false, false), PlanOnly: true},
		{Config: config(true, true, true), Check: resource.TestCheckResourceAttr("onpager_enrichment_rules.test", "rules.0.lookup.1.host", "labels.fallback")},
		{ResourceName: "onpager_enrichment_rules.test", ImportState: true, ImportStateId: "tenant", ImportStateVerify: true},
		{Config: config(true, true, true), PlanOnly: true},
		{Config: config(true, true, false)},
	}})
}
