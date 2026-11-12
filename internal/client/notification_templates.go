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
	"unicode/utf8"
)

// NotificationTemplateDraft is the exact stored override, not an inherited view.
type NotificationTemplateDraft struct {
	Format string `json:"format"`
	Body   string `json:"body"`
}

// NotificationTemplateView distinguishes the scoped override from inheritance.
type NotificationTemplateView struct {
	Override  *NotificationTemplateDraft `json:"override"`
	Effective NotificationTemplateDraft  `json:"effective"`
	Source    string                     `json:"source"`
	ETag      string                     `json:"-"`
	// Committed records a successful HTTP mutation even if its response is malformed.
	Committed bool `json:"-"`
}

func notificationTemplatePath(team, kind, provider, language string) string {
	prefix := "/v1"
	if team != "" {
		prefix = teamPath(team)
	}
	return prefix + "/notification-templates/" + url.PathEscape(kind) + "/" + url.PathEscape(provider) + "/" + url.PathEscape(language)
}
func validNotificationTemplateDraft(v NotificationTemplateDraft, provider string) bool {
	formats := map[string][]string{"telegram": {"telegram_html", "telegram_markdown_v2", "plain"}, "mattermost": {"mattermost_markdown"}, "slack": {"slack_mrkdwn", "slack_plain"}, "discord": {"discord_markdown"}, "pachca": {"pachca_markdown"}, "zvonok": {"plain"}}
	if strings.TrimSpace(v.Body) == "" || len(v.Body) > 64<<10 || strings.ContainsRune(v.Body, 0) || !utf8.ValidString(v.Body) {
		return false
	}
	for _, f := range formats[provider] {
		if f == v.Format {
			return true
		}
	}
	return false
}

// notificationTemplateRequest captures the HTTP ETag, which is not in the body.
// Errors use the same bounded, secret-safe envelope as the shared JSON transport.
func (c *Client) notificationTemplateRequest(ctx context.Context, method, team, kind, provider, language, tag string, draft *NotificationTemplateDraft) (NotificationTemplateView, error) {
	var out NotificationTemplateView
	if method != http.MethodGet && !chainTagPattern.MatchString(tag) {
		return out, errors.New("a current strong SHA-256 notification-template ETag is required")
	}
	var body io.Reader
	if draft != nil {
		b, err := json.Marshal(draft)
		if err != nil {
			return out, errors.New("could not encode notification template")
		}
		body = bytes.NewReader(b)
	}
	headers := make(http.Header)
	if tag != "" {
		headers.Set("If-Match", tag)
	}
	res, err := c.do(ctx, method, notificationTemplatePath(team, kind, provider, language), body, headers)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && method != http.MethodGet
	out.ETag = res.Header.Get("ETag")
	if !chainTagPattern.MatchString(out.ETag) {
		out.ETag = ""
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
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
	var wire struct {
		Override  json.RawMessage            `json:"override"`
		Effective *NotificationTemplateDraft `json:"effective"`
		Source    string                     `json:"source"`
	}
	if err = json.Unmarshal(data, &wire); err != nil {
		return out, errors.New("OnPager returned an invalid notification-template response")
	}
	if len(wire.Override) == 0 || wire.Effective == nil || !chainTagPattern.MatchString(out.ETag) {
		return out, errors.New("OnPager returned an incomplete notification-template override or ETag")
	}
	if string(wire.Override) != "null" {
		if err = json.Unmarshal(wire.Override, &out.Override); err != nil || out.Override == nil {
			return out, errors.New("OnPager returned an invalid notification-template override")
		}
	}
	out.Effective = *wire.Effective
	out.Source = wire.Source
	expectedSource := "global"
	if team != "" {
		expectedSource = "team"
	}
	if !validNotificationTemplateDraft(out.Effective, provider) || out.Override != nil && (!validNotificationTemplateDraft(*out.Override, provider) || out.Source != expectedSource || *out.Override != out.Effective) || out.Override == nil && (out.Source != "builtin" && (out.Source != "global" || team == "")) {
		return out, errors.New("OnPager returned an invalid notification-template inheritance view")
	}
	return out, nil
}
func (c *Client) GetNotificationTemplate(ctx context.Context, team, kind, provider, language string) (NotificationTemplateView, error) {
	return c.notificationTemplateRequest(ctx, http.MethodGet, team, kind, provider, language, "", nil)
}
func (c *Client) PutNotificationTemplate(ctx context.Context, team, kind, provider, language, tag string, draft NotificationTemplateDraft) (NotificationTemplateView, error) {
	v, err := c.notificationTemplateRequest(ctx, http.MethodPut, team, kind, provider, language, tag, &draft)
	if err == nil && (v.Override == nil || *v.Override != draft) {
		err = errors.New("OnPager did not confirm the requested notification-template override")
	}
	return v, err
}
func (c *Client) DeleteNotificationTemplate(ctx context.Context, team, kind, provider, language, tag string) (NotificationTemplateView, error) {
	v, err := c.notificationTemplateRequest(ctx, http.MethodDelete, team, kind, provider, language, tag, nil)
	if err == nil && v.Override != nil {
		err = errors.New("OnPager did not confirm removal of the notification-template override")
	}
	return v, err
}
