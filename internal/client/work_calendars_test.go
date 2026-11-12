package client_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestWorkCalendarIndependentRevisionsAndDirectRoutes(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "GET" && r.URL.Query().Get("reference_date") != "2024-01-01" {
			t.Error("read/write projection must use fixed reference date")
		}
		expected := "/v1/teams/" + teamID + "/work-calendars"
		if calls > 1 {
			expected += "/" + serviceID
		}
		if calls >= 5 {
			expected += "/entries"
		}
		if calls >= 6 {
			expected += "/" + teamID
		}
		if r.URL.Path != expected {
			t.Errorf("route %s want %s", r.URL.Path, expected)
		}
		tag := ""
		if calls == 3 || calls == 4 {
			tag = `"7"`
		}
		if calls == 7 || calls == 8 {
			tag = `"13"`
		}
		if r.Header.Get("If-Match") != tag {
			t.Errorf("wrong own revision %q want %q", r.Header.Get("If-Match"), tag)
		}
		if calls == 4 || calls == 8 {
			w.WriteHeader(204)
			return
		}
		if calls >= 5 {
			json.NewEncoder(w).Encode(map[string]any{"id": teamID, "calendar_id": serviceID, "revision": 13, "name": "Holiday", "effect": "non_working", "rule_type": "date", "start_date": "2026-12-01"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": serviceID, "team_id": teamID, "revision": 7, "aggregate_revision": 99, "name": "Work", "base_schedule": map[string]any{"reference_date": "2024-01-01", "intervals": []any{}}, "working_weekend_schedule": map[string]any{"reference_date": "2024-01-01", "intervals": []any{}}, "production_country_code": "RU"})
	})
	in := client.WorkCalendarInput{Name: "Work"}
	if _, err := c.CreateWorkCalendar(t.Context(), teamID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetWorkCalendar(t.Context(), teamID, serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateWorkCalendar(t.Context(), teamID, serviceID, 7, in); err != nil {
		t.Fatal(err)
	}
	if err := c.ArchiveWorkCalendar(t.Context(), teamID, serviceID, 7); err != nil {
		t.Fatal(err)
	}
	entry := client.WorkCalendarEntryInput{Name: "Holiday", Effect: "non_working", RuleType: "date", StartDate: "2026-12-01"}
	if _, err := c.CreateWorkCalendarEntry(t.Context(), teamID, serviceID, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetWorkCalendarEntry(t.Context(), teamID, serviceID, teamID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateWorkCalendarEntry(t.Context(), teamID, serviceID, teamID, 13, entry); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteWorkCalendarEntry(t.Context(), teamID, serviceID, teamID, 13); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCalendarErrorsRetainStatusAndNeverRetry(t *testing.T) {
	for _, status := range []int{403, 404, 409, 412} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found"}})
			})
			err := c.ArchiveWorkCalendar(t.Context(), teamID, serviceID, 7)
			if status == 404 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var api *client.APIError
				if !errors.As(err, &api) || api.StatusCode != status {
					t.Fatalf("lost failure %v", err)
				}
			}
			if calls != 1 {
				t.Fatal("mutation retried")
			}
		})
	}
}

func TestWorkCalendarRejectsWrongResponseIdentity(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": teamID, "calendar_id": teamID, "revision": 4, "name": "bad", "effect": "working", "rule_type": "date"})
	})
	if _, err := c.GetWorkCalendarEntry(t.Context(), teamID, serviceID, teamID); err == nil {
		t.Fatal("wrong scope accepted")
	}
}
