package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// InhibitionMatcher contains literal label keys, including whitespace, dots and slashes.
type InhibitionMatcher struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// InhibitionInput replaces the whole rule. Enabled is sent even when false.
type InhibitionInput struct {
	Name                 string              `json:"name"`
	SourceExpression     string              `json:"source_expression"`
	TargetExpression     string              `json:"target_expression"`
	Matchers             []InhibitionMatcher `json:"matchers"`
	ResolvedTargetAction string              `json:"resolved_target_action"`
	Enabled              bool                `json:"enabled"`
}

type InhibitionRule struct {
	InhibitionInput
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	IndexState string `json:"index_state"`
	// Committed remains true after successful HTTP mutations with malformed content.
	Committed bool `json:"-"`
}

type InhibitionReceipt struct {
	ID        string `json:"id"`
	Revision  int64  `json:"revision"`
	Committed bool   `json:"-"`
}

// ValidateInhibitionInput enforces byte limits and canonical server-trimmed text.
// CEL type and cost validation remains the API's responsibility.
func ValidateInhibitionInput(in InhibitionInput) error {
	text := func(s string, max int, trim bool) bool {
		return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0) && (!trim || strings.TrimSpace(s) == s)
	}
	if !text(in.Name, 128, true) || !text(in.SourceExpression, 4096, true) || !text(in.TargetExpression, 4096, true) || (in.ResolvedTargetAction != "release" && in.ResolvedTargetAction != "keep") || len(in.Matchers) < 1 || len(in.Matchers) > 16 {
		return errors.New("invalid inhibition name, expression, action or matcher count")
	}
	seen := map[InhibitionMatcher]bool{}
	for _, pair := range in.Matchers {
		if !text(pair.Source, 256, false) || !text(pair.Target, 256, false) || seen[pair] {
			return errors.New("inhibition matcher pairs must be distinct literal nonempty UTF-8 keys of at most 256 bytes without NUL")
		}
		seen[pair] = true
	}
	return nil
}

func inhibitionPath(id string) string {
	p := "/v1/inhibition-rules"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}

// inhibitionRequest distinguishes a committed mutation from malformed response
// content so callers can save known identity and intent before returning errors.
func (c *Client) inhibitionRequest(ctx context.Context, method, id string, revision int64, input *InhibitionInput) (InhibitionRule, error) {
	var out InhibitionRule
	if method != http.MethodPost && !validResponseID(id) {
		return out, errors.New("a canonical nonzero inhibition UUID is required")
	}
	if (method == http.MethodPut || method == http.MethodDelete) && revision < 1 {
		return out, errors.New("a positive saved inhibition revision is required")
	}
	var body io.Reader
	if input != nil {
		if err := ValidateInhibitionInput(*input); err != nil {
			return out, err
		}
		data, err := json.Marshal(input)
		if err != nil {
			return out, errors.New("could not encode inhibition configuration")
		}
		body = bytes.NewReader(data)
	}
	h := make(http.Header)
	if revision > 0 {
		h.Set("If-Match", `"`+strconv.FormatInt(revision, 10)+`"`)
	}
	res, err := c.do(ctx, method, inhibitionPath(id), body, h)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && method != http.MethodGet
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
	if method == http.MethodDelete {
		if res.StatusCode != http.StatusNoContent || len(bytes.TrimSpace(raw)) != 0 {
			return out, errors.New("OnPager did not confirm inhibition deletion with an empty HTTP 204 response")
		}
		return out, nil
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return out, errors.New("OnPager returned invalid inhibition JSON")
	}
	// Validate identity separately from content so a malformed settings field
	// does not erase a usable committed UUID and revision.
	_ = json.Unmarshal(fields["id"], &out.ID)
	_ = json.Unmarshal(fields["revision"], &out.Revision)
	if !validResponseID(out.ID) || id != "" && out.ID != id {
		out.ID = ""
		out.Revision = 0
		return out, errors.New("OnPager returned an unexpected inhibition identity")
	}
	if out.Revision < 1 || method == http.MethodPut && out.Revision < revision {
		out.Revision = 0
		return out, errors.New("OnPager returned an invalid inhibition revision")
	}
	if method == http.MethodPut {
		return out, nil
	}
	var full InhibitionRule
	if err = json.Unmarshal(raw, &full); err != nil {
		return out, errors.New("OnPager returned invalid inhibition configuration")
	}
	full.Committed = out.Committed
	out = full
	for _, key := range []string{"name", "source_expression", "target_expression", "matchers", "resolved_target_action", "enabled", "index_state"} {
		if len(fields[key]) == 0 || string(bytes.TrimSpace(fields[key])) == "null" {
			return out, errors.New("OnPager returned incomplete inhibition configuration")
		}
	}
	if err = ValidateInhibitionInput(out.InhibitionInput); err != nil {
		return out, errors.New("OnPager returned invalid inhibition configuration")
	}
	if out.IndexState != "pending" && out.IndexState != "ready" && out.IndexState != "obsolete" {
		return out, errors.New("OnPager returned invalid inhibition index state")
	}
	return out, nil
}

func (c *Client) CreateInhibitionRule(ctx context.Context, input InhibitionInput) (InhibitionRule, error) {
	return c.inhibitionRequest(ctx, http.MethodPost, "", 0, &input)
}
func (c *Client) GetInhibitionRule(ctx context.Context, id string) (InhibitionRule, error) {
	return c.inhibitionRequest(ctx, http.MethodGet, id, 0, nil)
}
func (c *Client) UpdateInhibitionRule(ctx context.Context, id string, revision int64, input InhibitionInput) (InhibitionReceipt, error) {
	v, err := c.inhibitionRequest(ctx, http.MethodPut, id, revision, &input)
	return InhibitionReceipt{ID: v.ID, Revision: v.Revision, Committed: v.Committed}, err
}
func (c *Client) DeleteInhibitionRule(ctx context.Context, id string, revision int64) error {
	_, err := c.inhibitionRequest(ctx, http.MethodDelete, id, revision, nil)
	return allowAbsent(err)
}
