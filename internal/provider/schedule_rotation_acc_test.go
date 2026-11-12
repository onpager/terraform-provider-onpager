package provider_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// The managed children share a parent but keep independent declarations and CAS.
// All intervals are fixed; the test never submits alerts or runs notifications.
func TestAccScheduleDeclarations(t *testing.T) {
	c, prefix := routingAccSetup(t)
	user := os.Getenv("ONPAGER_ACC_USER_ID")
	if user == "" {
		t.Fatal("ONPAGER_ACC_USER_ID is required")
	}
	var scheduleID, rotationID, overrideID, siblingID string
	var siblingRevision, settingsRevision int64
	config := func(updated, active bool) string {
		every, unit, weekdays, duration, end := 1, "days", "null", "null", "null"
		effective, start := "2026-01-06T03:00:07+03:00", "2026-01-05T03:00:07+03:00"
		overrideStart, overrideEnd := "2026-01-05T12:00:07+03:00", "2026-01-05T13:00:07+03:00"
		if updated {
			every, unit, weekdays, duration, end = 2, "weeks", `["MO", "WE", "FR"]`, "3600", `"2041-01-05T00:00:07Z"`
			effective, start = "2040-01-05T03:00:07+03:00", "2039-01-03T03:00:07+03:00"
			overrideStart, overrideEnd = "2026-01-06T12:00:07+03:00", "2026-01-06T14:00:07+03:00"
		}
		return strings.Replace(routingAccHeader, `provider "onpager" {}`, `provider "onpager" { timezone = "Europe/Moscow" }`, 1) + fmt.Sprintf(`
resource "onpager_team" "declarations" {
 name = %q
 active = %t
}
resource "onpager_team_membership" "operator" {
 team_id = onpager_team.declarations.id
 user_id = %q
}
resource "onpager_schedule" "declarations" {
 team_id = onpager_team.declarations.id
 name = %q
 enabled = %t
}
resource "onpager_schedule_rotation" "primary" {
 schedule_id = onpager_schedule.declarations.id
 effective_from = %q
 starts_at = %q
 ends_at = %s
 user_ids = [onpager_team_membership.operator.user_id]
 recurrence = { every = %d, unit = %q }
 weekdays = %s
 max_shift_duration_seconds = %s
}
resource "onpager_schedule_override" "temporary" {
 schedule_id = onpager_schedule.declarations.id
 user_id = onpager_team_membership.operator.user_id
 starts_at = %q
 ends_at = %q
}
`, prefix, active, user, prefix, active, effective, start, end, every, unit, weekdays, duration, overrideStart, overrideEnd)
	}
	readSibling := func() error {
		got, err := c.GetOverrideRotation(context.Background(), scheduleID, siblingID)
		if err != nil {
			return err
		}
		if got.Revision != siblingRevision || got.Configuration.UserID != user {
			return fmt.Errorf("unmanaged sibling changed")
		}
		return nil
	}
	t.Cleanup(func() {
		if siblingID == "" {
			return
		}
		v, err := c.GetOverrideRotation(context.Background(), scheduleID, siblingID)
		if client.IsNotFound(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		if err := c.DeleteOverrideRotation(context.Background(), scheduleID, siblingID, v.Revision); err != nil {
			t.Error(err)
		}
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			if _, err := c.GetPrimaryRotation(context.Background(), scheduleID, rotationID); !client.IsNotFound(err) {
				return fmt.Errorf("primary declaration was not retired: %v", err)
			}
			if _, err := c.GetOverrideRotation(context.Background(), scheduleID, overrideID); !client.IsNotFound(err) {
				return fmt.Errorf("override declaration was not retired: %v", err)
			}
			// Both declaration and detail management routes intentionally hide
			// retired identities. Historical retention is the DELETE contract,
			// verified in backend tests; no database inspection is done here.
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false, true), Check: func(s *terraform.State) error {
				scheduleID = s.RootModule().Resources["onpager_schedule.declarations"].Primary.ID
				rotationID = s.RootModule().Resources["onpager_schedule_rotation.primary"].Primary.ID
				overrideID = s.RootModule().Resources["onpager_schedule_override.temporary"].Primary.ID
				settings, err := c.GetSchedule(t.Context(), scheduleID)
				if err != nil {
					return err
				}
				settingsRevision = settings.Revision
				sibling, err := c.CreateOverrideRotation(t.Context(), scheduleID, client.OverrideRotationRule{UserID: user, StartsAt: "2026-02-02T09:00:07Z", EndsAt: "2026-02-02T10:00:07Z"})
				if err != nil {
					return err
				}
				siblingID, siblingRevision = sibling.RotationID, sibling.Revision
				return nil
			}},
			{Config: config(false, true), PlanOnly: true},
			{Config: strings.Replace(config(false, true), `timezone = "Europe/Moscow"`, `timezone = "UTC"`, 1), PlanOnly: true},
			{ResourceName: "onpager_schedule_rotation.primary", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"effective_from", "starts_at"}, ImportStateCheck: scheduleAccImportInstants(map[string]string{"effective_from": "2026-01-06T00:00:07Z", "starts_at": "2026-01-05T00:00:07Z"}), ImportStateIdFunc: func(*terraform.State) (string, error) { return scheduleID + "/" + rotationID, nil }},
			{ResourceName: "onpager_schedule_override.temporary", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"starts_at", "ends_at"}, ImportStateCheck: scheduleAccImportInstants(map[string]string{"starts_at": "2026-01-05T09:00:07Z", "ends_at": "2026-01-05T10:00:07Z"}), ImportStateIdFunc: func(*terraform.State) (string, error) { return scheduleID + "/" + overrideID, nil }},
			{Config: config(true, true), Check: func(*terraform.State) error {
				settings, err := c.GetSchedule(t.Context(), scheduleID)
				if err != nil {
					return err
				}
				if settings.Revision != settingsRevision {
					return fmt.Errorf("child update changed parent settings revision")
				}
				// A stale child write must fail without replay, even though aggregate
				// revisions changed while siblings and parent settings stayed independent.
				v, err := c.GetPrimaryRotation(t.Context(), scheduleID, rotationID)
				if err != nil {
					return err
				}
				if _, err = c.UpdatePrimaryRotation(t.Context(), scheduleID, rotationID, v.Revision-1, v.EffectiveFrom, v.Configuration); err == nil {
					return fmt.Errorf("stale primary revision was accepted")
				} else {
					var api *client.APIError
					if !errors.As(err, &api) || api.StatusCode != 412 {
						return fmt.Errorf("stale primary write did not return conflict: %v", err)
					}
				}
				o, err := c.GetOverrideRotation(t.Context(), scheduleID, overrideID)
				if err != nil {
					return err
				}
				if _, err = c.UpdateOverrideRotation(t.Context(), scheduleID, overrideID, o.Revision-1, o.Configuration); err == nil {
					return fmt.Errorf("stale override revision was accepted")
				} else {
					var api *client.APIError
					if !errors.As(err, &api) || api.StatusCode != 412 {
						return fmt.Errorf("stale override write did not return conflict: %v", err)
					}
				}
				// Journal reads here only verify publication retention; the
				// provider itself uses direct declarations exclusively.
				for _, pair := range [][2]string{{"primary", rotationID}, {"override", overrideID}} {
					var history struct {
						Items []any `json:"items"`
					}
					route := "/v1/schedules/" + scheduleID + "/" + pair[0] + "-rotations/" + pair[1] + "/versions?limit=100"
					if err := scheduleAccRequest(t.Context(), os.Getenv("ONPAGER_ACC_ENDPOINT"), os.Getenv("ONPAGER_ACC_TOKEN"), "GET", route, nil, 0, &history); err != nil {
						return err
					}
					if len(history.Items) < 2 {
						return fmt.Errorf("declaration update lost earlier published rule")
					}
				}
				return readSibling()
			}},
			{Config: config(true, true), PlanOnly: true},
			{PreConfig: func() {
				v, err := c.GetPrimaryRotation(t.Context(), scheduleID, rotationID)
				if err != nil {
					t.Fatal(err)
				}
				v.Configuration.Recurrence.Every = 3
				if _, err = c.UpdatePrimaryRotation(t.Context(), scheduleID, rotationID, v.Revision, v.EffectiveFrom, v.Configuration); err != nil {
					t.Fatal(err)
				}
				o, err := c.GetOverrideRotation(t.Context(), scheduleID, overrideID)
				if err != nil {
					t.Fatal(err)
				}
				o.Configuration.EndsAt = "2026-01-06T12:00:07Z"
				if _, err = c.UpdateOverrideRotation(t.Context(), scheduleID, overrideID, o.Revision, o.Configuration); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("onpager_schedule_rotation.primary", "recurrence.every", "2"), resource.TestCheckResourceAttr("onpager_schedule_override.temporary", "ends_at", "2026-01-06T14:00:07+03:00"))},
			{PreConfig: func() {
				o, err := c.GetOverrideRotation(t.Context(), scheduleID, overrideID)
				if err != nil {
					t.Fatal(err)
				}
				if err = c.DeleteOverrideRotation(t.Context(), scheduleID, overrideID, o.Revision); err != nil {
					t.Fatal(err)
				}
			}, Config: config(true, true), Check: func(s *terraform.State) error {
				recreated := s.RootModule().Resources["onpager_schedule_override.temporary"].Primary.ID
				if recreated == overrideID {
					return fmt.Errorf("externally retired override was not recreated")
				}
				overrideID = recreated
				return nil
			}},
			// Returning to null removes the optional constraints without replacement.
			{Config: config(false, true)},
			{Config: config(false, true), PlanOnly: true},
			// Retire the unmanaged sibling first so membership revocation can succeed.
			{PreConfig: func() {
				if err := readSibling(); err != nil {
					t.Fatal(err)
				}
				if err := c.DeleteOverrideRotation(t.Context(), scheduleID, siblingID, siblingRevision); err != nil {
					t.Fatal(err)
				}
			}, Config: config(false, false)},
			{Config: config(false, false), PlanOnly: true},
			{ResourceName: "onpager_schedule_rotation.primary", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"effective_from", "starts_at"}, ImportStateCheck: scheduleAccImportInstants(map[string]string{"effective_from": "2026-01-06T00:00:07Z", "starts_at": "2026-01-05T00:00:07Z"}), ImportStateIdFunc: func(*terraform.State) (string, error) { return scheduleID + "/" + rotationID, nil }},
		},
	})
}

// Imported timestamps are canonical API values; exact string comparison is
// intentionally replaced only for these fields, with instant comparison here.
func scheduleAccImportInstants(expected map[string]string) resource.ImportStateCheckFunc {
	return func(states []*terraform.InstanceState) error {
		if len(states) != 1 {
			return fmt.Errorf("expected one imported declaration")
		}
		for key, want := range expected {
			got, err := time.Parse(time.RFC3339Nano, states[0].Attributes[key])
			if err != nil {
				return fmt.Errorf("imported %s is invalid", key)
			}
			at, err := time.Parse(time.RFC3339Nano, want)
			if err != nil {
				return err
			}
			if !got.Equal(at) {
				return fmt.Errorf("imported %s changed its instant", key)
			}
		}
		return nil
	}
}

// Request timezone coverage uses isolated passive declarations and the explicit
// Moscow/UTC API contract. It never resolves occurrences or sends notifications.
func TestAccScheduleRequestTimezone(t *testing.T) {
	c, prefix := routingAccSetup(t)
	user := os.Getenv("ONPAGER_ACC_USER_ID")
	if user == "" {
		t.Fatal("ONPAGER_ACC_USER_ID is required")
	}
	var scheduleID, primaryID, overrideID string
	config := func(zone string, explicit, updated bool) string {
		suffix := ""
		if explicit {
			suffix = "+03:00"
		}
		start, end := "09:00:00", "10:00:00"
		if updated {
			start, end = "11:00:00", "12:00:00"
		}
		header := strings.Replace(routingAccHeader, `provider "onpager" {}`, fmt.Sprintf(`provider "onpager" { timezone = %q }`, zone), 1)
		return header + fmt.Sprintf(`
resource "onpager_team" "timezone" { name = %q }
resource "onpager_team_membership" "timezone" {
 team_id = onpager_team.timezone.id
 user_id = %q
}
resource "onpager_schedule" "timezone" {
 team_id = onpager_team.timezone.id
 name = %q
 enabled = true
}
resource "onpager_schedule_rotation" "timezone" {
 schedule_id = onpager_schedule.timezone.id
 user_ids = [onpager_team_membership.timezone.user_id]
 starts_at = "2026-01-05T%s%s"
 effective_from = "2026-01-06T%s%s"
 ends_at = "2026-01-08T%s%s"
 recurrence = { every = 1, unit = "days" }
 weekdays = ["MO", "TU", "WE", "TH", "FR", "SA", "SU"]
}
resource "onpager_schedule_override" "timezone" {
 schedule_id = onpager_schedule.timezone.id
 user_id = onpager_team_membership.timezone.user_id
 starts_at = "2026-01-07T%s%s"
 ends_at = "2026-01-07T%s%s"
}
`, prefix, user, prefix, start, suffix, start, suffix, start, suffix, start, suffix, end, suffix)
	}
	check := func(hour, requestZone string) resource.TestCheckFunc {
		return func(state *terraform.State) error {
			scheduleID = state.RootModule().Resources["onpager_schedule.timezone"].Primary.ID
			primaryID = state.RootModule().Resources["onpager_schedule_rotation.timezone"].Primary.ID
			overrideID = state.RootModule().Resources["onpager_schedule_override.timezone"].Primary.ID
			primary, err := c.GetPrimaryRotation(t.Context(), scheduleID, primaryID)
			if err != nil {
				return err
			}
			override, err := c.GetOverrideRotation(t.Context(), scheduleID, overrideID)
			if err != nil {
				return err
			}
			for key, value := range map[string]string{"primary_start": primary.Configuration.StartsAt, "primary_effective": primary.EffectiveFrom, "override_start": override.Configuration.StartsAt} {
				date := "2026-01-05"
				if key == "primary_effective" {
					date = "2026-01-06"
				} else if key == "override_start" {
					date = "2026-01-07"
				}
				got, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					return err
				}
				expected, err := time.Parse(time.RFC3339Nano, date+"T"+hour+":00:00Z")
				if err != nil {
					return err
				}
				if !got.Equal(expected) {
					return fmt.Errorf("%s has unexpected saved instant: %s", key, value)
				}
			}
			if len(primary.Configuration.Weekdays) != 7 || primary.Configuration.Recurrence.Unit != "days" || primary.Configuration.Recurrence.Every != 1 {
				return fmt.Errorf("request timezone changed UTC recurrence/weekday declarations")
			}
			for _, name := range []string{"onpager_schedule_rotation.timezone", "onpager_schedule_override.timezone"} {
				if got := state.RootModule().Resources[name].Primary.Attributes["request_timezone"]; got != requestZone {
					return fmt.Errorf("%s request_timezone=%q, expected %q", name, got, requestZone)
				}
			}
			return nil
		}
	}
	primaryImport := func(*terraform.State) (string, error) { return scheduleID + "/" + primaryID, nil }
	overrideImport := func(*terraform.State) (string, error) { return scheduleID + "/" + overrideID, nil }
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: routingAccFactories(),
		CheckDestroy: func(*terraform.State) error {
			if _, err := c.GetPrimaryRotation(t.Context(), scheduleID, primaryID); !client.IsNotFound(err) {
				return fmt.Errorf("primary declaration not retired: %v", err)
			}
			if _, err := c.GetOverrideRotation(t.Context(), scheduleID, overrideID); !client.IsNotFound(err) {
				return fmt.Errorf("override declaration not retired: %v", err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("Europe/Moscow", false, false), Check: check("06", "Europe/Moscow")},
			{Config: config("Europe/Moscow", false, false), PlanOnly: true},
			{ResourceName: "onpager_schedule_rotation.timezone", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"starts_at", "effective_from", "ends_at", "request_timezone"}, ImportStateCheck: scheduleAccImportInstants(map[string]string{"starts_at": "2026-01-05T06:00:00Z", "effective_from": "2026-01-06T06:00:00Z", "ends_at": "2026-01-08T06:00:00Z"}), ImportStateIdFunc: primaryImport},
			{ResourceName: "onpager_schedule_override.timezone", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"starts_at", "ends_at", "request_timezone"}, ImportStateCheck: scheduleAccImportInstants(map[string]string{"starts_at": "2026-01-07T06:00:00Z", "ends_at": "2026-01-07T07:00:00Z"}), ImportStateIdFunc: overrideImport},
			{Config: config("Europe/Moscow", false, true), Check: check("08", "Europe/Moscow")},
			{Config: config("Europe/Moscow", false, true), PlanOnly: true},
			{Config: config("UTC", false, true), Check: check("11", "UTC")},
			{Config: config("UTC", false, true), PlanOnly: true},
			{Config: config("UTC", true, true), Check: check("08", "")},
			{Config: config("Europe/Moscow", true, true), PlanOnly: true},
			{PreConfig: func() {
				primary, err := c.GetPrimaryRotation(t.Context(), scheduleID, primaryID)
				if err != nil {
					t.Fatal(err)
				}
				primary.Configuration.StartsAt = "2026-01-05T13:00:00Z"
				if _, err = c.UpdatePrimaryRotation(t.Context(), scheduleID, primaryID, primary.Revision, primary.EffectiveFrom, primary.Configuration); err != nil {
					t.Fatal(err)
				}
				override, err := c.GetOverrideRotation(t.Context(), scheduleID, overrideID)
				if err != nil {
					t.Fatal(err)
				}
				override.Configuration.StartsAt = "2026-01-07T13:00:00Z"
				override.Configuration.EndsAt = "2026-01-07T14:00:00Z"
				if _, err = c.UpdateOverrideRotation(t.Context(), scheduleID, overrideID, override.Revision, override.Configuration); err != nil {
					t.Fatal(err)
				}
			}, Config: config("Europe/Moscow", true, true), Check: check("08", "")},
			{Config: strings.NewReplacer("2026-01-07T11:00:00+03:00", "2026-03-29T02:30:00", "2026-01-07T12:00:00+03:00", "2026-03-29T03:30:00").Replace(config("Europe/Berlin", true, true)), ExpectError: regexp.MustCompile("nonexistent_local_time")},
			{Config: strings.NewReplacer("2026-01-07T11:00:00+03:00", "2026-10-25T02:30:00", "2026-01-07T12:00:00+03:00", "2026-10-25T03:30:00").Replace(config("Europe/Berlin", true, true)), ExpectError: regexp.MustCompile("ambiguous_local_time")},
			{Config: config("Europe/Moscow", true, true), Check: check("08", "")},
			{Config: config("Europe/Moscow", true, true), PlanOnly: true},
		},
	})
}
