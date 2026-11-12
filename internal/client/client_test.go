package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func installTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func useHTTPFixture(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	installTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Result(), nil
	}))
}

func TestNewRejectsInvalidConfigurationWithoutLeakingSecrets(t *testing.T) {
	for _, tt := range []struct{ name, endpoint, token string }{
		{"missing endpoint", "", "secret-token"},
		{"relative endpoint", "localhost:8080", "secret-token"},
		{"unsupported scheme", "ftp://localhost", "secret-token"},
		{"missing host", "https:///v1", "secret-token"},
		{"userinfo", "https://user:secret-password@localhost", "secret-token"},
		{"query", "https://localhost?token=secret-token", "secret-token"},
		{"fragment", "https://localhost/#secret-token", "secret-token"},
		{"malformed", "https://localhost/%secret-token", "secret-token"},
		{"missing token", "https://localhost", ""},
		{"blank token", "https://localhost", " "},
		{"header injection", "https://localhost", "secret-token\r\nX-Test: injected"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.New(tt.endpoint, tt.token)
			if err == nil {
				t.Fatal("expected invalid configuration to be rejected")
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error discloses sensitive input: %v", err)
			}
		})
	}
}

func TestDoPreservesPrefixQueryBodyAndAuthentication(t *testing.T) {
	useHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.RequestURI() != "/onpager/v1/teams/team-id?limit=2" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing API authentication or JSON headers")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"name":"Platform"}` {
			t.Errorf("unexpected request body: %q, %v", body, err)
		}
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = io.WriteString(w, `{"error":{"code":"revision_mismatch"}}`)
	})
	c, err := client.New("https://control.example/onpager/", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(t.Context(), http.MethodPatch, "/v1/teams/team-id?limit=2", strings.NewReader(`{"name":"Platform"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("HTTP status was lost: %d", resp.StatusCode)
	}
}

func TestDoRejectsRoutesThatEscapeConfiguredEndpoint(t *testing.T) {
	useHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid route reached the backend")
	})
	c, err := client.New("https://control.example/onpager", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"https://example.com/v1/teams", "//example.com/v1/teams", "../teams", "v1/../teams", "v1/%2e%2e/teams", "v1/teams#fragment", "v1/%zz"} {
		t.Run(route, func(t *testing.T) {
			resp, err := c.Do(t.Context(), http.MethodGet, route, nil)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil {
				t.Fatal("expected invalid route to be rejected")
			}
		})
	}
}

func TestDoDoesNotFollowRedirectsOrReplayMutations(t *testing.T) {
	requests := 0
	useHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			t.Error("client followed a redirect or replayed a mutation")
		}
		w.Header().Set("Location", "/other-endpoint")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	c, err := client.New("https://control.example", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(t.Context(), http.MethodPost, "v1/teams", strings.NewReader(`{"name":"Platform"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status was lost: %d", resp.StatusCode)
	}
}

func TestDoPropagatesCancellation(t *testing.T) {
	started := make(chan struct{})
	installTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	c, err := client.New("https://control.example", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		resp, err := c.Do(ctx, http.MethodGet, "v1/teams", nil)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("request ended before reaching the backend: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
