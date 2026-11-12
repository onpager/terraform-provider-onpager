package client_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const silenceID = "77777777-7777-4777-8777-777777777777"

func silenceJSON() string {
	return `{"id":"` + silenceID + `","revision":7,"scope":"team","team_ids":["` + teamID + `","` + serviceID + `"],"expression":"false","comment":"","starts_at":"2027-01-01T00:00:00.123456Z","ends_at":"2027-01-02T00:00:00.654321Z","status":"expired","cancelled_at":null}`
}
func silenceInput() client.SilenceInput {
	return client.SilenceInput{Scope: "team", TeamIDs: []string{teamID, serviceID}, Expression: "false", Comment: "", StartsAt: "2027-01-01T00:00:00.123456", EndsAt: "2027-01-02T00:00:00.654321"}
}
func TestSilenceWholeObjectLifecycle(t *testing.T) {
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if r.URL.RawQuery != "" {
			t.Error("unexpected collection or projection query")
		}
		switch step {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/silences" || r.Header.Get("If-Match") != "" {
				t.Error("create route")
			}
			var body client.SilenceInput
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Scope != "team" || len(body.TeamIDs) != 2 || body.StartsAt != silenceInput().StartsAt || body.Comment != "" {
				t.Errorf("create lost explicit scope/finite timestamps: %+v %v", body, err)
			}
			w.Header().Set("ETag", `"7"`)
			w.WriteHeader(201)
			io.WriteString(w, silenceJSON())
		case 2:
			if r.Method != "GET" || r.URL.Path != "/v1/silences/"+silenceID {
				t.Error("not direct UUID read")
			}
			w.Header().Set("ETag", `"7"`)
			io.WriteString(w, silenceJSON())
		case 3:
			if r.Method != "PATCH" || r.URL.Path != "/v1/silences/"+silenceID || r.Header.Get("If-Match") != `"7"` {
				t.Error("update own CAS")
			}
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 5 || string(body["team_ids"]) != `["`+serviceID+`"]` || string(body["comment"]) != `""` || body["scope"] != nil {
				t.Errorf("all-target update scope/body %s", body)
			}
			w.Header().Set("ETag", `"8"`)
			io.WriteString(w, `{"id":"`+silenceID+`","revision":8}`)
		case 4:
			if r.Method != "DELETE" || r.URL.Path != "/v1/silences/"+silenceID || r.Header.Get("If-Match") != `"8"` {
				t.Error("destroy must exact DELETE own CAS")
			}
			b, _ := io.ReadAll(r.Body)
			if len(b) > 0 {
				t.Error("DELETE body")
			}
			w.WriteHeader(204)
		}
	})
	created, e := c.CreateSilence(t.Context(), silenceInput())
	if e != nil || created.ID != silenceID || !created.Committed {
		t.Fatalf("create %+v %v", created, e)
	}
	got, e := c.GetSilence(t.Context(), silenceID)
	if e != nil || got.Status != "expired" || got.StartsAt != "2027-01-01T00:00:00.123456Z" {
		t.Fatalf("read %+v %v", got, e)
	}
	input := silenceInput()
	input.TeamIDs = []string{serviceID}
	receipt, e := c.UpdateSilence(t.Context(), silenceID, 7, input)
	if e != nil || receipt.Revision != 8 || !receipt.Committed {
		t.Fatalf("receipt %+v %v", receipt, e)
	}
	if e = c.DeleteSilence(t.Context(), silenceID, 8); e != nil {
		t.Fatal(e)
	}
	if step != 4 {
		t.Fatal(step)
	}
}
func TestSilenceRejectsIncompleteAndForeignSuccessfulResponses(t *testing.T) {
	for _, change := range []string{`"revision":7`, `"comment":""`, `"team_ids":["` + teamID + `","` + serviceID + `"]`, `"starts_at":"2027-01-01T00:00:00.123456Z"`, `"status":"expired"`} {
		t.Run(change, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"7"`)
				io.WriteString(w, strings.Replace(silenceJSON(), change, strings.SplitN(change, ":", 2)[0]+`:null`, 1))
			})
			if _, e := c.GetSilence(t.Context(), silenceID); e == nil {
				t.Fatal("null required field accepted")
			}
		})
	}
	for _, body := range []string{strings.Replace(silenceJSON(), silenceID, teamID, 1), strings.Replace(silenceJSON(), `"scope":"team"`, `"scope":"tenant"`, 1), strings.Replace(silenceJSON(), `"status":"expired"`, `"status":"invalid"`, 1)} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("ETag", `"7"`); io.WriteString(w, body) })
		if _, e := c.GetSilence(t.Context(), silenceID); e == nil {
			t.Fatal("invalid response accepted")
		}
	}
}
func TestSilenceCommittedReceiptsAndFailures(t *testing.T) {
	for _, body := range []string{`{"id":"` + silenceID + `","revision":8}`, `{"id":"` + silenceID + `","revision":null}`, `{"id":"` + teamID + `","revision":8}`, `{"id":"` + silenceID + `","revision":8,"unexpected":true}`, `{`} {
		t.Run(body, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("ETag", `"8"`); io.WriteString(w, body) })
			v, e := c.UpdateSilence(t.Context(), silenceID, 7, silenceInput())
			if !v.Committed {
				t.Fatal("successful write lost commit marker")
			}
			valid := strings.Contains(body, `"revision":8`) && !strings.Contains(body, teamID)
			if valid && e != nil || !valid && e == nil {
				t.Fatalf("receipt %+v %v", v, e)
			}
			if strings.Contains(body, teamID) && v.ID != "" {
				t.Fatal("foreign identity leaked to partial state")
			}
		})
	}
	for _, status := range []int{403, 404, 412, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				code := "forbidden"
				if status == 404 {
					code = "not_found"
				}
				fmt.Fprintf(w, `{"error":{"code":%q}}`, code)
			})
			_, e := c.GetSilence(t.Context(), silenceID)
			if (status == 404) != client.IsNotFound(e) {
				t.Fatal(e)
			}
			_, e = c.UpdateSilence(t.Context(), silenceID, 7, silenceInput())
			if e == nil {
				t.Fatal("failure swallowed")
			}
			e = c.DeleteSilence(t.Context(), silenceID, 7)
			if (status == 404) != (e == nil) {
				t.Fatal("DELETE absence/error", e)
			}
			if calls != 3 {
				t.Fatal("write retried")
			}
		})
	}
}
func TestSilenceRefusesMissingCASAndUnexpectedDeleteSuccess(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200); io.WriteString(w, `{}`) })
	if _, e := c.UpdateSilence(t.Context(), silenceID, 0, silenceInput()); e == nil {
		t.Fatal("missing update CAS")
	}
	if e := c.DeleteSilence(t.Context(), silenceID, 0); e == nil {
		t.Fatal("missing delete CAS")
	}
	if calls != 0 {
		t.Fatal("missing CAS sent")
	}
	if e := c.DeleteSilence(t.Context(), silenceID, 7); e == nil {
		t.Fatal("unexpected success did not confirm DELETE")
	}
}

func TestSilenceCreateRejectsUntrustedRevisionButKeepsCommittedID(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"9"`)
		w.WriteHeader(201)
		io.WriteString(w, silenceJSON())
	})
	got, e := c.CreateSilence(t.Context(), silenceInput())
	if e == nil || !got.Committed || got.ID != silenceID || got.Revision != 0 {
		t.Fatalf("malformed ETag trusted for partial state: %+v %v", got, e)
	}
}
