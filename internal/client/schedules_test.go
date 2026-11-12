package client_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const scheduleFixtureID = "33333333-3333-4333-8333-333333333333"

func TestScheduleSettingsOwnRevisionAndNullCalendar(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "PATCH" || r.URL.Path != "/v1/schedules/"+scheduleFixtureID || r.Header.Get("If-Match") != `"2"` {
			t.Errorf("wrong own-revision request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("If-Match"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 3 || body["name"] != "Changed" || body["enabled"] != false {
			t.Errorf("wrong settings body: %#v", body)
		}
		if v, ok := body["work_calendar_id"]; !ok || v != nil {
			t.Error("calendar unlink must be explicit null")
		}
		io.WriteString(w, `{"id":"`+scheduleFixtureID+`","team_id":"`+teamID+`","name":"Changed","enabled":false,"work_calendar_id":null,"revision":3,"aggregate_revision":99}`)
	})
	name, enabled := "Changed", false
	got, err := c.UpdateSchedule(t.Context(), scheduleFixtureID, 2, client.ScheduleSettings{Name: &name, Enabled: &enabled, WorkCalendarID: json.RawMessage("null")})
	if err != nil || got.Revision != 3 || got.Enabled || got.WorkCalendarID != nil {
		t.Fatalf("update: %+v %v", got, err)
	}
	if calls != 1 {
		t.Fatalf("unexpected calls: %d", calls)
	}
}

func TestScheduleCreateReadDisable(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/teams/"+teamID+"/schedules" || r.Header.Get("If-Match") != "" {
				t.Error("wrong create")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["enabled"]; ok {
				t.Error("unsupported enabled in create")
			}
		case 2:
			if r.Method != "GET" || r.URL.Path != "/v1/schedules/"+scheduleFixtureID {
				t.Error("read must use direct identity")
			}
		case 3:
			if r.Method != "POST" || r.URL.Path != "/v1/schedules/"+scheduleFixtureID+"/disable" || r.Header.Get("If-Match") != `"1"` {
				t.Error("wrong disable")
			}
		default:
			t.Error("unexpected request")
		}
		enabled := "true"
		if calls == 3 {
			enabled = "false"
		}
		io.WriteString(w, `{"id":"`+scheduleFixtureID+`","team_id":"`+teamID+`","name":"Ops","enabled":`+enabled+`,"revision":1}`)
	})
	if _, err := c.CreateSchedule(t.Context(), teamID, "Ops", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetSchedule(t.Context(), scheduleFixtureID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DisableSchedule(t.Context(), scheduleFixtureID, 1); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleRejectsWrongOrIncompleteIdentity(t *testing.T) {
	for _, body := range []string{`{}`, `{"id":"` + teamID + `","team_id":"` + teamID + `","name":"Ops","enabled":true,"revision":1}`, `{"id":"` + scheduleFixtureID + `","team_id":"` + teamID + `","name":"Ops","enabled":true,"revision":0}`} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, err := c.GetSchedule(t.Context(), scheduleFixtureID); err == nil || client.IsNotFound(err) {
			t.Fatalf("accepted malformed schedule: %s", body)
		}
	}
}

func TestScheduleDisableRejectsStillEnabledResponse(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"`+scheduleFixtureID+`","team_id":"`+teamID+`","name":"Ops","enabled":true,"revision":2}`)
	})
	if _, err := c.DisableSchedule(t.Context(), scheduleFixtureID, 1); err == nil {
		t.Fatal("enabled response must not confirm successful retirement")
	}
}

func TestScheduleRejectsMissingEnabledState(t *testing.T) {
	for _, body := range []string{`{"id":"` + scheduleFixtureID + `","team_id":"` + teamID + `","name":"Ops","revision":1}`, `{"id":"` + scheduleFixtureID + `","team_id":"` + teamID + `","name":"Ops","enabled":null,"revision":1}`} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, err := c.GetSchedule(t.Context(), scheduleFixtureID); err == nil {
			t.Fatal("missing enabled state was treated as disabled")
		}
	}
}
