package client_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestIntegrationUsesOwnRevisionAndPreservesOneTimeReceipt(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/integrations" || r.Header.Get("If-Match") != "" {
				t.Error("unexpected integration create")
			}
			io.WriteString(w, `{"id":"`+teamID+`","name":"Metrics","source_kind":"alertmanager","enabled":true,"revision":1,"webhook_path":"/v1/integrations/x/events","token":"one-time-fixture"}`)
		case 2:
			if r.Method != "PATCH" || r.Header.Get("If-Match") != `"1"` {
				t.Error("integration PATCH must use its observed numeric revision")
			}
			var input map[string]any
			json.NewDecoder(r.Body).Decode(&input)
			poll := input["polling"].(map[string]any)
			if input["enabled"] != false || poll["password"] != "" {
				t.Error("explicit false/empty password was lost")
			}
			if _, exists := poll["token"]; exists {
				t.Error("omitted polling token must not be serialized as null/empty")
			}
			io.WriteString(w, `{"id":"`+teamID+`","revision":2}`)
		case 3:
			if r.Method != "DELETE" || r.Header.Get("If-Match") != `"2"` {
				t.Error("destroy must call integration DELETE with observed revision")
			}
			w.WriteHeader(204)
		}
	})
	v, err := c.CreateIntegration(t.Context(), client.IntegrationInput{Name: "Metrics", SourceKind: "alertmanager"})
	if err != nil || v.Token != "one-time-fixture" {
		t.Fatalf("create receipt: %v", err)
	}
	off, empty := false, ""
	receipt, err := c.UpdateIntegration(t.Context(), v.ID, v.Revision, client.IntegrationPatch{Enabled: &off, Polling: &client.IntegrationPollingInput{Enabled: false, AuthKind: "basic", Username: "metrics", Password: &empty}})
	if err != nil || receipt.Revision != 2 {
		t.Fatalf("update: %v", err)
	}
	if err := c.DeleteIntegration(t.Context(), v.ID, receipt.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationRejectsWrongIdentityAndNeverRetriesConflict(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "GET" {
			io.WriteString(w, `{"id":"`+serviceID+`","name":"Metrics","source_kind":"generic","revision":1,"enabled":true}`)
			return
		}
		w.WriteHeader(412)
		io.WriteString(w, `{"error":{"code":"revision_mismatch","message":"secret-fixture"}}`)
	})
	if _, err := c.GetIntegration(t.Context(), teamID); err == nil || client.IsNotFound(err) {
		t.Fatal("wrong integration UUID accepted")
	}
	err := c.DeleteIntegration(t.Context(), teamID, 7)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 412 || calls != 2 {
		t.Fatal("revision conflict swallowed or retried")
	}
}
