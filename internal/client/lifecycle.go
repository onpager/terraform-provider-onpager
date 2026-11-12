package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

func (c *Client) ReactivateTeam(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, teamPath(id)+"/reactivate", nil, nil, 0)
}
func (c *Client) ReactivateService(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, servicePath(id)+"/reactivate", nil, nil, 0)
}

type Membership struct {
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	Lead   bool   `json:"lead"`
}

func memberPath(team, user string) string { return teamPath(team) + "/members/" + url.PathEscape(user) }
func validateMembership(v Membership, team, user string) error {
	if v.TeamID != team || v.UserID != user {
		return errors.New("OnPager returned an unexpected membership identity")
	}
	return nil
}
func (c *Client) GetMembership(ctx context.Context, team, user string) (Membership, error) {
	var out Membership
	err := c.json(ctx, http.MethodGet, memberPath(team, user), nil, &out, 0)
	if err == nil {
		err = validateMembership(out, team, user)
	}
	return out, err
}
func (c *Client) AddMembership(ctx context.Context, team, user string, lead bool) (Membership, error) {
	var out Membership
	err := c.json(ctx, http.MethodPost, teamPath(team)+"/members", struct {
		User string `json:"user_id"`
		Lead bool   `json:"lead"`
	}{user, lead}, &out, 0)
	if err == nil {
		err = validateMembership(out, team, user)
	}
	return out, err
}
func (c *Client) UpdateMembership(ctx context.Context, team, user string, lead bool) (Membership, error) {
	var out Membership
	err := c.json(ctx, http.MethodPatch, memberPath(team, user), map[string]bool{"lead": lead}, &out, 0)
	if err == nil {
		err = validateMembership(out, team, user)
	}
	return out, err
}
func (c *Client) RemoveMembership(ctx context.Context, team, user string) error {
	return allowAbsent(c.json(ctx, http.MethodDelete, memberPath(team, user), nil, nil, 0))
}
