package client_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestUserBotContactUpsertReceipts(t *testing.T) {
	bot, name := serviceID, "Draft"
	for _, status := range []int{200, 201} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.Method != "POST" || r.URL.RequestURI() != "/v1/users/"+teamID+"/bot-contacts" || r.Header.Get("If-Match") != "" || strings.Contains(string(body), "recipient_id") || strings.Contains(string(body), "verified") {
				t.Error("unexpected managed contact request")
			}
			w.WriteHeader(status)
			if status == 201 {
				fmt.Fprintf(w, `{"id":%q,"bot_id":%q,"display_name":"Draft","verified":false}`, teamID, bot)
			} else {
				fmt.Fprintf(w, `{"id":%q,"bot_id":%q,"display_name":"Draft","recipient_id":"recorded","verified":true}`, teamID, bot)
			}
		})
		v, err := c.UpsertUserBotContact(t.Context(), teamID, client.UserBotContactInput{BotID: &bot, DisplayName: &name})
		if err != nil || !v.Committed || v.ID != teamID || (v.RecipientID == "recorded") != (status == 200) || v.Verified != (status == 200) || v.Created != (status == 201) {
			t.Fatal("upsert lost returned contact", err)
		}
	}
}

func TestUserBotContactChecksReadReceiptsWithoutLeakingAddresses(t *testing.T) {
	for _, raw := range []string{
		fmt.Sprintf(`{"id":%q,"bot_id":%q,"display_name":"Contact"}`, teamID, serviceID),
		fmt.Sprintf(`{"id":%q,"bot_id":%q,"display_name":"Contact","verified":true}`, teamID, serviceID),
		fmt.Sprintf(`{"id":%q,"bot_id":%q,"display_name":"Contact","verified":false}`, serviceID, serviceID),
		fmt.Sprintf(`{"id":%q,"bot_id":%q,"display_name":"Contact","verified":false,"recipient_id":"private marker"}`, teamID, serviceID),
	} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.RequestURI() != "/v1/users/"+teamID+"/bot-contacts/"+teamID {
				t.Error("unexpected detail read")
			}
			fmt.Fprint(w, raw)
		})
		if _, err := c.GetUserBotContact(t.Context(), teamID, teamID); err == nil || strings.Contains(err.Error(), "private marker") {
			t.Fatal("invalid contact receipt accepted or disclosed")
		}
	}
}

func TestUserBotContactDeleteOnlyConfirmsAbsence(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code      string
		wantError bool
	}{
		{204, "", false}, {404, "not_found", false}, {404, "unknown_route", true}, {403, "forbidden", true}, {405, "method_not_allowed", true}, {409, "conflict", true}, {200, "", true},
	} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "DELETE" || r.URL.RequestURI() != "/v1/users/"+teamID+"/bot-contacts/"+serviceID || r.Header.Get("If-Match") != "" {
				t.Error("unexpected delete request")
			}
			w.WriteHeader(tc.status)
			if tc.status != 204 {
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"private marker"}}`, tc.code)
			}
		})
		err := c.DeleteUserBotContact(t.Context(), teamID, serviceID)
		if (err != nil) != tc.wantError || err != nil && strings.Contains(err.Error(), "private marker") {
			t.Fatal("delete did not distinguish absence from failure", tc.status, err)
		}
	}
}

func TestUserBotContactInvalidFieldsAndCancellation(t *testing.T) {
	c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input reached API") })
	bot, name := serviceID, "Contact"
	for _, recipient := range []string{"", strings.Repeat("я", 513), "a b", "a\u00a0b", "a\u0007b"} {
		_, err := c.UpsertUserBotContact(t.Context(), teamID, client.UserBotContactInput{BotID: &bot, DisplayName: &name, RecipientID: &recipient})
		if err == nil {
			t.Fatal("invalid recipient accepted")
		}
	}
	for _, badName := range []string{"", " extra", strings.Repeat("я", 101)} {
		_, err := c.UpsertUserBotContact(t.Context(), teamID, client.UserBotContactInput{BotID: &bot, DisplayName: &badName})
		if err == nil {
			t.Fatal("invalid name accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetUserBotContact(ctx, teamID, serviceID); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}

func TestUserBotContactRequiresNewDraftSemanticsOnlyOn201(t *testing.T) {
	bot, name := serviceID, "Contact"
	for _, fields := range []string{`"recipient_id":"other","verified":true`, `"recipient_id":"other","verified":false`} {
		c := fixtureClient(t, func(w http.ResponseWriter, q *http.Request) {
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"id":%q,"bot_id":%q,"display_name":"Contact",%s}`, teamID, bot, fields)
		})
		v, err := c.UpsertUserBotContact(t.Context(), teamID, client.UserBotContactInput{BotID: &bot, DisplayName: &name})
		if err == nil || v.ID != teamID || !v.Committed || !v.Created {
			t.Fatal("new binding did not reject contradictory server defaults or lost its committed identity")
		}
	}
}

func TestUserBotContactUnexpectedSuccessRetainsCompatibleUpsertIdentity(t *testing.T) {
	bot, name := serviceID, "Contact"
	c := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
		fmt.Fprintf(w, `{"id":%q,"bot_id":%q}`, teamID, bot)
	})
	v, err := c.UpsertUserBotContact(t.Context(), teamID, client.UserBotContactInput{BotID: &bot, DisplayName: &name})
	if err == nil || !v.Committed || v.ID != teamID {
		t.Fatal("unexpected success acknowledged or recoverable method ID discarded")
	}
}
