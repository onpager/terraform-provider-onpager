package client_test

import (
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestNormalizeProviderBaseURL(t *testing.T) {
	const prefix = "http://mock/"
	atLimit := prefix + strings.Repeat("a", 16384-len(prefix))
	for raw, want := range map[string]string{
		"":                                "",
		"https://MOCK:443/slack/api///":   "https://mock/slack/api",
		"http://[::1]:080/":               "http://[::1]",
		"http://[::1]:8080/st%61nd/a%3ab": "http://[::1]:8080/stand/a%3Ab",
		atLimit:                           atLimit,
	} {
		if got, err := client.NormalizeProviderBaseURL(raw); err != nil || got != want {
			t.Errorf("normalized URL differs for input of %d bytes: %v", len(raw), err)
		}
	}
	for _, raw := range []string{
		"//mock/api", "ftp://mock", "http://:80", "http://mock:0", "http://mock:65536",
		"http://mock:", "http://user:private-marker@mock", "http://mock/api?private-marker",
		"http://mock/api?", "http://mock/api#", "http://mock/a/%2e%2e/b", "http://mock/a//b",
		"http://mock/a%2Fb", "http://mock/a%5cb", "http://mock/%0a", "http://mock/a b", atLimit + "b",
	} {
		if _, err := client.NormalizeProviderBaseURL(raw); err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Errorf("expected a redacted validation error for input of %d bytes", len(raw))
		}
	}
}
