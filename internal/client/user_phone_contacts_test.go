package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const phoneTargetUUID = "33333333-3333-4333-8333-333333333333"

func phoneContactJSON() map[string]any {
	return map[string]any{"id": serviceID, "user_id": teamID, "channel_id": phoneChannelUUID, "target_id": phoneTargetUUID, "display_name": "Phone", "masked_phone": "+…0123", "verified": false, "enabled": true, "draft": false, "generation": 0}
}

const phoneChannelUUID = "55555555-5555-4555-8555-555555555555"

func TestUserPhoneContactReadRejectsBrokenAndForeignCards(t *testing.T) {
	for _, fields := range []map[string]any{{"id": teamID}, {"user_id": serviceID}, {"channel_id": nil}, {"target_id": nil}, {"draft": true}, {"masked_phone": "+12025550123"}, {"enabled": nil}, {"verified": nil}, {"generation": -1}, {"generation": nil}} {
		c := fixtureClient(t, func(w http.ResponseWriter, q *http.Request) {
			if q.Method != "GET" || q.URL.RequestURI() != "/v1/users/"+teamID+"/phone-contacts/"+serviceID {
				t.Error("unexpected detail operation")
			}
			v := phoneContactJSON()
			for k, value := range fields {
				v[k] = value
			}
			json.NewEncoder(w).Encode(v)
		})
		_, err := c.GetUserPhoneContact(t.Context(), teamID, serviceID)
		if err == nil || strings.Contains(err.Error(), "0123") {
			t.Fatalf("accepted incomplete, foreign or unsafe card for fields %v", fields)
		}
	}
}

func TestUserPhoneContactReplayMayReturnDifferentLiveFields(t *testing.T) {
	for _, status := range []int{200, 201} {
		c := fixtureClient(t, func(w http.ResponseWriter, q *http.Request) {
			var body map[string]any
			if json.NewDecoder(q.Body).Decode(&body) != nil || q.Method != "POST" || q.URL.RequestURI() != "/v1/users/"+teamID+"/phone-contacts" || q.Header.Get("If-Match") != "" || len(body) != 5 || body["template_contact_id"] != phoneTargetUUID || body["request_id"] != teamID {
				t.Error("incorrect scoped create request")
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(phoneContactJSON())
		})
		phone, name, channel, slot, key := "+13035559999", "Changed", phoneChannelUUID, phoneTargetUUID, teamID
		result, err := c.CreateUserPhoneContact(t.Context(), teamID, client.UserPhoneContactInput{Phone: &phone, DisplayName: &name, ChannelID: &channel, TemplateContactID: &slot, RequestID: &key})
		if (err != nil) != (status == 201) || result.ID != serviceID || !result.Committed || result.Created != (status == 201) {
			t.Fatal("incorrect new/replayed receipt handling", status, err)
		}
	}
}

func TestUserPhoneContactPatchValidationAndNoSecretDisclosure(t *testing.T) {
	c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid patch reached API") })
	name := strings.Repeat("Я", 201)
	phone := "+12025550123"
	key := teamID
	yes := true
	badPhone := "+1202555 123"
	for _, patch := range []client.UserPhoneContactInput{{Phone: &badPhone}, {DisplayName: &name}, {Verified: &yes, Phone: &phone}, {Verified: &yes, ChannelID: &key}, {RequestID: &key}, {TemplateContactID: &key}} {
		if _, err := c.UpdateUserPhoneContact(t.Context(), teamID, serviceID, patch); err == nil || strings.Contains(err.Error(), "1202555") {
			t.Fatal("invalid patch accepted or phone leaked")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetUserPhoneContact(ctx, teamID, serviceID); !errors.Is(err, context.Canceled) {
		t.Fatal("lost context cancellation", err)
	}
}

func TestUserPhoneContactDeleteRequiresConfirmedScopedAbsence(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		ok     bool
	}{{204, "", true}, {404, "not_found", true}, {404, "unknown_route", false}, {403, "forbidden", false}, {409, "conflict", false}, {200, "", false}} {
		c := fixtureClient(t, func(w http.ResponseWriter, q *http.Request) {
			if q.Method != "DELETE" || q.URL.RequestURI() != "/v1/users/"+teamID+"/phone-contacts/"+serviceID || q.Header.Get("If-Match") != "" {
				t.Error("unexpected delete")
			}
			w.WriteHeader(tc.status)
			if tc.status != 204 {
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"private-address"}}`, tc.code)
			}
		})
		err := c.DeleteUserPhoneContact(t.Context(), teamID, serviceID)
		if (err == nil) != tc.ok || err != nil && strings.Contains(err.Error(), "private-address") {
			t.Fatal(tc.status, err)
		}
	}
}
