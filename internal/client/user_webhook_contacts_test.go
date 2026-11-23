package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const webhookMethodID = "33333333-3333-4333-8333-333333333333"

func webhookContactReceipt() map[string]any {
	return map[string]any{
		"user_id": teamID, "webhook_id": serviceID, "id": webhookMethodID,
		"display_name": "Receiver", "values": map[string]string{"recipient": "private-marker"},
		"draft": false, "generation": 1, "verified": true, "enabled": true, "etag": `"1"`,
	}
}

func TestUserWebhookContactCreateUsesDurableKeyAndSlot(t *testing.T) {
	slot := "44444444-4444-4444-8444-444444444444"
	for _, status := range []int{200, 201} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			want := map[string]any{"request_id": webhookMethodID, "webhook_id": serviceID, "template_contact_id": slot, "values": map[string]any{}, "display_name": "Receiver"}
			if r.Method != "POST" || r.URL.RequestURI() != "/v1/users/"+teamID+"/webhook-contacts" || r.Header.Get("If-Match") != "" || !reflect.DeepEqual(body, want) {
				t.Error("wrong scoped create or optional fields")
			}
			w.Header().Set("ETag", `"1"`)
			w.WriteHeader(status)
			receipt := webhookContactReceipt()
			receipt["values"] = map[string]string{"optional": ""}
			_ = json.NewEncoder(w).Encode(receipt)
		})
		v, err := c.CreateUserWebhookContact(t.Context(), teamID, client.UserWebhookContactCreate{RequestID: webhookMethodID, WebhookID: serviceID, TemplateContactID: &slot, DisplayName: "Receiver", Values: map[string]string{}})
		if err != nil || v.ID != webhookMethodID || v.ETag != `"1"` || !v.Verified {
			t.Fatal(err)
		}
	}
}

func TestUserWebhookContactStrictReadReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		header      string
	}{
		{"valid", "", "", `"1"`}, {"owner", "user_id", serviceID, `"1"`}, {"definition", "webhook_id", teamID, `"1"`}, {"method", "id", serviceID, `"1"`},
		{"weak", "", "", `W/"1"`}, {"missing tag", "", "", ""}, {"wrong tag", "", "", `"2"`}, {"noncanonical", "etag", `"01"`, `"01"`},
		{"null map", "values", nil, `"1"`}, {"null element", "values", map[string]any{"secret": nil}, `"1"`}, {"missing verification", "verified", nil, `"1"`},
		{"invalid generation", "generation", -1, `"1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.RequestURI() != "/v1/users/"+teamID+"/webhook-contacts/"+webhookMethodID || r.Header.Get("If-Match") != "" {
					t.Error("unexpected read")
				}
				w.Header().Set("ETag", tc.header)
				view := webhookContactReceipt()
				if tc.field != "" {
					view[tc.field] = tc.value
				}
				_ = json.NewEncoder(w).Encode(view)
			})
			_, err := c.GetUserWebhookContact(t.Context(), teamID, webhookMethodID, serviceID)
			if (err == nil) != (tc.name == "valid") || err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("untrusted receipt accepted or leaked", err)
			}
		})
	}
}

func TestUserWebhookContactDeleteRequiresOwnTagAndConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		status   int
		code     string
		accepted bool
	}{{204, "", true}, {404, "not_found", true}, {404, "unknown_route", false}, {403, "forbidden", false}, {412, "configuration_conflict", false}, {200, "", false}} {
		calls := 0
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != "DELETE" || r.URL.RequestURI() != "/v1/users/"+teamID+"/webhook-contacts/"+webhookMethodID || r.Header.Get("If-Match") != `"7"` {
				t.Error("missing scoped identity or guarded deletion")
			}
			w.WriteHeader(tc.status)
			if tc.status != 204 {
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"private-marker"}}`, tc.code)
			}
		})
		if err := c.DeleteUserWebhookContact(t.Context(), teamID, webhookMethodID, ""); err == nil || calls != 0 {
			t.Fatal("unguarded delete reached API")
		}
		err := c.DeleteUserWebhookContact(t.Context(), teamID, webhookMethodID, `"7"`)
		if (err == nil) != tc.accepted || calls != 1 || err != nil && strings.Contains(err.Error(), "private-marker") {
			t.Fatal("wrong delete outcome", err)
		}
		if tc.status == 412 {
			var ae *client.APIError
			if !errors.As(err, &ae) || ae.StatusCode != 412 {
				t.Fatal("conflict lost", err)
			}
		}
	}
}
