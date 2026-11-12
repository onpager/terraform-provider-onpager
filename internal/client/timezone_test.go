package client_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestRequestTimezoneOption(t *testing.T) {
	for _, zone := range []string{"", "UTC", "Europe/Moscow", "Asia/Kathmandu"} {
		t.Run("zone="+zone, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				values := r.Header.Values("Onpager-Timezone")
				if zone == "" && len(values) != 0 || zone != "" && (len(values) != 1 || values[0] != zone) {
					t.Errorf("unexpected timezone header: %v", values)
				}
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing bearer authentication")
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			options := []client.Option{}
			if zone != "" {
				options = append(options, client.WithTimezone(zone))
			}
			c, err := client.New(server.URL, "fixture-token", options...)
			if err != nil {
				t.Fatal(err)
			}
			if c.Timezone() != zone || requests != 0 {
				t.Fatal("configuration performed IO or lost timezone")
			}
			response, err := c.Do(t.Context(), "GET", "/v1/teams", nil)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if requests != 1 {
				t.Fatal("unexpected retries")
			}
		})
	}
}
func TestRequestTimezoneRejectsInvalidConfiguration(t *testing.T) {
	for _, zone := range []string{"", "Local", "+03:00", "UTC+3", "Invalid/Zone", " Europe/Moscow", "UTC,Europe/Moscow", "../UTC", "UTC\r\nInjected: value"} {
		t.Run("invalid", func(t *testing.T) {
			c, err := client.New("https://unused.invalid", "fixture-token", client.WithTimezone(zone))
			if err == nil || c != nil {
				t.Fatal("invalid timezone produced a client")
			}
			if zone != "" && strings.Contains(err.Error(), zone) {
				t.Fatal("configuration error echoed input")
			}
		})
	}
}
