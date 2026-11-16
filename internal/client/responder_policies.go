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

// TeamResponderPolicyInput selects one exact importance and lets Control derive
// the immutable chain snapshot. Publication revisions are not CAS preconditions.
type TeamResponderPolicyInput struct {
	Importance string `json:"importance"`
	Strategy   string `json:"strategy"`
	ChainID    string `json:"chain_id,omitempty"`
}

type ResponderChainStep struct {
	Kind         string  `json:"kind"`
	WaitSeconds  int64   `json:"wait_seconds,omitempty"`
	TargetUserID *string `json:"target_user_id,omitempty"`
	ScheduleID   *string `json:"schedule_id,omitempty"`
	Importance   string  `json:"importance,omitempty"`
}

// Configured is false only for a validated successful complete zero sentinel.
// Committed remains true after any successful publication status, even if its
// receipt cannot be decoded, so Terraform can retain the known desired intent.
type TeamResponderPolicy struct {
	TeamID     string               `json:"team_id"`
	Importance string               `json:"importance"`
	Strategy   string               `json:"strategy"`
	ChainID    string               `json:"chain_id"`
	Revision   int64                `json:"revision"`
	ChainSteps []ResponderChainStep `json:"chain_steps"`
	Configured bool                 `json:"-"`
	Committed  bool                 `json:"-"`
}

const responderZeroUUID = "00000000-0000-0000-0000-000000000000"

func validResponderImportance(v string) bool { return v == "default" || v == "important" }
func validResponderPolicyInput(in TeamResponderPolicyInput) bool {
	return validResponderImportance(in.Importance) && (in.Strategy == "team_fanout" && in.ChainID == "" || in.Strategy == "team_chain" && validResponseID(in.ChainID))
}
func responderPolicyPath(team string) string { return teamPath(team) + "/responder-policies" }

func decodeResponderPolicy(raw []byte, team, importance string, allowAbsent bool) (TeamResponderPolicy, error) {
	var out TeamResponderPolicy
	invalid := errors.New("OnPager returned an incomplete or invalid team responder policy identity, revision or canonical chain snapshot")
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, &out) != nil {
		return TeamResponderPolicy{}, invalid
	}
	for _, name := range []string{"team_id", "importance", "strategy", "chain_id", "revision", "chain_steps"} {
		value, ok := fields[name]
		if !ok || name != "chain_steps" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return TeamResponderPolicy{}, invalid
		}
	}
	if allowAbsent && out.TeamID == responderZeroUUID && out.Importance == "" && out.Strategy == "" && out.ChainID == responderZeroUUID && out.Revision == 0 && bytes.Equal(bytes.TrimSpace(fields["chain_steps"]), []byte("null")) {
		return out, nil
	}
	if out.TeamID != team || out.Importance != importance || out.Revision <= 0 || out.ChainSteps == nil {
		return TeamResponderPolicy{}, invalid
	}
	switch out.Strategy {
	case "team_fanout":
		if out.ChainID != responderZeroUUID || len(out.ChainSteps) != 0 {
			return TeamResponderPolicy{}, invalid
		}
	case "team_chain":
		if !validResponseID(out.ChainID) || len(out.ChainSteps) == 0 || out.ChainSteps[len(out.ChainSteps)-1].Kind != "wait" {
			return TeamResponderPolicy{}, invalid
		}
		var rawSteps []map[string]json.RawMessage
		if json.Unmarshal(fields["chain_steps"], &rawSteps) != nil {
			return TeamResponderPolicy{}, invalid
		}
		hasNotify := false
		for i, step := range out.ChainSteps {
			f := rawSteps[i]
			for _, name := range []string{"webhook_id", "target_channel_id", "target_id", "channel_sources"} {
				if _, ok := f[name]; ok {
					return TeamResponderPolicy{}, invalid
				}
			}
			for _, name := range []string{"kind", "wait_seconds", "importance"} {
				if v, ok := f[name]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
					return TeamResponderPolicy{}, invalid
				}
			}
			if step.Importance != "" && !validResponderImportance(step.Importance) {
				return TeamResponderPolicy{}, invalid
			}
			switch step.Kind {
			case "wait":
				if step.WaitSeconds <= 0 || step.WaitSeconds > 9223372036 || step.TargetUserID != nil || step.ScheduleID != nil || step.Importance != "" {
					return TeamResponderPolicy{}, invalid
				}
			case "notify_user":
				if step.WaitSeconds != 0 || step.TargetUserID == nil || !validResponseID(*step.TargetUserID) || step.ScheduleID != nil {
					return TeamResponderPolicy{}, invalid
				}
				hasNotify = true
			case "notify_oncall":
				if step.WaitSeconds != 0 || step.TargetUserID != nil || step.ScheduleID == nil || !validResponseID(*step.ScheduleID) {
					return TeamResponderPolicy{}, invalid
				}
				hasNotify = true
			default:
				return TeamResponderPolicy{}, invalid
			}
		}
		if !hasNotify {
			return TeamResponderPolicy{}, invalid
		}
	default:
		return TeamResponderPolicy{}, invalid
	}
	out.Configured = true
	return out, nil
}

func (c *Client) responderPolicyRequest(ctx context.Context, method, team, importance string, in *TeamResponderPolicyInput) (TeamResponderPolicy, error) {
	var out TeamResponderPolicy
	if !validResponseID(team) || !validResponderImportance(importance) || in != nil && !validResponderPolicyInput(*in) {
		return out, errors.New("team responder policy requires a nonzero canonical team UUID, explicit default or important importance, and a valid strategy with chain_id only for team_chain")
	}
	route := responderPolicyPath(team)
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return out, errors.New("could not encode OnPager responder policy")
		}
		body = bytes.NewReader(raw)
	} else {
		route += "?importance=" + url.QueryEscape(importance)
	}
	res, err := c.do(ctx, method, route, body, nil)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && method == http.MethodPost
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
	expected := http.StatusOK
	if method == http.MethodPost {
		expected = http.StatusCreated
	}
	if method == http.MethodDelete {
		expected = http.StatusNoContent
	}
	if res.StatusCode != expected {
		return out, errors.New("OnPager returned an unexpected team responder policy success status")
	}
	if method == http.MethodDelete {
		return out, nil
	}
	value, err := decodeResponderPolicy(raw, team, importance, method == http.MethodGet)
	if err != nil {
		return out, err
	}
	if in != nil && (value.Strategy != in.Strategy || value.Strategy == "team_chain" && value.ChainID != in.ChainID) {
		return out, errors.New("OnPager did not confirm the requested team responder policy strategy and chain")
	}
	value.Committed = out.Committed
	return value, nil
}

func (c *Client) GetTeamResponderPolicy(ctx context.Context, team, importance string) (TeamResponderPolicy, error) {
	return c.responderPolicyRequest(ctx, http.MethodGet, team, importance, nil)
}
func (c *Client) PublishTeamResponderPolicy(ctx context.Context, team string, in TeamResponderPolicyInput) (TeamResponderPolicy, error) {
	return c.responderPolicyRequest(ctx, http.MethodPost, team, in.Importance, &in)
}
func (c *Client) ResetTeamResponderPolicy(ctx context.Context, team, importance string) error {
	_, err := c.responderPolicyRequest(ctx, http.MethodDelete, team, importance, nil)
	return err
}
