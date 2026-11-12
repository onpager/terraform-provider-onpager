package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

func TestTemporalTimezoneOnlyChangeInvalidatesPlannedRevisions(t *testing.T) {
	for _, kind := range []string{"calendar", "entry", "primary", "override"} {
		for _, zone := range []string{"Europe/Moscow", "UTC"} {
			t.Run(kind+"/"+zone, func(t *testing.T) {
				var r resource.Resource
				var values map[string]attr.Value
				revisions := []string{"revision"}
				switch kind {
				case "calendar":
					r, values = onpager.NewWorkCalendarResource(), workCalendarValues()
					values["base_schedule"] = calendarDatedValue("intervals", "2024-01-01", [2]string{"2024-01-01T09:00:00", "2024-01-01T17:00:00"})
					revisions = append(revisions, "aggregate_revision")
				case "entry":
					r, values = onpager.NewWorkCalendarEntryResource(), workCalendarEntryValues()
					values["starts_at"] = types.StringValue("2027-01-04T09:00:00")
				default:
					primary := kind == "primary"
					r, values = declarationResource(primary), declarationValues(primary)
					values["starts_at"] = types.StringValue("2027-01-04T09:00:00")
				}
				values["request_timezone"] = types.StringValue("Europe/Moscow")
				c, err := client.New("http://127.0.0.1:1", "fixture-token", client.WithTimezone(zone))
				if err != nil {
					t.Fatal(err)
				}
				var configured resource.ConfigureResponse
				r.(resource.ResourceWithConfigure).Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &configured)
				var schema resource.SchemaResponse
				r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
				state := valueState(t, schema.Schema, values)
				plan := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: schema.Schema, Raw: state.Raw}}
				r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{State: state, Plan: plan.Plan}, &plan)
				if plan.Diagnostics.HasError() {
					t.Fatal(plan.Diagnostics)
				}
				for _, name := range revisions {
					var revision types.Int64
					plan.Plan.GetAttribute(t.Context(), path.Root(name), &revision)
					if revision.IsUnknown() != (zone == "UTC") {
						t.Errorf("timezone-only change must invalidate %s; unchanged zone must retain it: %v", name, revision)
					}
				}
			})
		}
	}
}
