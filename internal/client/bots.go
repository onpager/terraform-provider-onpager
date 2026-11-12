package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
)

type BotConfiguration struct {
	Mode           string   `json:"mode,omitempty"`
	BaseURL        string   `json:"base_url,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}
type BotPacing struct {
	Requests       int64 `json:"requests"`
	WindowMS       int64 `json:"window_ms"`
	SearchRequests int64 `json:"search_requests"`
}
type BotSettings struct {
	DisplayName   string           `json:"display_name"`
	PublicHandle  string           `json:"public_handle"`
	EnrollmentURL string           `json:"enrollment_url"`
	Enabled       bool             `json:"enabled"`
	Visibility    string           `json:"visibility"`
	Configuration BotConfiguration `json:"configuration"`
	Pacing        BotPacing        `json:"pacing"`
	TeamIDs       []string         `json:"team_ids"`
}
type BotSecretChange struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

func (BotSecretChange) String() string   { return "[REDACTED]" }
func (BotSecretChange) GoString() string { return "[REDACTED]" }

type BotInput struct {
	BotSettings
	Secrets map[string]BotSecretChange `json:"secrets,omitempty"`
}

func (BotInput) String() string   { return "[REDACTED]" }
func (BotInput) GoString() string { return "[REDACTED]" }

type BotCreateInput struct {
	BotInput
	Messenger         string `json:"messenger"`
	AddressingScopeID string `json:"addressing_scope_id,omitempty"`
}

func (BotCreateInput) String() string   { return "[REDACTED]" }
func (BotCreateInput) GoString() string { return "[REDACTED]" }

type BotScope struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type BotRuntime struct {
	State       string `json:"state"`
	ErrorCode   string `json:"error_code,omitempty"`
	LastProbeAt string `json:"last_probe_at,omitempty"`
	ObservedAt  string `json:"observed_at,omitempty"`
}
type Bot struct {
	BotSettings
	ID              string     `json:"id"`
	Messenger       string     `json:"messenger"`
	AddressingScope BotScope   `json:"addressing_scope"`
	SecretKeys      []string   `json:"secret_keys"`
	CallbackURL     string     `json:"callback_url,omitempty"`
	Runtime         BotRuntime `json:"runtime"`
	// Committed distinguishes successful mutations from HTTP failures even when
	// response validation fails. PUT204 retains the exact submitted intent.
	Committed bool `json:"-"`
}

func botPath(id string) string {
	if id == "" {
		return "/v1/bots"
	}
	return "/v1/bots/" + url.PathEscape(id)
}
func (c *Client) botRequest(ctx context.Context, method, id, messenger string, input *BotInput, create *BotCreateInput) (Bot, error) {
	var out Bot
	if method != http.MethodPost && !validResponseID(id) {
		return out, errors.New("a canonical nonzero bot UUID is required")
	}
	var payload any
	if create != nil {
		if e := ValidateBotCreateInput(*create); e != nil {
			return out, e
		}
		payload = create
	}
	if input != nil {
		if e := ValidateBotInput(messenger, *input); e != nil {
			return out, e
		}
		payload = input
	}
	var body io.Reader
	if payload != nil {
		raw, e := json.Marshal(payload)
		if e != nil {
			return out, errors.New("could not encode bot configuration")
		}
		body = bytes.NewReader(raw)
	}
	res, e := c.do(ctx, method, botPath(id), body, nil)
	if e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && method != http.MethodGet
	if out.Committed {
		out.ID = id
		if input != nil {
			out.BotSettings = input.BotSettings
			out.Messenger = messenger
		}
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil {
		return out, errors.New("could not read OnPager response")
	}
	if len(raw) > 4<<20 {
		return out, errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if !success {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		code := envelope.Error.Code
		if !safeCode.MatchString(code) {
			code = ""
		}
		return out, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	if method == http.MethodPut || method == http.MethodDelete {
		if res.StatusCode != 204 || len(bytes.TrimSpace(raw)) != 0 {
			return out, errors.New("OnPager did not confirm bot mutation with an empty HTTP 204 response")
		}
		return out, nil
	}
	decoded, decodeError := decodeBot(raw, id, out.Committed)
	if method == http.MethodPost && res.StatusCode != 201 || method == http.MethodGet && res.StatusCode != 200 {
		return decoded, errors.New("OnPager returned an unexpected bot response status")
	}
	return decoded, decodeError
}
func (c *Client) CreateBot(ctx context.Context, in BotCreateInput) (Bot, error) {
	return c.botRequest(ctx, http.MethodPost, "", "", nil, &in)
}
func (c *Client) GetBot(ctx context.Context, id string) (Bot, error) {
	return c.botRequest(ctx, http.MethodGet, id, "", nil, nil)
}
func (c *Client) UpdateBot(ctx context.Context, id, messenger string, in BotInput) (Bot, error) {
	return c.botRequest(ctx, http.MethodPut, id, messenger, &in, nil)
}
func (c *Client) DeleteBot(ctx context.Context, id string) error {
	_, e := c.botRequest(ctx, http.MethodDelete, id, "", nil, nil)
	return allowAbsent(e)
}

func decodeBot(raw []byte, expected string, committed bool) (Bot, error) {
	out := Bot{Committed: committed}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil || fields == nil {
		return out, errors.New("OnPager returned invalid bot JSON")
	}
	_ = json.Unmarshal(fields["id"], &out.ID)
	if !validResponseID(out.ID) || expected != "" && out.ID != expected {
		out.ID = ""
		return out, errors.New("OnPager returned an unexpected bot identity")
	}
	fail := func() (Bot, error) {
		return out, errors.New("OnPager returned incomplete or invalid bot configuration")
	}
	for _, key := range []string{"messenger", "display_name", "public_handle", "enrollment_url", "enabled", "visibility", "addressing_scope", "configuration", "pacing", "team_ids", "secret_keys", "runtime"} {
		if len(fields[key]) == 0 || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return fail()
		}
	}
	var full Bot
	if e := json.Unmarshal(raw, &full); e != nil {
		return fail()
	}
	for _, pair := range []struct {
		key      string
		required []string
	}{{"addressing_scope", []string{"kind", "id"}}, {"pacing", []string{"requests", "window_ms", "search_requests"}}, {"runtime", []string{"state"}}} {
		var nested map[string]json.RawMessage
		if json.Unmarshal(fields[pair.key], &nested) != nil {
			return fail()
		}
		for _, key := range pair.required {
			if len(nested[key]) == 0 || bytes.Equal(bytes.TrimSpace(nested[key]), []byte("null")) {
				return fail()
			}
		}
	}
	if (full.Messenger == "telegram" || full.Messenger == "pachca") && full.Configuration.Mode == "" || full.Messenger == "telegram" && len(full.Configuration.AllowedUpdates) == 0 {
		return fail()
	}
	if ValidateBotInput(full.Messenger, BotInput{BotSettings: full.BotSettings}) != nil || !validBotScope(full.Messenger, full.AddressingScope.ID) || full.AddressingScope.Kind != botScopeKind(full.Messenger) || validateBotSecretKeys(full.Messenger, full.Configuration.Mode, full.SecretKeys) != nil {
		return fail()
	}
	switch full.Runtime.State {
	case "starting", "running", "reconfiguring", "disabled", "error":
	default:
		return fail()
	}
	full.Committed = committed
	return full, nil
}
