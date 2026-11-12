// Package client provides the transport shared by OnPager resources and data sources.
package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client communicates with one configured OnPager Control endpoint.
type Client struct {
	endpoint *url.URL
	token    string
	http     *http.Client
	timezone string
}

// New validates configuration without contacting the backend. Error messages
// intentionally omit input values because URLs and tokens can contain secrets.
func New(endpoint, token string, options ...Option) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("endpoint must be an absolute HTTP(S) URL without user information, a query or a fragment")
	}
	if token == "" {
		return nil, errors.New("a bearer token is required")
	}
	for _, ch := range token {
		if ch <= ' ' || ch >= 127 {
			return nil, errors.New("bearer token must contain only visible ASCII characters without whitespace")
		}
	}
	c := &Client{
		endpoint: u,
		token:    token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("invalid OnPager client option")
		}
		if err := option(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Do sends an API request, retaining any path prefix in the configured endpoint.
// Routes are relative to that endpoint and may include query parameters. HTTP
// statuses are returned unchanged for resource-specific handling. The caller must
// close the response body. The provider adds no retry loop or automatic redirects.
func (c *Client) Do(ctx context.Context, method, route string, body io.Reader) (*http.Response, error) {
	return c.do(ctx, method, route, body, nil)
}

func (c *Client) do(ctx context.Context, method, route string, body io.Reader, headers http.Header) (*http.Response, error) {
	relative, err := url.Parse(route)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.User != nil || relative.Fragment != "" {
		return nil, errors.New("API route must be relative and must not contain a fragment")
	}
	for _, segment := range strings.Split(relative.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("API route must not contain dot segments")
		}
	}
	u := c.endpoint.JoinPath(relative.EscapedPath())
	u.RawQuery = relative.RawQuery
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, errors.New("could not construct the OnPager API request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		req.Header[key] = values
	}
	if c.timezone != "" {
		req.Header.Set("Onpager-Timezone", c.timezone)
	}
	return c.http.Do(req)
}
