package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
)

// ScheduleHandover owns a schedule's independent handover policy revision.
// Revision zero on GET means no policy has ever been stored.
type ScheduleHandover struct {
	Enabled          bool     `json:"enabled"`
	OncallChannelIDs []string `json:"oncall_channel_ids"`
	Revision         int64    `json:"revision"`
	Committed        bool     `json:"-"`
}

func handoverPath(team, schedule string) string {
	return teamPath(team) + "/schedules/" + url.PathEscape(schedule) + "/handover"
}

func validHandover(v ScheduleHandover) bool {
	if v.Revision < 0 || len(v.OncallChannelIDs) > 100 || v.Enabled && len(v.OncallChannelIDs) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range v.OncallChannelIDs {
		if !validResponseID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (c *Client) handoverRequest(ctx context.Context, method, team, schedule string, input *ScheduleHandover) (ScheduleHandover, error) {
	var out ScheduleHandover
	var body io.Reader
	if input != nil {
		if !validHandover(*input) {
			return out, errors.New("invalid handover settings or expected revision")
		}
		payload := *input
		if payload.OncallChannelIDs == nil {
			payload.OncallChannelIDs = []string{}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return out, errors.New("could not encode handover settings")
		}
		body = bytes.NewReader(raw)
	}
	res, err := c.do(ctx, method, handoverPath(team, schedule), body, nil)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && method == http.MethodPut
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
	var wire struct {
		Enabled          *bool     `json:"enabled"`
		OncallChannelIDs *[]string `json:"oncall_channel_ids"`
		Revision         *int64    `json:"revision"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return out, errors.New("OnPager returned invalid handover settings")
	}
	if wire.Revision != nil && *wire.Revision >= 0 {
		out.Revision = *wire.Revision
	}
	if wire.Enabled == nil || wire.OncallChannelIDs == nil || wire.Revision == nil {
		return out, errors.New("OnPager returned incomplete handover settings")
	}
	out.Enabled, out.OncallChannelIDs = *wire.Enabled, *wire.OncallChannelIDs
	if *wire.Revision < 0 || !validHandover(out) || method == http.MethodGet && out.Revision == 0 && (out.Enabled || len(out.OncallChannelIDs) > 0) {
		return out, errors.New("OnPager returned invalid handover settings")
	}
	if input != nil {
		want, got := slices.Clone(input.OncallChannelIDs), slices.Clone(out.OncallChannelIDs)
		slices.Sort(want)
		slices.Sort(got)
		if out.Revision < 1 || out.Revision < input.Revision || out.Enabled != input.Enabled || !slices.Equal(want, got) {
			return out, errors.New("OnPager did not confirm the requested handover settings")
		}
	}
	return out, nil
}

func (c *Client) GetScheduleHandover(ctx context.Context, team, schedule string) (ScheduleHandover, error) {
	return c.handoverRequest(ctx, http.MethodGet, team, schedule, nil)
}
func (c *Client) PutScheduleHandover(ctx context.Context, team, schedule string, input ScheduleHandover) (ScheduleHandover, error) {
	return c.handoverRequest(ctx, http.MethodPut, team, schedule, &input)
}
