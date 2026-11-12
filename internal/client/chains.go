package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

type ChainStep struct {
	Kind           string   `json:"kind"`
	WaitSeconds    int64    `json:"wait_seconds,omitempty"`
	TargetUserID   *string  `json:"target_user_id,omitempty"`
	ScheduleID     *string  `json:"schedule_id,omitempty"`
	WebhookID      *string  `json:"webhook_id,omitempty"`
	Importance     string   `json:"importance,omitempty"`
	ChannelSources []string `json:"channel_sources,omitempty"`
}
type ChainInput struct {
	Name                    string      `json:"name"`
	Steps                   []ChainStep `json:"steps"`
	WorkCalendarID          *string     `json:"work_calendar_id"`
	WorkCalendarReference   string      `json:"work_calendar_reference"`
	OffHoursDelegateChainID *string     `json:"off_hours_delegate_chain_id"`
}
type Chain struct {
	ChainInput
	ID        string  `json:"id"`
	TeamID    string  `json:"team_id"`
	ETag      string  `json:"etag"`
	RetiredAt *string `json:"retired_at,omitempty"`
}

var chainTagPattern = regexp.MustCompile(`^"[0-9a-f]{64}"$`)

func chainPath(team, id string) string {
	return teamPath(team) + "/escalation-chains/" + url.PathEscape(id)
}
func validateChain(v Chain, team, id string) error {
	if v.ID != id || v.TeamID != team || !validResponseID(v.ID) || !validResponseID(v.TeamID) || v.Name == "" || v.Steps == nil || !chainTagPattern.MatchString(v.ETag) {
		return errors.New("OnPager returned an invalid escalation chain or ETag")
	}
	if v.WorkCalendarReference != "start_time" && v.WorkCalendarReference != "step_time" {
		return errors.New("OnPager returned an invalid chain calendar reference")
	}
	for _, p := range []*string{v.WorkCalendarID, v.OffHoursDelegateChainID} {
		if p != nil && !validResponseID(*p) {
			return errors.New("OnPager returned an invalid chain reference")
		}
	}
	if v.RetiredAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *v.RetiredAt); err != nil {
			return errors.New("OnPager returned an invalid chain retirement timestamp")
		}
	}
	return nil
}
func (c *Client) CreateChain(ctx context.Context, team string, input ChainInput) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.json(ctx, http.MethodPost, teamPath(team)+"/escalation-chains", input, &out, 0)
	if err == nil && !validResponseID(out.ID) {
		err = errors.New("OnPager returned an invalid new chain identity")
	}
	return out.ID, err
}
func (c *Client) GetChain(ctx context.Context, team, id string) (Chain, error) {
	var out Chain
	err := c.json(ctx, http.MethodGet, chainPath(team, id), nil, &out, 0)
	if err == nil {
		err = validateChain(out, team, id)
	}
	return out, err
}
func (c *Client) UpdateChain(ctx context.Context, team, id, tag string, input ChainInput) (Chain, error) {
	if !chainTagPattern.MatchString(tag) {
		return Chain{}, errors.New("a current strong SHA-256 chain ETag is required")
	}
	var out Chain
	err := c.jsonWithETag(ctx, http.MethodPut, chainPath(team, id), input, &out, tag)
	if err == nil {
		err = validateChain(out, team, id)
	}
	return out, err
}
func (c *Client) ArchiveChain(ctx context.Context, team, id, tag string) error {
	if !chainTagPattern.MatchString(tag) {
		return errors.New("a current strong SHA-256 chain ETag is required")
	}
	return allowAbsent(c.jsonWithETag(ctx, http.MethodDelete, chainPath(team, id), nil, nil, tag))
}
