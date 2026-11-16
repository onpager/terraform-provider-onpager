package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type TeamResponderGuardPolicyInput struct {
	MaxOpenRequestsPerSubject int64 `json:"max_open_requests_per_subject"`
}

// TeamResponderGuardPolicy is one team's effective guard policy. Only an
// explicit publication is a managed override; the default is logical absence.
// Revision is observed metadata and is never a write precondition. Committed
// records successful publication even when its receipt cannot be validated.
type TeamResponderGuardPolicy struct {
	TeamResponderGuardPolicyInput
	Revision  int64 `json:"revision"`
	IsDefault bool  `json:"is_default"`
	Committed bool  `json:"-"`
}

func (c *Client) teamResponderGuardPolicyRequest(ctx context.Context, method, team string, input *TeamResponderGuardPolicyInput) (TeamResponderGuardPolicy, error) {
	var out TeamResponderGuardPolicy
	if !validResponseID(team) {
		return out, errors.New("team responder guard policies require a nonzero canonical lowercase team UUID")
	}
	var body io.Reader
	if input != nil {
		if input.MaxOpenRequestsPerSubject < 1 || input.MaxOpenRequestsPerSubject > 1000 {
			return out, errors.New("max_open_requests_per_subject must be between 1 and 1000")
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return out, errors.New("could not encode OnPager team responder guard policy")
		}
		body = bytes.NewReader(raw)
	}
	res, err := c.do(ctx, method, "/v1/teams/"+team+"/responder-guard-policies", body, nil)
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
	if method == http.MethodDelete {
		if res.StatusCode != http.StatusNoContent {
			return out, errors.New("OnPager returned an unexpected team responder guard policy reset success status")
		}
		return out, nil
	}
	expectedStatus := http.StatusOK
	if method == http.MethodPost {
		expectedStatus = http.StatusCreated
	}
	if res.StatusCode != expectedStatus {
		return out, errors.New("OnPager returned an unexpected team responder guard policy success status")
	}
	var wire struct {
		MaxOpenRequestsPerSubject *int64 `json:"max_open_requests_per_subject"`
		Revision                  *int64 `json:"revision"`
		IsDefault                 *bool  `json:"is_default"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.MaxOpenRequestsPerSubject == nil || wire.Revision == nil || wire.IsDefault == nil {
		return out, errors.New("OnPager returned an incomplete or invalid team responder guard policy")
	}
	if *wire.MaxOpenRequestsPerSubject < 1 || *wire.MaxOpenRequestsPerSubject > 1000 || *wire.Revision < 0 || (*wire.IsDefault && *wire.Revision != 0) || (!*wire.IsDefault && *wire.Revision == 0) {
		return out, errors.New("OnPager returned an invalid team responder guard policy limit, revision or default indicator")
	}
	if input != nil && (*wire.IsDefault || *wire.MaxOpenRequestsPerSubject != input.MaxOpenRequestsPerSubject) {
		return out, errors.New("OnPager did not confirm the requested explicit team responder guard policy")
	}
	out.MaxOpenRequestsPerSubject, out.Revision, out.IsDefault = *wire.MaxOpenRequestsPerSubject, *wire.Revision, *wire.IsDefault
	return out, nil
}

func (c *Client) GetTeamResponderGuardPolicy(ctx context.Context, team string) (TeamResponderGuardPolicy, error) {
	return c.teamResponderGuardPolicyRequest(ctx, http.MethodGet, team, nil)
}

func (c *Client) PublishTeamResponderGuardPolicy(ctx context.Context, team string, input TeamResponderGuardPolicyInput) (TeamResponderGuardPolicy, error) {
	return c.teamResponderGuardPolicyRequest(ctx, http.MethodPost, team, &input)
}

func (c *Client) ResetTeamResponderGuardPolicy(ctx context.Context, team string) error {
	_, err := c.teamResponderGuardPolicyRequest(ctx, http.MethodDelete, team, nil)
	return err
}
