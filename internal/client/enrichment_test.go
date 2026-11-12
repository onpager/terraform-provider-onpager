package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func enrichmentDatasetJSON(version, revision int, detail bool) string {
	s := fmt.Sprintf(`{"id":%q,"name":"inventory","description":"","enabled":false,"configuration_version":%d,"current_revision":%d,"has_publisher_token":false`, teamID, version, revision)
	if detail {
		s += fmt.Sprintf(`,"revision":{"dataset_id":%q,"revision":%d,"row_count":2}`, teamID, revision)
	}
	return s + `}`
}

func TestEnrichmentDatasetBodyCASAndDisabledPublication(t *testing.T) {
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if r.Header.Get("If-Match") != "" {
			t.Error("enrichment must not send If-Match")
		}
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		switch step {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/enrichment/datasets" || b["enabled"] != false || b["rows"] == nil {
				t.Fatalf("wrong atomic disabled create: %#v", b)
			}
			fmt.Fprint(w, enrichmentDatasetJSON(1, 1, true))
		case 2:
			if r.Method != "PATCH" || b["expected_version"] != float64(7) || b["description"] != "" || b["enabled"] != false {
				t.Fatalf("wrong metadata CAS: %#v", b)
			}
			fmt.Fprint(w, enrichmentDatasetJSON(8, 3, false))
		case 3:
			if r.Method != "POST" || r.URL.Path != "/v1/enrichment/datasets/"+teamID+"/revisions" || b["expected_version"] != float64(8) || len(b) != 2 {
				t.Fatalf("wrong content CAS: %#v", b)
			}
			fmt.Fprint(w, enrichmentDatasetJSON(9, 4, true))
		case 4:
			if r.Method != "DELETE" || r.URL.Path != "/v1/enrichment/datasets/"+teamID || b["expected_version"] != float64(9) || len(b) != 1 {
				t.Fatalf("wrong delete: %#v", b)
			}
			w.WriteHeader(204)
		default:
			t.Fatal("unexpected retry or workaround")
		}
	})
	rows := []map[string]string{{"host": "a"}, {"host": "b"}}
	if v, e := c.CreateEnrichmentDataset(t.Context(), client.EnrichmentDatasetInput{Name: "inventory", Enabled: false, Rows: rows}); e != nil || v.Enabled {
		t.Fatal(v, e)
	}
	description, enabled := "", false
	if _, e := c.UpdateEnrichmentDataset(t.Context(), teamID, 7, client.EnrichmentDatasetUpdate{Description: &description, Enabled: &enabled}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.PublishEnrichmentDataset(t.Context(), teamID, 8, rows); e != nil {
		t.Fatal(e)
	}
	if e := c.DeleteEnrichmentDataset(t.Context(), teamID, 9); e != nil {
		t.Fatal(e)
	}
	if step != 4 {
		t.Fatal(step)
	}
}

func TestEnrichmentRowsReadExactImmutableRevisionToCompletion(t *testing.T) {
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if r.Method != "GET" || r.URL.Path != "/v1/enrichment/datasets/"+teamID+"/revisions/3/rows" || r.URL.Query().Get("limit") != "200" || len(r.URL.Query()) != 2 {
			t.Fatalf("not an immutable paginated row read: %s", r.URL)
		}
		if step == 1 {
			if r.URL.Query().Get("offset") != "0" {
				t.Fatal(r.URL)
			}
			fmt.Fprint(w, `{"items":[{"row_position":0,"data":{"host":"a","empty":""}}],"has_more":true}`)
		} else {
			if step != 2 || r.URL.Query().Get("offset") != "1" {
				t.Fatal(r.URL)
			}
			fmt.Fprint(w, `{"items":[{"row_position":1,"data":{"host":"b"}}],"has_more":false}`)
		}
	})
	rows, e := c.GetEnrichmentDatasetRows(t.Context(), teamID, 3, 2)
	if e != nil || len(rows) != 2 || rows[0]["host"] != "a" || rows[1]["host"] != "b" {
		t.Fatal(rows, e)
	}
}

func TestEnrichmentRejectsIncompleteSnapshotsAndPagination(t *testing.T) {
	for _, body := range []string{`{}`, strings.Replace(enrichmentDatasetJSON(1, 1, true), `"enabled":false`, `"enabled":null`, 1), strings.Replace(enrichmentDatasetJSON(1, 1, true), `"description":""`, `"description":null`, 1), strings.Replace(enrichmentDatasetJSON(1, 1, true), `"row_count":2`, `"row_count":null`, 1)} {
		t.Run(body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, e := c.GetEnrichmentDataset(t.Context(), teamID); e == nil {
				t.Fatal("incomplete snapshot accepted")
			}
		})
	}
	for _, body := range []string{`{"items":[],"has_more":true}`, `{"items":[],"has_more":false}`, `{"items":[],"has_more":null}`, `{"items":[{"row_position":1,"data":{}}],"has_more":false}`, `{"items":[{"row_position":0,"data":null}],"has_more":false}`, `{"items":[{"row_position":0,"data":{"x":null}}],"has_more":false}`} {
		t.Run(body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, e := c.GetEnrichmentDatasetRows(t.Context(), teamID, 1, 1); e == nil {
				t.Fatal("incomplete rows accepted")
			}
		})
	}
}

func TestEnrichmentMalformedMutationRetainsCommitAndIdentity(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":%q,"configuration_version":2,"current_revision":"bad"}`, teamID)
	})
	v, e := c.PublishEnrichmentDataset(t.Context(), teamID, 1, []map[string]string{})
	if e == nil || !v.Committed || v.ID != teamID || v.ConfigurationVersion != 2 {
		t.Fatal(v, e)
	}
}

func TestEnrichmentRulesOrderedSnapshotZeroRevisionAndClear(t *testing.T) {
	rules := []client.EnrichmentRule{{ID: teamID, DatasetID: serviceID, Lookup: []map[string]string{{"host": "labels.host"}, {"host": "labels.fallback"}}, Emit: map[string]string{"owner": "owner"}}}
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if step == 1 {
			if r.Method != "GET" || r.URL.Path != "/v1/enrichment/rules" {
				t.Fatal(r.URL)
			}
			fmt.Fprint(w, `{"revision":0,"rules":[]}`)
			return
		}
		var b struct {
			ExpectedRevision *int64                  `json:"expected_revision"`
			Rules            []client.EnrichmentRule `json:"rules"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Fatal(e)
		}
		if r.Method != "POST" || r.URL.Path != "/v1/enrichment/rules/revisions" || r.Header.Get("If-Match") != "" || b.ExpectedRevision == nil || *b.ExpectedRevision != int64(step-2) || b.Rules == nil {
			t.Fatal("wrong whole snapshot CAS")
		}
		if step == 2 && (len(b.Rules) != 1 || b.Rules[0].Lookup[1]["host"] != "labels.fallback") || step == 3 && len(b.Rules) != 0 {
			t.Fatal("list order or empty clear lost")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": step - 1, "rules": b.Rules})
	})
	if v, e := c.GetEnrichmentRules(t.Context()); e != nil || v.Revision != 0 {
		t.Fatal(v, e)
	}
	if _, e := c.PublishEnrichmentRules(t.Context(), 0, rules); e != nil {
		t.Fatal(e)
	}
	if _, e := c.PublishEnrichmentRules(t.Context(), 1, []client.EnrichmentRule{}); e != nil {
		t.Fatal(e)
	}
}

func TestEnrichmentErrorsSafeAndNeverRetried(t *testing.T) {
	for _, status := range []int{403, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"dataset_in_use","message":"secret-sensitive-row"}}`)
			})
			e := c.DeleteEnrichmentDataset(t.Context(), teamID, 2)
			var api *client.APIError
			if !errors.As(e, &api) || api.StatusCode != status || calls != 1 || strings.Contains(e.Error(), "secret-sensitive-row") {
				t.Fatal(e, calls)
			}
		})
	}
}

func TestEnrichmentDatasetDeleteRequiresExactNoContentConfirmation(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{200, ""}, {200, "{}"}, {202, ""}, {202, "{}"}, {204, ""}, {204, " \n\t"}, {204, "{}"}} {
		t.Run(fmt.Sprintf("%d/%q", tc.status, tc.body), func(t *testing.T) {
			calls := 0
			// Inject at the transport boundary because net/http servers strip
			// forbidden response bodies from 204 before a client can observe them.
			useHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "DELETE" || r.URL.Path != "/v1/enrichment/datasets/"+teamID {
					t.Fatal("unexpected deletion request")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			c, e := client.New("http://fixture.invalid", "fixture-token")
			if e != nil {
				t.Fatal(e)
			}
			err := c.DeleteEnrichmentDataset(t.Context(), teamID, 7)
			if (err != nil) != (tc.status != 204 || strings.TrimSpace(tc.body) != "") || calls != 1 {
				t.Fatalf("unconfirmed DELETE accepted or retried: status=%d error=%v calls=%d", tc.status, err, calls)
			}
		})
	}
}

func TestEnrichmentResponseBoundExceedsDefaultButRemainsFinite(t *testing.T) {
	for _, size := range []int{5 << 20, 13 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			// Unknown padding models a valid large JSON response without disclosing data in failures.
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"revision":0,"rules":[],"padding":%q}`, strings.Repeat("x", size))
			})
			_, e := c.GetEnrichmentRules(t.Context())
			if (size > 12<<20) != (e != nil) {
				t.Fatalf("unexpected bounded response result: %v", e)
			}
		})
	}
}

func TestEnrichmentLargeActualRowsAndUnchangedOtherResponseLimit(t *testing.T) {
	rows := make([]map[string]string, 200)
	for i := range rows {
		rows[i] = map[string]string{"index": fmt.Sprint(i)}
		for _, field := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			rows[i][field] = strings.Repeat("x", 4096)
		}
	}
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/rows") {
			items := []any{}
			for i, row := range rows {
				items = append(items, map[string]any{"row_position": i, "data": row})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false})
			return
		}
		fmt.Fprintf(w, `{"padding":%q}`, strings.Repeat("x", 5<<20))
	})
	got, e := c.GetEnrichmentDatasetRows(t.Context(), teamID, 1, 200)
	if e != nil || len(got) != 200 || got[199]["index"] != "199" {
		t.Fatal("valid dataset page above 4 MiB was rejected", e)
	}
	if _, e = c.GetGroup(t.Context(), teamID); e == nil || !strings.Contains(e.Error(), "4 MiB") {
		t.Fatal("unrelated response bound changed", e)
	}
}

func TestEnrichmentRowsEnforceCumulativeBudgetBeforeAnotherPage(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 3 {
			fmt.Fprint(w, `{"items":[],"has_more":false}`)
			return
		}
		items := []any{}
		for i := 0; i < 200; i++ {
			row := map[string]string{"index": fmt.Sprint((calls-1)*200 + i)}
			for _, field := range []string{"a", "b", "c", "d", "e"} {
				row[field] = strings.Repeat("x", 4096)
			}
			items = append(items, map[string]any{"row_position": (calls-1)*200 + i, "data": row})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": true})
	})
	if _, e := c.GetEnrichmentDatasetRows(t.Context(), teamID, 1, 100000); e == nil || calls != 3 {
		t.Fatalf("oversized paginated dataset was buffered or fetched again: error=%v calls=%d", e, calls)
	}
}

func TestEnrichmentRowsRejectInvalidTextBeforeAnotherPage(t *testing.T) {
	for _, bad := range []map[string]string{{strings.Repeat("k", 257): "v"}, {"field": strings.Repeat("v", 4097)}, {"field": "bad\x00value"}} {
		t.Run(fmt.Sprint(len(bad)), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				row := bad
				if calls > 1 {
					row = map[string]string{"field": "ok"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"row_position": calls - 1, "data": row}}, "has_more": calls == 1})
			})
			if _, e := c.GetEnrichmentDatasetRows(t.Context(), teamID, 1, 2); e == nil || calls != 1 {
				t.Fatalf("invalid row was buffered until later pages: error=%v calls=%d", e, calls)
			}
		})
	}
}

func TestEnrichmentRowsLimitDistinctFieldsAcrossPages(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		row := map[string]string{"additional": ""}
		if calls == 1 {
			row = map[string]string{}
			for i := 0; i < 256; i++ {
				row[fmt.Sprintf("field%d", i)] = ""
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"row_position": calls - 1, "data": row}}, "has_more": calls < 3})
	})
	if _, e := c.GetEnrichmentDatasetRows(t.Context(), teamID, 1, 3); e == nil || calls != 2 {
		t.Fatalf("excess distinct fields were buffered until later pages: error=%v calls=%d", e, calls)
	}
}
