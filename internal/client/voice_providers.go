package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type VoiceProviderConfiguration struct {
	CallCampaignID         string `json:"call_campaign_id"`
	VerificationCampaignID string `json:"verification_campaign_id"`
	MaxTextRunes           int64  `json:"max_text_runes"`
}

type VoiceProviderSecretChange struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

func (VoiceProviderSecretChange) String() string   { return "[REDACTED]" }
func (VoiceProviderSecretChange) GoString() string { return "[REDACTED]" }

// VoiceProviderInput contains complete public settings; omitted named secrets
// retain the server's stored credential. Kind belongs to the create body only.
type VoiceProviderInput struct {
	DisplayName   string                               `json:"display_name"`
	Enabled       bool                                 `json:"enabled"`
	Visibility    string                               `json:"visibility"`
	Configuration VoiceProviderConfiguration           `json:"configuration"`
	TeamIDs       []string                             `json:"team_ids"`
	Secrets       map[string]VoiceProviderSecretChange `json:"secrets,omitempty"`
}

func (VoiceProviderInput) String() string   { return "[REDACTED]" }
func (VoiceProviderInput) GoString() string { return "[REDACTED]" }

type VoiceProviderRuntime struct {
	State      string `json:"state"`
	ErrorCode  string `json:"error_code,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
}
type VoiceProvider struct {
	ID            string                     `json:"id"`
	Kind          string                     `json:"kind"`
	DisplayName   string                     `json:"display_name"`
	Enabled       bool                       `json:"enabled"`
	Visibility    string                     `json:"visibility"`
	Configuration VoiceProviderConfiguration `json:"configuration"`
	TeamIDs       []string                   `json:"team_ids"`
	SecretKeys    []string                   `json:"secret_keys"`
	CallbackURL   string                     `json:"callback_url,omitempty"`
	Runtime       VoiceProviderRuntime       `json:"runtime"`
	Committed     bool                       `json:"-"`
}

func (VoiceProvider) String() string   { return "[REDACTED]" }
func (VoiceProvider) GoString() string { return "[REDACTED]" }

func voiceProviderPath(id string) string {
	p := "/v1/voice-providers"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}

func voiceProviderText(v string, max int) bool {
	return v != "" && strings.TrimSpace(v) == v && len(v) <= max && utf8.ValidString(v) && !strings.ContainsFunc(v, unicode.IsControl)
}

func validateVoiceProvider(v VoiceProvider, raw []byte) error {
	invalid := func() error {
		return errors.New("OnPager returned an invalid or incomplete voice-provider configuration")
	}
	if !outgoingWebhookCompleteObject(raw, "id", "kind", "display_name", "enabled", "visibility", "configuration", "team_ids", "secret_keys", "runtime") {
		return invalid()
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if !outgoingWebhookCompleteObject(fields["configuration"], "call_campaign_id", "verification_campaign_id", "max_text_runes") || !outgoingWebhookCompleteObject(fields["runtime"], "state") {
		return invalid()
	}
	if len(v.SecretKeys) != 1 || v.SecretKeys[0] != "public_key" {
		return invalid()
	}
	if v.Kind != "zvonok" || !voiceProviderText(v.DisplayName, 200) || (v.Visibility != "global" && v.Visibility != "team") || len(v.TeamIDs) > 1000 || v.Visibility == "global" && len(v.TeamIDs) > 0 || !voiceProviderText(v.Configuration.CallCampaignID, 128) || !voiceProviderText(v.Configuration.VerificationCampaignID, 128) || v.Configuration.MaxTextRunes < 1 || v.Configuration.MaxTextRunes > 500 {
		return invalid()
	}
	seen := map[string]bool{}
	for _, id := range v.TeamIDs {
		if !validResponseID(id) || seen[id] {
			return invalid()
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, key := range v.SecretKeys {
		if key != "public_key" || seen[key] {
			return invalid()
		}
		seen[key] = true
	}
	switch v.Runtime.State {
	case "disabled", "starting", "reconfiguring", "running", "error":
	default:
		return invalid()
	}
	if v.Runtime.ErrorCode != "" && !safeCode.MatchString(v.Runtime.ErrorCode) {
		return invalid()
	}
	if v.Runtime.ObservedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, v.Runtime.ObservedAt); err != nil {
			return invalid()
		}
	}
	if v.CallbackURL != "" {
		u, err := url.Parse(v.CallbackURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
			return invalid()
		}
	}
	return nil
}

// voiceProviderRequest records a confirmed HTTP commit independently of reading
// its response, so a malformed success receipt cannot erase committed state.
func (c *Client) voiceProviderRequest(ctx context.Context, method, id, kind string, in any) (VoiceProvider, error) {
	var out VoiceProvider
	if id != "" && !validResponseID(id) {
		return out, errors.New("a nonzero canonical voice-provider UUID is required")
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return out, errors.New("could not encode voice-provider configuration")
		}
		body = bytes.NewReader(raw)
	}
	res, err := c.do(ctx, method, voiceProviderPath(id), body, nil)
	if err != nil {
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
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
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
		if res.StatusCode != http.StatusNoContent || len(bytes.TrimSpace(raw)) != 0 {
			return out, errors.New("OnPager did not confirm voice-provider mutation with an empty HTTP 204 response")
		}
		return out, nil
	}
	committed := out.Committed
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out, errors.New("OnPager returned an invalid voice-provider response")
	}
	_ = json.Unmarshal(fields["id"], &out.ID)
	if !validResponseID(out.ID) || id != "" && out.ID != id {
		return VoiceProvider{Committed: committed}, errors.New("OnPager returned an unexpected voice-provider identity")
	}
	var returnedKind string
	_ = json.Unmarshal(fields["kind"], &returnedKind)
	if returnedKind != "" && (returnedKind != "zvonok" || kind != "" && returnedKind != kind) {
		return VoiceProvider{Committed: committed}, errors.New("OnPager returned an unexpected voice-provider identity")
	}
	if method == http.MethodPost && res.StatusCode != 201 || method == http.MethodGet && res.StatusCode != 200 {
		return out, errors.New("OnPager returned an unexpected voice-provider response status")
	}
	var full VoiceProvider
	if err = json.Unmarshal(raw, &full); err != nil {
		return out, errors.New("OnPager returned an invalid voice-provider response")
	}
	full.Committed = committed
	if err = validateVoiceProvider(full, raw); err != nil {
		return out, err
	}
	return full, nil
}

func (c *Client) CreateVoiceProvider(ctx context.Context, kind string, in VoiceProviderInput) (VoiceProvider, error) {
	if kind != "zvonok" {
		return VoiceProvider{}, errors.New("unsupported voice-provider kind")
	}
	if in.TeamIDs == nil {
		in.TeamIDs = []string{}
	}
	return c.voiceProviderRequest(ctx, http.MethodPost, "", kind, struct {
		VoiceProviderInput
		Kind string `json:"kind"`
	}{in, kind})
}
func (c *Client) GetVoiceProvider(ctx context.Context, id string) (VoiceProvider, error) {
	if !validResponseID(id) {
		return VoiceProvider{}, errors.New("a nonzero canonical voice-provider UUID is required")
	}
	return c.voiceProviderRequest(ctx, http.MethodGet, id, "", nil)
}
func (c *Client) UpdateVoiceProvider(ctx context.Context, id string, in VoiceProviderInput) (VoiceProvider, error) {
	if !validResponseID(id) {
		return VoiceProvider{}, errors.New("a nonzero canonical voice-provider UUID is required")
	}
	if in.TeamIDs == nil {
		in.TeamIDs = []string{}
	}
	return c.voiceProviderRequest(ctx, http.MethodPut, id, "", in)
}
func (c *Client) DeleteVoiceProvider(ctx context.Context, id string) error {
	if !validResponseID(id) {
		return errors.New("a nonzero canonical voice-provider UUID is required")
	}
	_, err := c.voiceProviderRequest(ctx, http.MethodDelete, id, "", nil)
	return allowAbsent(err)
}
