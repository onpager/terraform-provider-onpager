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

func TestNotificationTemplateExactOverrideAndCAS(t *testing.T) {
	tag := `"` + strings.Repeat("a", 64) + `"`
	for _, team := range []string{"", teamID} {
		t.Run(team, func(t *testing.T) {
			calls := 0
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := "/v1/notification-templates/alert_direct/telegram/en"
				if team != "" {
					path = "/v1/teams/" + team + "/notification-templates/alert_direct/telegram/en"
				}
				if r.URL.Path != path || r.URL.RawQuery != "" {
					t.Errorf("unexpected route %s", r.URL.String())
				}
				w.Header().Set("ETag", tag)
				if r.Method == "GET" {
					io.WriteString(w, `{"override":null,"effective":{"format":"telegram_html","body":"Inherited"},"source":"builtin"}`)
					return
				}
				if r.Header.Get("If-Match") != tag {
					t.Error("current strong ETag missing")
				}
				if r.Method == "DELETE" {
					io.WriteString(w, `{"override":null,"effective":{"format":"telegram_html","body":"Inherited"},"source":"builtin"}`)
					return
				}
				if r.Method != "PUT" {
					t.Fatalf("unexpected method %s", r.Method)
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if len(body) != 2 || body["format"] != "telegram_html" || body["body"] != "Custom" {
					t.Errorf("wrong payload: %v", body)
				}
				source := "global"
				if team != "" {
					source = "team"
				}
				json.NewEncoder(w).Encode(map[string]any{"override": map[string]string{"format": "telegram_html", "body": "Custom"}, "effective": map[string]string{"format": "telegram_html", "body": "Custom"}, "source": source})
			})
			v, err := c.GetNotificationTemplate(t.Context(), team, "alert_direct", "telegram", "en")
			if err != nil || v.Override != nil || v.ETag != tag {
				t.Fatalf("read inherited override: %+v %v", v, err)
			}
			v, err = c.PutNotificationTemplate(t.Context(), team, "alert_direct", "telegram", "en", tag, client.NotificationTemplateDraft{Format: "telegram_html", Body: "Custom"})
			if err != nil || v.Override == nil || v.Override.Body != "Custom" || !v.Committed {
				t.Fatalf("put: %+v %v", v, err)
			}
			v, err = c.DeleteNotificationTemplate(t.Context(), team, "alert_direct", "telegram", "en", tag)
			if err != nil || v.Override != nil || !v.Committed || calls != 3 {
				t.Fatalf("reset: %+v %v calls=%d", v, err, calls)
			}
		})
	}
}
func TestNotificationTemplateRejectsMalformedViews(t *testing.T) {
	for _, body := range []string{`{}`, `{"effective":{"format":"telegram_html","body":"x"},"source":"builtin"}`, `{"override":null,"source":"builtin"}`, `{"override":{},"effective":{"format":"telegram_html","body":"x"},"source":"global"}`, `{"override":null,"effective":{"format":"telegram_html","body":"x"},"source":"team"}`} {
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"`+strings.Repeat("b", 64)+`"`)
			io.WriteString(w, body)
		})
		if _, err := c.GetNotificationTemplate(t.Context(), "", "alert_direct", "telegram", "en"); err == nil {
			t.Fatalf("accepted malformed view %s", body)
		}
	}
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"override":null,"effective":{"format":"telegram_html","body":"x"},"source":"builtin"}`)
	})
	if _, err := c.GetNotificationTemplate(t.Context(), "", "alert_direct", "telegram", "en"); err == nil {
		t.Fatal("missing ETag accepted")
	}
}
func TestNotificationTemplateConflictsAndMalformedCommittedResponse(t *testing.T) {
	for _, status := range []int{403, 409, 412, 428} {
		calls := 0
		c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"code":"precondition_failed","message":"secret"}}`)
		})
		v, err := c.DeleteNotificationTemplate(t.Context(), "", "alert_direct", "telegram", "en", `"`+strings.Repeat("c", 64)+`"`)
		var api *client.APIError
		if !errors.As(err, &api) || api.StatusCode != status || v.Committed || calls != 1 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("failure lost/retried: %+v %v", v, err)
		}
	}
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"`+strings.Repeat("d", 64)+`"`)
		io.WriteString(w, `{}`)
	})
	v, err := c.PutNotificationTemplate(t.Context(), teamID, "alert_direct", "telegram", "en", `"`+strings.Repeat("c", 64)+`"`, client.NotificationTemplateDraft{Format: "telegram_html", Body: "Saved"})
	if err == nil || !v.Committed || v.ETag == "" {
		t.Fatal("committed mutation evidence lost")
	}
}

func TestNotificationTemplateMutationRequiresConfirmation(t *testing.T) {
	for _, method := range []string{"PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"`+strings.Repeat("f", 64)+`"`)
				io.WriteString(w, `{"override":{"format":"telegram_html","body":"Unexpected"},"effective":{"format":"telegram_html","body":"Unexpected"},"source":"global"}`)
			})
			tag := `"` + strings.Repeat("e", 64) + `"`
			var v client.NotificationTemplateView
			var err error
			if method == "PUT" {
				v, err = c.PutNotificationTemplate(t.Context(), "", "alert_direct", "telegram", "en", tag, client.NotificationTemplateDraft{Format: "telegram_html", Body: "Expected"})
			} else {
				v, err = c.DeleteNotificationTemplate(t.Context(), "", "alert_direct", "telegram", "en", tag)
			}
			if err == nil || !v.Committed {
				t.Fatal("mutation accepted without confirming the requested result")
			}
		})
	}
}

func TestNotificationTemplateMalformedETagIsNotSavedAfterCommittedPut(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"unsafe"`)
		io.WriteString(w, `{"override":{"format":"telegram_html","body":"Saved"},"effective":{"format":"telegram_html","body":"Saved"},"source":"global"}`)
	})
	v, err := c.PutNotificationTemplate(t.Context(), "", "alert_direct", "telegram", "en", `"`+strings.Repeat("a", 64)+`"`, client.NotificationTemplateDraft{Format: "telegram_html", Body: "Saved"})
	if err == nil || !v.Committed || v.ETag != "" {
		t.Fatalf("invalid concurrency token retained: %+v %v", v, err)
	}
}

func TestNotificationTemplateLanguagesKeepSeparateContentAndCAS(t *testing.T) {
	for _, team := range []string{"", teamID} {
		t.Run(team, func(t *testing.T) {
			ruTag, enTag, savedTag := `"`+strings.Repeat("a", 64)+`"`, `"`+strings.Repeat("b", 64)+`"`, `"`+strings.Repeat("c", 64)+`"`
			stored := false
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				prefix := "/v1"
				if team != "" {
					prefix = "/v1/teams/" + team
				}
				var override any
				source, tag, body := "builtin", ruTag, "Русский builtin"
				switch r.URL.Path {
				case prefix + "/notification-templates/alert_direct/zvonok/en":
					if r.Method != "GET" {
						t.Fatal("Russian override changed the English slot")
					}
					tag, body = enTag, "English builtin"
				case prefix + "/notification-templates/alert_direct/zvonok/ru":
					if r.Method == "PUT" {
						if r.Header.Get("If-Match") != ruTag {
							t.Fatal("another language's absence ETag was used")
						}
						var draft client.NotificationTemplateDraft
						if err := json.NewDecoder(r.Body).Decode(&draft); err != nil || draft.Format != "plain" || draft.Body != "  русский\n source " {
							t.Fatal("voice source changed")
						}
						stored = true
					} else if r.Method == "DELETE" {
						if r.Header.Get("If-Match") != savedTag {
							t.Fatal("another language's override ETag was used")
						}
						stored = false
					}
					if stored {
						tag, body, source = savedTag, "  русский\n source ", "global"
						if team != "" {
							source = "team"
						}
						override = map[string]string{"format": "plain", "body": body}
					}
				default:
					t.Fatalf("unexpected key %s", r.URL.Path)
				}
				w.Header().Set("ETag", tag)
				json.NewEncoder(w).Encode(map[string]any{"override": override, "effective": map[string]string{"format": "plain", "body": body}, "source": source, "allowed_actions": map[string]bool{"read": true, "update": true, "delete": true, "preview": true, "test": false}})
			})
			ru, err := c.GetNotificationTemplate(t.Context(), team, "alert_direct", "zvonok", "ru")
			if err != nil || ru.ETag != ruTag || ru.Override != nil {
				t.Fatalf("RU absence: %+v %v", ru, err)
			}
			ru, err = c.PutNotificationTemplate(t.Context(), team, "alert_direct", "zvonok", "ru", ru.ETag, client.NotificationTemplateDraft{Format: "plain", Body: "  русский\n source "})
			if err != nil || ru.ETag != savedTag || ru.Override == nil {
				t.Fatalf("RU save: %+v %v", ru, err)
			}
			readEnglish := func() {
				t.Helper()
				en, err := c.GetNotificationTemplate(t.Context(), team, "alert_direct", "zvonok", "en")
				if err != nil || en.ETag != enTag || en.Override != nil || en.Effective.Body != "English builtin" {
					t.Fatalf("EN inherited content changed: %+v %v", en, err)
				}
			}
			readEnglish()
			ru, err = c.DeleteNotificationTemplate(t.Context(), team, "alert_direct", "zvonok", "ru", ru.ETag)
			if err != nil || ru.ETag != ruTag || ru.Override != nil || ru.Effective.Body != "Русский builtin" {
				t.Fatalf("RU reset: %+v %v", ru, err)
			}
			readEnglish()
		})
	}
}
