package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type OutgoingWebhookPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type OutgoingWebhookAuth struct {
	Kind           string `json:"kind"`
	Username       string `json:"username,omitempty"`
	PasswordSecret string `json:"password_secret,omitempty"`
	TokenSecret    string `json:"token_secret,omitempty"`
}
type OutgoingWebhookRequest struct {
	VerifySSL      *bool                 `json:"verify_ssl,omitempty"`
	TimeoutSeconds int64                 `json:"timeout_seconds,omitempty"`
	Method         string                `json:"method"`
	Endpoint       string                `json:"endpoint"`
	BodyFormat     string                `json:"body_format"`
	BodyTemplate   string                `json:"body_template"`
	ContentType    string                `json:"content_type,omitempty"`
	Query          []OutgoingWebhookPair `json:"query,omitempty"`
	Headers        []OutgoingWebhookPair `json:"headers,omitempty"`
	Form           []OutgoingWebhookPair `json:"form,omitempty"`
	Auth           OutgoingWebhookAuth   `json:"auth"`
}
type OutgoingWebhookRecipientField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
}
type OutgoingWebhookSecretChange struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

func (OutgoingWebhookSecretChange) String() string   { return "[REDACTED]" }
func (OutgoingWebhookSecretChange) GoString() string { return "[REDACTED]" }

type OutgoingWebhookSecret struct {
	Key      string `json:"key"`
	HasValue bool   `json:"has_value"`
}
type OutgoingWebhookInput struct {
	Purpose         string                                 `json:"purpose"`
	DisplayName     string                                 `json:"display_name"`
	Enabled         bool                                   `json:"enabled"`
	Request         OutgoingWebhookRequest                 `json:"request"`
	RecipientFields []OutgoingWebhookRecipientField        `json:"recipient_fields"`
	Secrets         map[string]OutgoingWebhookSecretChange `json:"secrets,omitempty"`
}

func (OutgoingWebhookInput) String() string   { return "[REDACTED]" }
func (OutgoingWebhookInput) GoString() string { return "[REDACTED]" }

type OutgoingWebhook struct {
	ID    string `json:"id"`
	Scope struct {
		TeamID *string `json:"team_id"`
	} `json:"scope"`
	Purpose         string                          `json:"purpose"`
	DisplayName     string                          `json:"display_name"`
	Enabled         bool                            `json:"enabled"`
	ETag            string                          `json:"etag"`
	Request         OutgoingWebhookRequest          `json:"request"`
	RecipientFields []OutgoingWebhookRecipientField `json:"recipient_fields"`
	Secrets         []OutgoingWebhookSecret         `json:"secrets"`
	RuntimeStatus   string                          `json:"runtime_status"`
	Committed       bool                            `json:"-"`
}

func ValidOutgoingWebhookETag(tag string) bool {
	n, e := strconv.ParseInt(strings.Trim(tag, `"`), 10, 64)
	return e == nil && n > 0 && tag == `"`+strconv.FormatInt(n, 10)+`"`
}
func outgoingWebhookPath(team, id string) string {
	p := "/v1"
	if team != "" {
		p = teamPath(team)
	}
	p += "/outgoing-webhooks"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}

func outgoingWebhookCompleteObject(raw json.RawMessage, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, k := range keys {
		if len(fields[k]) == 0 || string(fields[k]) == "null" {
			return false
		}
	}
	return true
}

func outgoingWebhookCompleteArray(raw json.RawMessage, keys ...string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		return false
	}
	for _, row := range rows {
		if !outgoingWebhookCompleteObject(row, keys...) {
			return false
		}
	}
	return true
}

// EqualOutgoingWebhookRequests accounts for the server's documented defaults
// and extraction of endpoint query parameters without rendering any template.
func EqualOutgoingWebhookRequests(a, b OutgoingWebhookRequest) bool {
	normalize := func(c OutgoingWebhookRequest) OutgoingWebhookRequest {
		if c.Method == "" {
			c.Method = "POST"
		}
		if c.BodyFormat == "" {
			c.BodyFormat = "json"
		}
		if c.TimeoutSeconds == 0 {
			c.TimeoutSeconds = 5
		}
		if c.VerifySSL == nil {
			v := true
			c.VerifySSL = &v
		}
		if c.Auth.Kind == "" {
			c.Auth.Kind = "none"
		}
		if u, e := url.Parse(c.Endpoint); e == nil {
			q, e := url.ParseQuery(u.RawQuery)
			if e == nil {
				c.Query = append([]OutgoingWebhookPair(nil), c.Query...)
				keys := make([]string, 0, len(q))
				for k := range q {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					for _, v := range q[k] {
						c.Query = append(c.Query, OutgoingWebhookPair{k, v})
					}
				}
				c.Endpoint = strings.SplitN(c.Endpoint, "?", 2)[0]
			}
		}
		if len(c.Query) == 0 {
			c.Query = nil
		}
		if len(c.Headers) == 0 {
			c.Headers = nil
		}
		if len(c.Form) == 0 {
			c.Form = nil
		}
		return c
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func (c *Client) outgoingWebhookRequest(ctx context.Context, method, team, id, tag string, input any) (OutgoingWebhook, error) {
	var out OutgoingWebhook
	if method == http.MethodPut || method == http.MethodDelete {
		if !ValidOutgoingWebhookETag(tag) {
			return out, errors.New("a current strong numeric outgoing-webhook ETag is required")
		}
	}
	var body io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return out, errors.New("could not encode outgoing webhook")
		}
		body = bytes.NewReader(b)
	}
	h := make(http.Header)
	if tag != "" {
		h.Set("If-Match", tag)
	}
	res, e := c.do(ctx, method, outgoingWebhookPath(team, id), body, h)
	if e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && method != http.MethodGet
	data, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil {
		return out, errors.New("could not read OnPager response")
	}
	if len(data) > 4<<20 {
		return out, errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if !success {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		code := envelope.Error.Code
		if !safeCode.MatchString(code) {
			code = ""
		}
		return out, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	committed := out.Committed
	if e = json.Unmarshal(data, &out); e != nil {
		return OutgoingWebhook{Committed: committed}, errors.New("OnPager returned an invalid outgoing-webhook response")
	}
	out.Committed = committed
	// Do not expose a foreign ID even to partial-state recovery.
	if !validResponseID(out.ID) || id != "" && out.ID != id {
		out.ID = ""
		out.ETag = ""
		return out, errors.New("OnPager returned an unexpected outgoing-webhook identity")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	// Validate any returned scope before exposing even a partial create identity.
	if len(fields["scope"]) > 0 && string(fields["scope"]) != "null" {
		var scope map[string]json.RawMessage
		_ = json.Unmarshal(fields["scope"], &scope)
		if len(scope["team_id"]) == 0 || team == "" && out.Scope.TeamID != nil || team != "" && (out.Scope.TeamID == nil || *out.Scope.TeamID != team) {
			out.ID = ""
			out.ETag = ""
			return out, errors.New("OnPager returned an unexpected outgoing-webhook scope")
		}
	}
	if !ValidOutgoingWebhookETag(out.ETag) || out.ETag != res.Header.Get("ETag") {
		out.ETag = ""
		return out, errors.New("OnPager returned an invalid outgoing-webhook ETag")
	}
	for _, k := range []string{"scope", "purpose", "display_name", "enabled", "etag", "request", "recipient_fields", "secrets", "runtime_status"} {
		if len(fields[k]) == 0 || string(fields[k]) == "null" {
			return out, errors.New("OnPager returned an incomplete outgoing-webhook response")
		}
	}
	var scope map[string]json.RawMessage
	_ = json.Unmarshal(fields["scope"], &scope)
	if len(scope["team_id"]) == 0 || team == "" && out.Scope.TeamID != nil || team != "" && (out.Scope.TeamID == nil || *out.Scope.TeamID != team) {
		out.ID = ""
		return out, errors.New("OnPager returned an unexpected outgoing-webhook scope")
	}
	var request map[string]json.RawMessage
	_ = json.Unmarshal(fields["request"], &request)
	for _, k := range []string{"method", "endpoint", "body_format", "body_template", "auth", "verify_ssl", "timeout_seconds"} {
		if len(request[k]) == 0 || string(request[k]) == "null" {
			return out, errors.New("OnPager returned incomplete outgoing-webhook request settings")
		}
	}
	if !outgoingWebhookCompleteObject(request["auth"], "kind") || !outgoingWebhookCompleteArray(fields["secrets"], "key", "has_value") || !outgoingWebhookCompleteArray(fields["recipient_fields"], "key", "label", "kind", "required") {
		return out, errors.New("OnPager returned incomplete outgoing-webhook metadata")
	}
	for _, key := range []string{"query", "headers", "form"} {
		if !outgoingWebhookCompleteArray(request[key], "key", "value") {
			return out, errors.New("OnPager returned incomplete outgoing-webhook request pairs")
		}
	}
	if (out.Purpose != "notification" && out.Purpose != "escalation") || strings.TrimSpace(out.DisplayName) == "" || out.Request.Endpoint == "" || out.Request.VerifySSL == nil || out.Request.TimeoutSeconds < 1 || out.Request.TimeoutSeconds > 60 {
		return out, errors.New("OnPager returned invalid outgoing-webhook settings")
	}
	if method == http.MethodDelete && out.Enabled {
		return out, errors.New("OnPager did not confirm outgoing-webhook disable")
	}
	return out, nil
}
func (c *Client) CreateOutgoingWebhook(ctx context.Context, team, requestID string, in OutgoingWebhookInput) (OutgoingWebhook, error) {
	if !validResponseID(requestID) {
		return OutgoingWebhook{}, errors.New("a nonzero canonical outgoing-webhook request UUID is required")
	}
	if in.RecipientFields == nil {
		in.RecipientFields = []OutgoingWebhookRecipientField{}
	}
	return c.outgoingWebhookRequest(ctx, http.MethodPost, team, "", "", struct {
		OutgoingWebhookInput
		RequestID string `json:"request_id"`
	}{in, requestID})
}
func (c *Client) GetOutgoingWebhook(ctx context.Context, team, id string) (OutgoingWebhook, error) {
	return c.outgoingWebhookRequest(ctx, http.MethodGet, team, id, "", nil)
}
func (c *Client) UpdateOutgoingWebhook(ctx context.Context, team, id, tag string, in OutgoingWebhookInput) (OutgoingWebhook, error) {
	if in.RecipientFields == nil {
		in.RecipientFields = []OutgoingWebhookRecipientField{}
	}
	return c.outgoingWebhookRequest(ctx, http.MethodPut, team, id, tag, in)
}
func (c *Client) DisableOutgoingWebhook(ctx context.Context, team, id, tag string) (OutgoingWebhook, error) {
	return c.outgoingWebhookRequest(ctx, http.MethodDelete, team, id, tag, nil)
}
