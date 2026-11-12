package provider_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func enrichmentAccDeleteDataset(ctx context.Context, c *client.Client, id string) error {
	if id == "" {
		return nil
	}
	v, e := c.GetEnrichmentDataset(ctx, id)
	if client.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	return c.DeleteEnrichmentDataset(ctx, id, v.ConfigurationVersion)
}

// Only passive datasets created by this test are edited/deleted. No publisher
// credentials, runtime enrichment, event input, preview or alert is involved.
func TestAccEnrichmentDataset(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var id string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := enrichmentAccDeleteDataset(ctx, c, id); e != nil {
			t.Errorf("cleanup enrichment dataset: %v", e)
		}
	})
	config := func(updated bool) string {
		description := ""
		owner := "operations"
		if updated {
			description = "updated"
			owner = "sre"
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_enrichment_dataset" "test" {
 name = %q
 description = %q
 enabled = false
 rows = [{host="tfacc-passive.invalid",owner=%q},{host="tfacc-passive.invalid",owner=%q}]
}
`, prefix, description, owner, owner)
	}
	capture := func(s *terraform.State) error {
		if row := s.RootModule().Resources["onpager_enrichment_dataset.test"]; row != nil && row.Primary != nil {
			id = row.Primary.ID
		}
		return nil
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(s *terraform.State) error {
		_ = capture(s)
		if id == "" {
			return nil
		}
		_, e := c.GetEnrichmentDataset(t.Context(), id)
		if !client.IsNotFound(e) {
			return fmt.Errorf("destroy did not remove live dataset: %v", e)
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "enabled", "false"), resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "rows.#", "1"))},
		{ResourceName: "onpager_enrichment_dataset.test", ImportState: true, ImportStateVerify: true},
		{Config: config(false), PlanOnly: true},
		{Config: config(true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "description", "updated"), resource.TestCheckResourceAttr("onpager_enrichment_dataset.test", "enabled", "false"), func(*terraform.State) error {
			_, e := c.PublishEnrichmentDataset(t.Context(), id, 1, []map[string]string{{"host": "tfacc-passive.invalid", "owner": "stale"}})
			var api *client.APIError
			if !errors.As(e, &api) || api.StatusCode != 409 {
				return fmt.Errorf("expected stale dataset CAS409, got %v", e)
			}
			return nil
		})},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			v, e := c.GetEnrichmentDataset(t.Context(), id)
			if e != nil {
				t.Fatal(e)
			}
			description := "external drift"
			v, e = c.UpdateEnrichmentDataset(t.Context(), id, v.ConfigurationVersion, client.EnrichmentDatasetUpdate{Description: &description})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = c.PublishEnrichmentDataset(t.Context(), id, v.ConfigurationVersion, []map[string]string{{"host": "tfacc-passive.invalid", "owner": "drift"}}); e != nil {
				t.Fatal(e)
			}
		}, Config: config(true)},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			if e := enrichmentAccDeleteDataset(t.Context(), c, id); e != nil {
				t.Fatal(e)
			}
		}, Config: config(true), Check: capture},
	}})
}

// Whole-list live ownership is allowed only on an explicitly disposable empty
// tenant configuration. The initial guard precedes all managed writes.
func TestAccEnrichmentRules(t *testing.T) {
	c, prefix := routingAccSetup(t)
	if os.Getenv("ONPAGER_ACC_ENRICHMENT_RULES_DISPOSABLE") != "1" {
		t.Skip("whole-tenant rules live test requires ONPAGER_ACC_ENRICHMENT_RULES_DISPOSABLE=1 and an empty disposable rules configuration")
	}
	initial, e := c.GetEnrichmentRules(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if len(initial.Rules) != 0 {
		t.Fatal("refusing to overwrite a nonempty unowned tenant enrichment rules configuration")
	}
	// This stable explicit test rule identity belongs only to this guarded run.
	const ruleID = "a7917917-d421-4e84-bf22-24b18760d311"
	var dataset string
	owned := false
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if owned {
			v, e := c.GetEnrichmentRules(ctx)
			if e != nil {
				t.Errorf("cleanup read enrichment rules: %v", e)
				return
			}
			for _, r := range v.Rules {
				if r.ID != ruleID || r.DatasetID != dataset {
					t.Error("cleanup refused foreign enrichment rules snapshot")
					return
				}
			}
			if len(v.Rules) > 0 {
				if _, e = c.PublishEnrichmentRules(ctx, v.Revision, []client.EnrichmentRule{}); e != nil {
					t.Errorf("cleanup enrichment rules: %v", e)
					return
				}
			}
		}
		if e := enrichmentAccDeleteDataset(ctx, c, dataset); e != nil {
			t.Errorf("cleanup rules dataset: %v", e)
		}
	})
	config := func(updated bool) string {
		lookup := `[{host="labels.host"},{host="labels.fallback"}]`
		if updated {
			lookup = `[{host="labels.fallback"},{host="labels.host"}]`
		}
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_enrichment_dataset" "rules" {
 name = %q
 rows = [{host=%q,owner="tfacc-owner"}]
}
resource "onpager_enrichment_rules" "test" {
 rules = [{id=%q,dataset_id=onpager_enrichment_dataset.rules.id,lookup=%s,emit={owner="owner"}}]
}
`, prefix+"-rules", prefix+".invalid", ruleID, lookup)
	}
	capture := func(s *terraform.State) error {
		if v := s.RootModule().Resources["onpager_enrichment_dataset.rules"]; v != nil && v.Primary != nil {
			dataset = v.Primary.ID
		}
		if v := s.RootModule().Resources["onpager_enrichment_rules.test"]; v != nil && v.Primary != nil && v.Primary.ID == "tenant" {
			owned = true
		}
		return nil
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(s *terraform.State) error {
		_ = capture(s)
		v, e := c.GetEnrichmentRules(t.Context())
		if e != nil {
			return e
		}
		if len(v.Rules) != 0 {
			return fmt.Errorf("destroy retained tenant enrichment rules")
		}
		if dataset != "" {
			_, e = c.GetEnrichmentDataset(t.Context(), dataset)
			if !client.IsNotFound(e) {
				return fmt.Errorf("destroy retained rules dataset: %v", e)
			}
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: resource.ComposeTestCheckFunc(capture, resource.TestCheckResourceAttr("onpager_enrichment_rules.test", "id", "tenant"), resource.TestCheckResourceAttr("onpager_enrichment_rules.test", "rules.0.lookup.0.host", "labels.host"), func(*terraform.State) error {
			v, e := c.GetEnrichmentDataset(t.Context(), dataset)
			if e != nil {
				return e
			}
			e = c.DeleteEnrichmentDataset(t.Context(), dataset, v.ConfigurationVersion)
			var api *client.APIError
			if !errors.As(e, &api) || api.StatusCode != 409 || api.Code != "dataset_in_use" {
				return fmt.Errorf("expected current-rule deletion blocker, got %v", e)
			}
			return nil
		})},
		{ResourceName: "onpager_enrichment_rules.test", ImportState: true, ImportStateId: "tenant", ImportStateVerify: true},
		{Config: config(false), PlanOnly: true},
		{Config: config(true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_enrichment_rules.test", "rules.0.lookup.0.host", "labels.fallback"), func(*terraform.State) error {
			_, e := c.PublishEnrichmentRules(t.Context(), initial.Revision, []client.EnrichmentRule{})
			var api *client.APIError
			if !errors.As(e, &api) || api.StatusCode != 409 {
				return fmt.Errorf("expected stale rules CAS409, got %v", e)
			}
			return nil
		})},
		{Config: config(true), PlanOnly: true},
		{PreConfig: func() {
			v, e := c.GetEnrichmentRules(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			v.Rules[0].Lookup = []map[string]string{{"host": "labels.external"}}
			if _, e = c.PublishEnrichmentRules(t.Context(), v.Revision, v.Rules); e != nil {
				t.Fatal(e)
			}
		}, Config: config(true)},
		{Config: config(true), PlanOnly: true},
	}})
}
