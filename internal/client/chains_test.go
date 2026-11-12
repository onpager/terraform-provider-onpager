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

func TestChainUsesStrongETagForReplaceAndArchive(t *testing.T) {
	tag := `"` + strings.Repeat("a", 64) + `"`
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/teams/"+teamID+"/escalation-chains/"+serviceID {
			t.Error("wrong chain detail route")
		}
		if calls == 1 {
			if r.Method != "GET" {
				t.Error("expected detail read")
			}
		} else if r.Header.Get("If-Match") != tag {
			t.Error("strong chain ETag was not sent")
		}
		if calls == 2 && r.Method != "PUT" {
			t.Error("expected replace")
		}
		if calls == 3 {
			if r.Method != "DELETE" {
				t.Error("destroy must call archive DELETE")
			}
			body, _ := io.ReadAll(r.Body)
			if len(body) != 0 {
				t.Error("archive body must be empty")
			}
			w.WriteHeader(204)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": serviceID, "team_id": teamID, "name": "Primary", "etag": tag, "steps": []any{}, "work_calendar_id": nil, "work_calendar_reference": "start_time", "off_hours_delegate_chain_id": nil})
	})
	got, err := c.GetChain(t.Context(), teamID, serviceID)
	if err != nil || got.ETag != tag {
		t.Fatalf("chain: %+v %v", got, err)
	}
	_, err = c.UpdateChain(t.Context(), teamID, serviceID, tag, client.ChainInput{Name: "Primary", Steps: []client.ChainStep{}, WorkCalendarReference: "start_time"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ArchiveChain(t.Context(), teamID, serviceID, tag); err != nil {
		t.Fatal(err)
	}
}

func TestChainArchiveConflictIsNotSuccessOrRetried(t *testing.T) {
	for _, status := range []int{403, 409, 412} {
		calls := 0
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"chain_in_use","details":{"dependencies":[{"kind":"escalation_rule","id":"`+serviceID+`","team_id":"`+teamID+`"}]}}}`)
		})
		err := c.ArchiveChain(t.Context(), teamID, serviceID, `"`+strings.Repeat("b", 64)+`"`)
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != status || calls != 1 {
			t.Fatalf("archive failure lost or retried: %v (%d calls)", err, calls)
		}
	}
}

func TestChainRejectsMalformedRetirementOrETag(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"`+serviceID+`","team_id":"`+teamID+`","name":"Primary","steps":[],"etag":"bad","retired_at":"","work_calendar_reference":"start_time"}`)
	})
	_, err := c.GetChain(t.Context(), teamID, serviceID)
	if err == nil {
		t.Fatal("malformed chain state must not be accepted as archived")
	}
}
