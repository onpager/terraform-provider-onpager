package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const emptyNotificationTemplate = `{"enabled":false,"contacts":[],"policies":{"default":{"configured":false,"steps":[]},"important":{"configured":false,"steps":[]}}}`

func testNotificationTemplate() client.UserNotificationDefaultsInput {
	wait := int64(30)
	slot := teamID
	return client.UserNotificationDefaultsInput{
		Contacts: []client.NotificationDefaultContact{{ID: slot, ChannelID: serviceID, DisplayName: "Pager", LookupAttribute: "email"}},
		Policies: client.NotificationDefaultPolicies{
			Default:   client.NotificationDefaultPolicy{Configured: true, Steps: []client.NotificationDefaultStep{{Kind: "notify", ContactID: &slot}, {Kind: "wait", WaitSeconds: &wait}}},
			Important: client.NotificationDefaultPolicy{Configured: true, Steps: []client.NotificationDefaultStep{}},
		},
	}
}

func TestUserNotificationDefaultsCompleteTemplateAndReset(t *testing.T) {
	const path = "/v1/settings/user-notification-defaults"
	template := testNotificationTemplate()
	bodies := []string{
		`{"enabled":false,"contacts":[{"id":"` + teamID + `","channel_id":"` + serviceID + `","display_name":"Pager","require_verification":false,"lookup_attribute":"email"}],"policies":{"default":{"configured":true,"steps":[{"kind":"notify","contact_id":"` + teamID + `"},{"kind":"wait","wait_seconds":30}]},"important":{"configured":true,"steps":[]}}}`,
		emptyNotificationTemplate,
	}
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.RequestURI() != path || req.Header.Get("If-Match") != "" {
			t.Error("unexpected route or precondition")
		}
		if calls == 1 {
			if req.Method != http.MethodGet {
				t.Error("expected detail GET")
			}
			io.WriteString(w, strings.Replace(emptyNotificationTemplate, "{", `{"revision":0,`, 1))
			return
		}
		if req.Method != http.MethodPut || calls > 3 {
			t.Errorf("unexpected request %d %s", calls, req.Method)
			return
		}
		body, _ := io.ReadAll(req.Body)
		var got, want any
		if json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(bodies[calls-2]), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Error("PUT did not send exactly the complete desired snapshot")
		}
		io.WriteString(w, strings.Replace(bodies[calls-2], "{", `{"revision":12,`, 1))
	})
	v, err := c.GetUserNotificationDefaults(t.Context())
	if err != nil || !v.IsEmpty() || v.Revision != 0 || v.Committed {
		t.Fatal("pristine read", err)
	}
	for _, in := range []client.UserNotificationDefaultsInput{template, client.EmptyUserNotificationDefaults()} {
		got, err := c.ReplaceUserNotificationDefaults(t.Context(), in)
		if err != nil || !got.Committed || got.Revision != 12 || !reflect.DeepEqual(got.UserNotificationDefaultsInput, in) {
			t.Fatal("PUT receipt", err)
		}
	}
}

func TestUserNotificationDefaultsClientRejectsInvalidTemplate(t *testing.T) {
	const maxWait = int64(math.MaxInt64 / 1_000_000_000)
	for _, tc := range []struct {
		name   string
		change func(*client.UserNotificationDefaultsInput)
	}{
		{"null contacts", func(v *client.UserNotificationDefaultsInput) { v.Contacts = nil }},
		{"zero UUID", func(v *client.UserNotificationDefaultsInput) {
			v.Contacts[0].ID = "00000000-0000-0000-0000-000000000000"
		}},
		{"duplicate channel", func(v *client.UserNotificationDefaultsInput) {
			c := v.Contacts[0]
			c.ID = serviceID
			v.Contacts = append(v.Contacts, c)
		}},
		{"duplicate slot", func(v *client.UserNotificationDefaultsInput) {
			c := v.Contacts[0]
			c.ChannelID = teamID
			v.Contacts = append(v.Contacts, c)
		}},
		{"name normalization", func(v *client.UserNotificationDefaultsInput) { v.Contacts[0].DisplayName = " Pager" }},
		{"name limit", func(v *client.UserNotificationDefaultsInput) { v.Contacts[0].DisplayName = strings.Repeat("я", 201) }},
		{"null steps", func(v *client.UserNotificationDefaultsInput) { v.Policies.Important.Steps = nil }},
		{"dangling slot", func(v *client.UserNotificationDefaultsInput) {
			id := serviceID
			v.Policies.Default.Steps[0].ContactID = &id
		}},
		{"unconfigured steps", func(v *client.UserNotificationDefaultsInput) { v.Policies.Default.Configured = false }},
		{"wait seconds", func(v *client.UserNotificationDefaultsInput) { *v.Policies.Default.Steps[1].WaitSeconds = maxWait + 1 }},
		{"notify and wait", func(v *client.UserNotificationDefaultsInput) {
			v.Policies.Default.Steps[0].WaitSeconds = v.Policies.Default.Steps[1].WaitSeconds
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := testNotificationTemplate()
			tc.change(&in)
			c := fixtureClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid template sent") })
			got, err := c.ReplaceUserNotificationDefaults(t.Context(), in)
			if err == nil || got.Committed {
				t.Fatal("invalid template accepted")
			}
		})
	}
}

func TestUserNotificationDefaultsClientPreservesIntentOnMalformedReceipts(t *testing.T) {
	template := testNotificationTemplate()
	data, err := json.Marshal(client.UserNotificationDefaults{UserNotificationDefaultsInput: template, Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	for _, tc := range []struct{ name, body string }{
		{"missing enabled", strings.Replace(valid, `"enabled":false,`, "", 1)},
		{"null revision", strings.Replace(valid, `"revision":2`, `"revision":null`, 1)},
		{"missing verification", strings.Replace(valid, `"require_verification":false,`, "", 1)},
		{"missing policy", strings.Replace(valid, `,"important":{"configured":true,"steps":[]}`, "", 1)},
		{"missing configured", strings.Replace(valid, `"configured":true,`, "", 1)},
		{"null steps", strings.Replace(valid, `"steps":[]`, `"steps":null`, 1)},
		{"zero revision with stored template", strings.Replace(valid, `"revision":2`, `"revision":0`, 1)},
		{"different template", strings.Replace(valid, `"display_name":"Pager"`, `"display_name":"Other"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, tc.body) })
			got, err := c.ReplaceUserNotificationDefaults(t.Context(), template)
			if err == nil || !got.Committed || !reflect.DeepEqual(got.UserNotificationDefaultsInput, template) {
				t.Fatalf("malformed receipt lost intent or was accepted: %v", err)
			}
			if tc.name != "different template" {
				if _, err := c.GetUserNotificationDefaults(t.Context()); err == nil {
					t.Fatal("malformed GET accepted")
				}
			}
		})
	}
}

func TestUserNotificationDefaultsClientFailedWritesAreSanitized(t *testing.T) {
	for _, status := range []int{204, 401, 403, 404, 422, 500} {
		calls := 0
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"forbidden","message":"private-marker"}}`)
		})
		v, err := c.ReplaceUserNotificationDefaults(t.Context(), client.EmptyUserNotificationDefaults())
		if err == nil || strings.Contains(err.Error(), "private-marker") || v.Committed != (status == 204) || calls != 1 {
			t.Fatal("unsafe or retried write", status, err)
		}
	}
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached API") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetUserNotificationDefaults(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("lost request cancellation", err)
	}
}
