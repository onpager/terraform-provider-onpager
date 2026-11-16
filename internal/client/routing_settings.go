package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type DefaultRoute struct {
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
}
type RoutingSettings struct {
	DefaultRoute *DefaultRoute `json:"default_route"`
	Committed    bool          `json:"-"`
}

func validDefaultRoute(v *DefaultRoute) bool {
	return v == nil || (v.TargetKind == "team" || v.TargetKind == "service") && validResponseID(v.TargetID)
}
func sameDefaultRoute(a, b *DefaultRoute) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (c *Client) routingSettingsRequest(ctx context.Context, input *RoutingSettings) (RoutingSettings, error) {
	var out RoutingSettings
	method := http.MethodGet
	var body io.Reader
	if input != nil {
		if !validDefaultRoute(input.DefaultRoute) {
			return out, errors.New("default route requires team/service and a canonical nonzero target UUID, or explicit null")
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return out, errors.New("could not encode OnPager routing settings")
		}
		body, method = bytes.NewReader(raw), http.MethodPatch
	}
	res, err := c.do(ctx, method, "/v1/settings/routing", body, nil)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && input != nil
	if out.Committed {
		out.DefaultRoute = input.DefaultRoute
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
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
	if res.StatusCode != http.StatusOK {
		return out, errors.New("OnPager returned an unexpected routing settings status")
	}
	var fields map[string]json.RawMessage
	var full RoutingSettings
	if json.Unmarshal(raw, &fields) != nil || fields == nil || fields["default_route"] == nil || json.Unmarshal(raw, &full) != nil || !validDefaultRoute(full.DefaultRoute) {
		return out, errors.New("OnPager returned incomplete or invalid routing settings")
	}
	if input != nil && !sameDefaultRoute(input.DefaultRoute, full.DefaultRoute) {
		return out, errors.New("OnPager did not confirm the desired complete default route")
	}
	full.Committed = out.Committed
	return full, nil
}

func (c *Client) GetRoutingSettings(ctx context.Context) (RoutingSettings, error) {
	return c.routingSettingsRequest(ctx, nil)
}
func (c *Client) UpdateRoutingSettings(ctx context.Context, in RoutingSettings) (RoutingSettings, error) {
	return c.routingSettingsRequest(ctx, &in)
}
