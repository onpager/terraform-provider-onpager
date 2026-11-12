package client_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestHandoverOwnBodyRevisionAndCanonicalCleanup(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/teams/"+teamID+"/schedules/"+serviceID+"/handover" || r.URL.RawQuery != "" || r.Header.Get("If-Match") != "" {
			t.Fatalf("unexpected handover request %s", r.URL.String())
		}
		if r.Method == "GET" {
			io.WriteString(w, `{"enabled":false,"oncall_channel_ids":[],"revision":0}`)
			return
		}
		if r.Method != "PUT" {
			t.Fatal("unexpected mutation")
		}
		var input map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 3 || string(input["enabled"]) != "false" || string(input["oncall_channel_ids"]) != "[]" || string(input["revision"]) != "0" {
			t.Fatalf("noncanonical cleanup payload: %v", input)
		}
		io.WriteString(w, `{"enabled":false,"oncall_channel_ids":[],"revision":1}`)
	})
	v, err := c.GetScheduleHandover(t.Context(), teamID, serviceID)
	if err != nil || v.Revision != 0 || v.Enabled || len(v.OncallChannelIDs) != 0 {
		t.Fatalf("initial slot: %+v %v", v, err)
	}
	v, err = c.PutScheduleHandover(t.Context(), teamID, serviceID, client.ScheduleHandover{Revision: v.Revision})
	if err != nil || v.Revision != 1 || !v.Committed || calls != 2 {
		t.Fatalf("cleanup: %+v %v", v, err)
	}
}

func TestHandoverMalformedReadsAndCommittedWrites(t *testing.T) {
	for _, body := range []string{`{}`, `{"enabled":false,"oncall_channel_ids":[],"revision":-1}`, `{"enabled":true,"oncall_channel_ids":[],"revision":1}`, `{"enabled":false,"oncall_channel_ids":null,"revision":1}`, `{"enabled":false,"revision":1}`, `{"enabled":false,"oncall_channel_ids":["bad"],"revision":1}`, `{"enabled":false,"oncall_channel_ids":[],"revision":0}`} {
		t.Run(body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			v, err := c.PutScheduleHandover(t.Context(), teamID, serviceID, client.ScheduleHandover{})
			if err == nil || !v.Committed {
				t.Fatalf("malformed committed write accepted/lost: %+v %v", v, err)
			}
			if body != `{"enabled":false,"oncall_channel_ids":[],"revision":0}` {
				if _, err = c.GetScheduleHandover(t.Context(), teamID, serviceID); err == nil {
					t.Fatal("malformed read accepted")
				}
			}
		})
	}
}

func TestHandoverErrorsRetainStatusWithoutReplayOrSecrets(t *testing.T) {
	for _, status := range []int{403, 404, 409, 422, 500} {
		calls := 0
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"conflict","message":"sensitive details"}}`)
		})
		v, err := c.PutScheduleHandover(t.Context(), teamID, serviceID, client.ScheduleHandover{Revision: 7})
		var api *client.APIError
		if !errors.As(err, &api) || api.StatusCode != status || v.Committed || calls != 1 || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("status/replay: %+v %v", v, err)
		}
	}
}
