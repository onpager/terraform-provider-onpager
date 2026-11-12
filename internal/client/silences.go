package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// SilenceInput owns the whole immutable scope and the complete team UUID set.
// Timestamp strings are sent unchanged for the backend's request-zone parser.
type SilenceInput struct {
	Scope      string   `json:"scope"`
	TeamIDs    []string `json:"team_ids"`
	Expression string   `json:"expression"`
	StartsAt   string   `json:"starts_at"`
	EndsAt     string   `json:"ends_at"`
	Comment    string   `json:"comment"`
}
type Silence struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	SilenceInput
	Status      string  `json:"status"`
	CancelledAt *string `json:"cancelled_at"`
	Committed   bool    `json:"-"`
}
type SilenceReceipt struct {
	ID        string `json:"id"`
	Revision  int64  `json:"revision"`
	Committed bool   `json:"-"`
}

func silencePath(id string) string {
	p := "/v1/silences"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}

// silenceRequest preserves the fact that a mutation committed even if decoding
// its receipt fails. It never retries a mutation or reveals response bodies.
func (c *Client) silenceRequest(ctx context.Context, method, id string, revision int64, input any) (data []byte, tag string, committed bool, err error) {
	if (method == http.MethodPatch || method == http.MethodDelete) && revision <= 0 {
		return nil, "", false, errors.New("a current positive silence revision is required")
	}
	var body io.Reader
	if input != nil {
		var b []byte
		b, err = json.Marshal(input)
		if err != nil {
			return nil, "", false, errors.New("could not encode OnPager silence")
		}
		body = bytes.NewReader(b)
	}
	h := make(http.Header)
	if revision > 0 {
		h.Set("If-Match", fmt.Sprintf(`"%d"`, revision))
	}
	res, e := c.do(ctx, method, silencePath(id), body, h)
	if e != nil {
		if ctx.Err() != nil {
			return nil, "", false, ctx.Err()
		}
		return nil, "", false, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	committed = res.StatusCode >= 200 && res.StatusCode < 300 && method != http.MethodGet
	tag = res.Header.Get("ETag")
	data, err = io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		return nil, tag, committed, errors.New("could not read OnPager response")
	}
	if len(data) > 4<<20 {
		return nil, tag, committed, errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
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
		return nil, tag, false, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	if method == http.MethodDelete && (res.StatusCode != http.StatusNoContent || len(bytes.TrimSpace(data)) > 0) {
		return nil, tag, committed, errors.New("OnPager did not confirm silence deletion with an empty HTTP 204 response")
	}
	return data, tag, committed, nil
}
func silenceFields(data []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return false
	}
	for _, k := range keys {
		v, ok := fields[k]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
	}
	return true
}
func silenceValidate(out Silence, id, tag string, data []byte) error {
	if !silenceFields(data, "id", "revision", "scope", "team_ids", "expression", "starts_at", "ends_at", "comment", "status") {
		return errors.New("OnPager returned an incomplete silence")
	}
	if !validResponseID(out.ID) || id != "" && out.ID != id {
		return errors.New("OnPager returned an unexpected silence identity")
	}
	if out.Revision <= 0 || tag != fmt.Sprintf(`"%d"`, out.Revision) {
		return errors.New("OnPager returned an invalid silence revision or ETag")
	}
	if out.Scope != "team" && out.Scope != "tenant" || out.Scope == "team" && len(out.TeamIDs) == 0 || out.Scope == "tenant" && len(out.TeamIDs) != 0 {
		return errors.New("OnPager returned an invalid silence scope")
	}
	seen := map[string]bool{}
	for _, team := range out.TeamIDs {
		if !validResponseID(team) || seen[team] {
			return errors.New("OnPager returned invalid silence team UUIDs")
		}
		seen[team] = true
	}
	start, e := time.Parse(time.RFC3339Nano, out.StartsAt)
	end, ee := time.Parse(time.RFC3339Nano, out.EndsAt)
	if e != nil || ee != nil || !end.After(start) || out.Expression == "" || !slices.Contains([]string{"active", "scheduled", "expired", "cancelled"}, out.Status) {
		return errors.New("OnPager returned invalid silence configuration")
	}
	return nil
}
func (c *Client) CreateSilence(ctx context.Context, in SilenceInput) (Silence, error) {
	data, tag, committed, e := c.silenceRequest(ctx, http.MethodPost, "", 0, in)
	out := Silence{Committed: committed}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(data, &out)
	out.Committed = committed
	if !validResponseID(out.ID) {
		out.ID = ""
	}
	// Preserve only a revision corroborated by the strong response ETag when
	// recovering the committed identity from a malformed successful response.
	if out.Revision <= 0 || tag != fmt.Sprintf(`"%d"`, out.Revision) {
		out.Revision = 0
	}
	if e != nil {
		return out, errors.New("OnPager returned an invalid silence response")
	}
	return out, silenceValidate(out, "", tag, data)
}
func (c *Client) GetSilence(ctx context.Context, id string) (Silence, error) {
	data, tag, _, e := c.silenceRequest(ctx, http.MethodGet, id, 0, nil)
	if e != nil {
		return Silence{}, e
	}
	var out Silence
	if json.Unmarshal(data, &out) != nil {
		return out, errors.New("OnPager returned an invalid silence response")
	}
	e = silenceValidate(out, id, tag, data)
	if out.ID != id {
		out.ID = ""
	}
	return out, e
}
func (c *Client) UpdateSilence(ctx context.Context, id string, revision int64, in SilenceInput) (SilenceReceipt, error) {
	// Scope cannot be edited. Include every mutable field, including an empty
	// comment and empty tenant team set, rather than omitting their zero values.
	patch := struct {
		TeamIDs    []string `json:"team_ids"`
		Expression string   `json:"expression"`
		StartsAt   string   `json:"starts_at"`
		EndsAt     string   `json:"ends_at"`
		Comment    string   `json:"comment"`
	}{in.TeamIDs, in.Expression, in.StartsAt, in.EndsAt, in.Comment}
	data, tag, committed, e := c.silenceRequest(ctx, http.MethodPatch, id, revision, patch)
	out := SilenceReceipt{Committed: committed}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(data, &out)
	out.Committed = committed
	if !validResponseID(out.ID) || out.ID != id {
		out.ID = ""
		out.Revision = 0
		return out, errors.New("OnPager returned an unexpected silence identity")
	}
	if e != nil || !silenceFields(data, "id", "revision") || out.Revision <= 0 || tag != fmt.Sprintf(`"%d"`, out.Revision) {
		out.Revision = 0
		return out, errors.New("OnPager returned an invalid silence mutation receipt")
	}
	return out, nil
}
func (c *Client) DeleteSilence(ctx context.Context, id string, revision int64) error {
	_, _, _, e := c.silenceRequest(ctx, http.MethodDelete, id, revision, nil)
	return allowAbsent(e)
}
