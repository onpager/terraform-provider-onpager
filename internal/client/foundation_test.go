package client_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const (
	teamID    = "11111111-1111-4111-8111-111111111111"
	serviceID = "22222222-2222-4222-8222-222222222222"
)

func fixtureClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := client.New(s.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTeamLifecycleUsesIdentityReceiptAndDisable(t *testing.T) {
	step := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		step++
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing bearer authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch step {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/teams" {
				t.Errorf("wrong create route: %s %s", r.Method, r.URL.Path)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["name"] != "Operations" {
				t.Error("wrong create body")
			}
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"`+teamID+`","name":"Operations","active":true}`)
		case 2:
			if r.Method != "PATCH" || r.URL.Path != "/v1/teams/"+teamID || r.Header.Get("If-Match") != "" {
				t.Error("rename must not send If-Match")
			}
			io.WriteString(w, `{"id":"`+teamID+`"}`)
		case 3:
			if r.Method != "GET" || r.URL.Path != "/v1/teams/"+teamID {
				t.Error("wrong detail route")
			}
			io.WriteString(w, `{"id":"`+teamID+`","name":"Platform","active":true}`)
		case 4:
			if r.Method != "POST" || r.URL.Path != "/v1/teams/"+teamID+"/disable" {
				t.Error("destroy must disable, not physically delete")
			}
			body, _ := io.ReadAll(r.Body)
			if len(body) != 0 {
				t.Error("disable must not send a body")
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected request")
		}
	})
	created, err := c.CreateTeam(t.Context(), "Operations")
	if err != nil || created.ID != teamID {
		t.Fatalf("create: %+v %v", created, err)
	}
	receipt, err := c.RenameTeam(t.Context(), teamID, "Platform")
	if err != nil || receipt.ID != teamID {
		t.Fatalf("rename: %+v %v", receipt, err)
	}
	got, err := c.GetTeam(t.Context(), teamID)
	if err != nil || got.Name != "Platform" {
		t.Fatalf("read: %+v %v", got, err)
	}
	if err := c.DisableTeam(t.Context(), teamID); err != nil {
		t.Fatal(err)
	}
	if step != 4 {
		t.Fatalf("incomplete lifecycle: %d calls", step)
	}
}

func TestServiceTransferUsesDedicatedEndpoint(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/services/"+serviceID+"/transfer" || r.Header.Get("If-Match") != "" {
			t.Error("wrong transfer request")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["team_id"] != teamID {
			t.Error("wrong transfer body")
		}
		io.WriteString(w, `{"id":"`+serviceID+`"}`)
	})
	r, err := c.TransferService(t.Context(), serviceID, teamID)
	if err != nil || r.ID != serviceID {
		t.Fatalf("transfer: %+v %v", r, err)
	}
}

func TestAPIErrorsPreserveStatusWithoutLeakingBody(t *testing.T) {
	for _, status := range []int{403, 409, 412, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"code":"revision_mismatch","message":"secret-password","details":{"token":"secret-token"}}}`)
			})
			err := c.DisableTeam(t.Context(), teamID)
			var apiErr *client.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status || client.IsNotFound(err) {
				t.Fatalf("lost HTTP failure: %v", err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatal("error leaked the response body")
			}
			if calls != 1 {
				t.Fatal("mutation was retried")
			}
		})
	}
}

func TestDisableAlreadyMissingSucceeds(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"code":"not_found"}}`)
	})
	if err := c.DisableService(t.Context(), serviceID); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedJSONIsNotAbsence(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{broken secret-token`) })
	_, err := c.GetTeam(t.Context(), teamID)
	if err == nil || client.IsNotFound(err) || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe malformed response handling: %v", err)
	}
}

func TestIncompleteOrWrongIdentityResponseIsRejected(t *testing.T) {
	for _, body := range []string{`{}`, `{"id":"` + teamID + `","name":"","active":true}`, `{"id":"` + serviceID + `","name":"Ops","active":true}`} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		_, err := c.GetTeam(t.Context(), teamID)
		if err == nil || client.IsNotFound(err) {
			t.Fatalf("invalid resource response accepted: %s", body)
		}
	}
}

func TestUnverified404DoesNotConfirmAbsenceOrSuccessfulDisable(t *testing.T) {
	for _, body := range []string{`<html>proxy route not found</html>`, `{"error":{"code":"unknown_route"}}`, `{}`} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404); io.WriteString(w, body) })
		_, err := c.GetTeam(t.Context(), teamID)
		if err == nil || client.IsNotFound(err) {
			t.Fatalf("unverified 404 was accepted as absence: %v", err)
		}
		if err = c.DisableTeam(t.Context(), teamID); err == nil {
			t.Fatal("unverified 404 was accepted as successful disable")
		}
	}
}

func TestTeamsAndServicesAcceptUnversionedAPI(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != "" {
			t.Error("team/service writes must not send If-Match")
		}
		switch {
		case r.Method == "PATCH" || strings.HasSuffix(r.URL.Path, "/transfer"):
			id := teamID
			if strings.Contains(r.URL.Path, "services") {
				id = serviceID
			}
			io.WriteString(w, `{"id":"`+id+`"}`)
		case strings.Contains(r.URL.Path, "services"):
			io.WriteString(w, `{"id":"`+serviceID+`","name":"API","team_id":"`+teamID+`","active":true}`)
		default:
			io.WriteString(w, `{"id":"`+teamID+`","name":"Operations","active":true}`)
		}
	})
	if _, err := c.CreateTeam(t.Context(), "Operations"); err != nil {
		t.Error(err)
	}
	if _, err := c.GetTeam(t.Context(), teamID); err != nil {
		t.Error(err)
	}
	if _, err := c.RenameTeam(t.Context(), teamID, "Platform"); err != nil {
		t.Error(err)
	}
	if _, err := c.CreateService(t.Context(), teamID, "API"); err != nil {
		t.Error(err)
	}
	if _, err := c.GetService(t.Context(), serviceID); err != nil {
		t.Error(err)
	}
	if _, err := c.RenameService(t.Context(), serviceID, "Backend"); err != nil {
		t.Error(err)
	}
	if _, err := c.TransferService(t.Context(), serviceID, teamID); err != nil {
		t.Error(err)
	}
}
