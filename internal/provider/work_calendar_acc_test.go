package provider_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

// TestAccWorkCalendars only creates passive configuration and never sends alerts.
func TestAccWorkCalendars(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 with explicit local OnPager API settings")
	}
	endpoint, token := os.Getenv("ONPAGER_ACC_ENDPOINT"), os.Getenv("ONPAGER_ACC_TOKEN")
	if endpoint == "" || token == "" {
		t.Fatal("set ONPAGER_ACC_ENDPOINT and ONPAGER_ACC_TOKEN")
	}
	t.Setenv("ONPAGER_ENDPOINT", endpoint)
	t.Setenv("ONPAGER_TOKEN", token)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	c, err := client.New(endpoint, token)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("tfacc-%x", suffix)
	var teamID, calendarID, entryID, siblingID string
	ctx := t.Context()
	config := func(updated bool, variant string) string {
		name := prefix + "-calendar"
		rule := `rule_type = "date"
 start_date = "2027-05-01"`
		if updated {
			name += "-updated"
			rule = `rule_type = "weekly"
 weekdays = ["MO", "WE"]
 start_date = "2027-01-01"
 end_date = "2027-12-31"
 clock = { reference_date = "2024-01-01", windows = [
 { starts_at = "2024-01-01T00:00:00Z", ends_at = "2024-01-01T01:00:01Z" },
 { starts_at = "2024-01-03T00:00:00Z", ends_at = "2024-01-03T01:00:01Z" }
 ] }`
		}
		if variant != "" {
			rule = variant
		}
		return fmt.Sprintf(`terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" { timezone = "UTC" }
resource "onpager_team" "calendar" { name = %q }
resource "onpager_work_calendar" "main" {
 team_id = onpager_team.calendar.id
 name = %q
 base_schedule = { reference_date = "2024-01-01", intervals = [
 { starts_at = "2024-01-01T09:00:00Z", ends_at = "2024-01-01T17:00:00Z" },
 { starts_at = "2024-01-07T18:00:00Z", ends_at = "2024-01-08T00:00:00Z" }
 ] }
 working_weekend_schedule = { reference_date = "2024-01-01", intervals = [] }
}
resource "onpager_work_calendar_entry" "main" {
 team_id = onpager_team.calendar.id
 calendar_id = onpager_work_calendar.main.id
 name = %q
 effect = "non_working"
 %s
}
`, prefix+"-team", name, prefix+"-exception", rule)
	}
	capture := func(s *terraform.State) error {
		teamID = s.RootModule().Resources["onpager_team.calendar"].Primary.ID
		calendarID = s.RootModule().Resources["onpager_work_calendar.main"].Primary.ID
		entryID = s.RootModule().Resources["onpager_work_calendar_entry.main"].Primary.ID
		return nil
	}
	parentImport := func(*terraform.State) (string, error) { return teamID + "/" + calendarID, nil }
	entryImport := func(*terraform.State) (string, error) { return teamID + "/" + calendarID + "/" + entryID, nil }
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"onpager": providerserver.NewProtocol6WithError(onpager.New("test")())},
		CheckDestroy: func(*terraform.State) error {
			v, err := c.GetWorkCalendar(context.Background(), teamID, calendarID)
			if err != nil {
				return err
			}
			if v.RetiredAt == nil {
				return fmt.Errorf("calendar destroy did not retain retired UUID")
			}
			if _, err = c.GetWorkCalendarEntry(context.Background(), teamID, calendarID, entryID); !client.IsNotFound(err) {
				return fmt.Errorf("managed entry was not deleted: %v", err)
			}
			if siblingID != "" {
				if _, err = c.GetWorkCalendarEntry(context.Background(), teamID, calendarID, siblingID); err != nil {
					return fmt.Errorf("archive erased unmanaged sibling: %w", err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false, ""), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_work_calendar.main", "inherit_production_calendar", "false"), capture)},
			// Entry creation happens after the parent create response and advances
			// aggregate_revision. Refresh the saved parent snapshot before comparing
			// every imported attribute, including both independent revision fields.
			{RefreshState: true},
			{ResourceName: "onpager_work_calendar.main", ImportState: true, ImportStateIdFunc: parentImport, ImportStateVerify: true},
			{ResourceName: "onpager_work_calendar_entry.main", ImportState: true, ImportStateIdFunc: entryImport, ImportStateVerify: true},
			{PreConfig: func() {
				parent, err := c.GetWorkCalendar(ctx, teamID, calendarID)
				if err != nil {
					t.Fatal(err)
				}
				entry, err := c.GetWorkCalendarEntry(ctx, teamID, calendarID, entryID)
				if err != nil {
					t.Fatal(err)
				}
				sibling, err := c.CreateWorkCalendarEntry(ctx, teamID, calendarID, client.WorkCalendarEntryInput{Name: prefix + "-unmanaged", Effect: "working", RuleType: "yearly", Month: 2, Day: 29})
				if err != nil {
					t.Fatal(err)
				}
				siblingID = sibling.ID
				fresh, err := c.GetWorkCalendar(ctx, teamID, calendarID)
				if err != nil {
					t.Fatal(err)
				}
				if fresh.Revision != parent.Revision || fresh.AggregateRevision <= parent.AggregateRevision {
					t.Fatal("sibling changed settings revision or did not invalidate aggregate")
				}
				other, err := c.GetWorkCalendarEntry(ctx, teamID, calendarID, entryID)
				if err != nil || other.Revision != entry.Revision {
					t.Fatal("sibling changed entry revision")
				}
			}, Config: config(true, ""), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_work_calendar_entry.main", "clock.windows.#", "2"), func(*terraform.State) error {
				_, err := c.GetWorkCalendarEntry(ctx, teamID, calendarID, siblingID)
				return err
			})},
			{ResourceName: "onpager_work_calendar_entry.main", ImportState: true, ImportStateIdFunc: entryImport, ImportStateVerify: true},
			{Config: config(true, ""), PlanOnly: true},
			{PreConfig: func() {
				parent, err := c.GetWorkCalendar(ctx, teamID, calendarID)
				if err != nil {
					t.Fatal(err)
				}
				in := parent.WorkCalendarInput
				in.Name = prefix + "-external"
				if _, err = c.UpdateWorkCalendar(ctx, teamID, calendarID, parent.Revision, in); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, "")},

			{Config: config(true, `rule_type = "date_range"
 start_date = "2027-12-24"
 end_date = "2027-12-26"`)},
			{Config: config(true, `rule_type = "datetime_interval"
 starts_at = "2027-12-24T05:00:00.123456+05:00"
 ends_at = "2027-12-24T05:00:01.123456+05:00"`)},
			{Config: config(true, `rule_type = "datetime_interval"
 starts_at = "2027-12-24T05:00:00.123456+05:00"
 ends_at = "2027-12-24T05:00:01.123456+05:00"`), PlanOnly: true},
			{Config: config(true, `rule_type = "yearly"
 month = 2
 day = 29
 clock = { reference_date = "2024-02-29", windows = [{ starts_at = "2024-02-29T23:59:59Z", ends_at = "2024-03-01T00:00:00Z" }] }`)},
			{ResourceName: "onpager_work_calendar_entry.main", ImportState: true, ImportStateIdFunc: entryImport, ImportStateVerify: true},
			{Config: config(true, `rule_type = "yearly"
 month = 2
 day = 29
 clock = { reference_date = "2024-02-29", windows = [{ starts_at = "2024-02-29T23:59:59Z", ends_at = "2024-03-01T00:00:00Z" }] }`), PlanOnly: true},
		},
	})
}

// This opt-in test creates isolated passive configuration only. It checks the
// backend's known Moscow/UTC contract without loading zones in the provider.
func TestAccCalendarRequestTimezone(t *testing.T) {
	c, prefix := routingAccSetup(t)
	var team, calendar, entry string
	config := func(zone string, explicit bool) string {
		suffix := ""
		if explicit {
			suffix = "+03:00"
		}
		return strings.Replace(routingAccHeader, `provider "onpager" {}`, fmt.Sprintf(`provider "onpager" { timezone = %q }`, zone), 1) + fmt.Sprintf(`
resource "onpager_team" "timezone" { name = %q }
resource "onpager_work_calendar" "timezone" {
 team_id = onpager_team.timezone.id
 name = %q
 base_schedule = { reference_date = "2024-01-01", intervals = [
 { starts_at = "2024-01-01T09:00:00%s", ends_at = "2024-01-01T17:00:00%s" }
 ] }
 working_weekend_schedule = { reference_date = "2024-01-01", intervals = [] }
}
resource "onpager_work_calendar_entry" "timezone" {
 team_id = onpager_team.timezone.id
 calendar_id = onpager_work_calendar.timezone.id
 name = %q
 effect = "non_working"
 rule_type = "datetime_interval"
 starts_at = "2027-01-04T09:00:00.123456%s"
 ends_at = "2027-01-04T10:00:00.123456%s"
}
`, prefix, prefix, suffix, suffix, prefix, suffix, suffix)
	}
	check := func(expected string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			team = s.RootModule().Resources["onpager_team.timezone"].Primary.ID
			calendar = s.RootModule().Resources["onpager_work_calendar.timezone"].Primary.ID
			entry = s.RootModule().Resources["onpager_work_calendar_entry.timezone"].Primary.ID
			got, err := c.GetWorkCalendarEntry(t.Context(), team, calendar, entry)
			if err != nil {
				return err
			}
			at, err := time.Parse(time.RFC3339Nano, got.StartsAt)
			if err != nil {
				return err
			}
			want, err := time.Parse(time.RFC3339Nano, expected)
			if err != nil {
				return err
			}
			if !at.Equal(want) {
				return fmt.Errorf("backend stored a different entry instant: got %s want %s", got.StartsAt, expected)
			}
			parent, err := c.GetWorkCalendar(t.Context(), team, calendar)
			if err != nil {
				return err
			}
			if parent.BaseSchedule.ReferenceDate != "2024-01-01" || len(parent.BaseSchedule.Intervals) != 1 {
				return fmt.Errorf("source week or interval identity changed")
			}
			clock, err := time.Parse(time.RFC3339Nano, parent.BaseSchedule.Intervals[0].StartsAt)
			if err != nil {
				return err
			}
			expectedClock := "2024-01-01T06:00:00Z"
			if expected == "2027-01-04T09:00:00.123456Z" {
				expectedClock = "2024-01-01T09:00:00Z"
			}
			wantClock, _ := time.Parse(time.RFC3339Nano, expectedClock)
			if !clock.Equal(wantClock) {
				return fmt.Errorf("backend stored a different weekly instant")
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		Steps: []resource.TestStep{
			{Config: config("Europe/Moscow", false), Check: resource.ComposeTestCheckFunc(check("2027-01-04T06:00:00.123456Z"), resource.TestCheckResourceAttr("onpager_work_calendar_entry.timezone", "starts_at", "2027-01-04T09:00:00.123456"), resource.TestCheckResourceAttr("onpager_work_calendar_entry.timezone", "request_timezone", "Europe/Moscow"))},
			{Config: config("Europe/Moscow", false), PlanOnly: true},
			{ResourceName: "onpager_work_calendar.timezone", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"base_schedule", "request_timezone", "aggregate_revision"}, ImportStateCheck: func(states []*terraform.InstanceState) error {
				if len(states) != 1 {
					return fmt.Errorf("expected one imported calendar")
				}
				count := 0
				for key, value := range states[0].Attributes {
					if strings.HasPrefix(key, "base_schedule.intervals.") && strings.HasSuffix(key, ".starts_at") {
						got, err := time.Parse(time.RFC3339Nano, value)
						if err != nil {
							return err
						}
						expected, _ := time.Parse(time.RFC3339Nano, "2024-01-01T06:00:00Z")
						if !got.Equal(expected) {
							return fmt.Errorf("calendar import changed source instant")
						}
						count++
					}
				}
				if count != 1 {
					return fmt.Errorf("calendar import lost source intervals")
				}
				return nil
			}, ImportStateIdFunc: func(*terraform.State) (string, error) { return team + "/" + calendar, nil }},

			{ResourceName: "onpager_work_calendar_entry.timezone", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"starts_at", "ends_at", "request_timezone"}, ImportStateCheck: scheduleAccImportInstants(map[string]string{"starts_at": "2027-01-04T06:00:00.123456Z", "ends_at": "2027-01-04T07:00:00.123456Z"}), ImportStateIdFunc: func(*terraform.State) (string, error) { return team + "/" + calendar + "/" + entry, nil }},
			{Config: config("UTC", false), Check: resource.ComposeTestCheckFunc(check("2027-01-04T09:00:00.123456Z"), resource.TestCheckResourceAttr("onpager_work_calendar_entry.timezone", "request_timezone", "UTC"))},
			{Config: config("UTC", false), PlanOnly: true},
			{Config: config("UTC", true), Check: check("2027-01-04T06:00:00.123456Z")},
			{Config: config("Europe/Moscow", true), PlanOnly: true},
			{Config: config("Europe/Moscow", true), Check: check("2027-01-04T06:00:00.123456Z")},
			{PreConfig: func() {
				got, err := c.GetWorkCalendarEntry(t.Context(), team, calendar, entry)
				if err != nil {
					t.Fatal(err)
				}
				input := got.WorkCalendarEntryInput
				input.StartsAt = "2027-01-04T08:00:00.123456Z"
				input.EndsAt = "2027-01-04T09:00:00.123456Z"
				if _, err = c.UpdateWorkCalendarEntry(t.Context(), team, calendar, entry, got.Revision, input); err != nil {
					t.Fatal(err)
				}
			}, Config: config("Europe/Moscow", true), Check: check("2027-01-04T06:00:00.123456Z")},
			{Config: config("Europe/Moscow", true), PlanOnly: true},
			{Config: strings.NewReplacer("2027-01-04T09:00:00.123456", "2026-03-29T02:30:00", "2027-01-04T10:00:00.123456", "2026-03-29T03:30:00").Replace(config("Europe/Berlin", false)), ExpectError: regexp.MustCompile("nonexistent_local_time")},
			{Config: strings.NewReplacer("2027-01-04T09:00:00.123456", "2026-10-25T02:30:00", "2027-01-04T10:00:00.123456", "2026-10-25T03:30:00").Replace(config("Europe/Berlin", false)), ExpectError: regexp.MustCompile("ambiguous_local_time")},
			{Config: config("Europe/Moscow", true), Check: check("2027-01-04T06:00:00.123456Z")},
		},
	})
}
