package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type Team struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type Service struct {
	ID     string `json:"id"`
	TeamID string `json:"team_id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type Receipt struct {
	ID string `json:"id"`
}

var responseIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validResponseID(id string) bool {
	return responseIDPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}
func validateEntity(id, name, expected string) error {
	if !validResponseID(id) || strings.TrimSpace(name) == "" || (expected != "" && id != expected) {
		return errors.New("OnPager returned an incomplete resource or unexpected identity")
	}
	return nil
}
func validateReceipt(v Receipt, expected string) error {
	if v.ID != expected {
		return errors.New("OnPager returned an invalid mutation receipt")
	}
	return nil
}

func teamPath(id string) string    { return "/v1/teams/" + url.PathEscape(id) }
func servicePath(id string) string { return "/v1/services/" + url.PathEscape(id) }

func (c *Client) CreateTeam(ctx context.Context, name string) (Team, error) {
	var out Team
	err := c.json(ctx, http.MethodPost, "/v1/teams", map[string]string{"name": name}, &out, 0)
	if err == nil {
		err = validateEntity(out.ID, out.Name, "")
	}
	return out, err
}
func (c *Client) GetTeam(ctx context.Context, id string) (Team, error) {
	var out Team
	err := c.json(ctx, http.MethodGet, teamPath(id), nil, &out, 0)
	if err == nil {
		err = validateEntity(out.ID, out.Name, id)
	}
	return out, err
}
func (c *Client) RenameTeam(ctx context.Context, id, name string) (Receipt, error) {
	var out Receipt
	err := c.json(ctx, http.MethodPatch, teamPath(id), map[string]string{"name": name}, &out, 0)
	if err == nil {
		err = validateReceipt(out, id)
	}
	return out, err
}
func (c *Client) DisableTeam(ctx context.Context, id string) error {
	return allowAbsent(c.json(ctx, http.MethodPost, teamPath(id)+"/disable", nil, nil, 0))
}
func (c *Client) CreateService(ctx context.Context, team, name string) (Service, error) {
	var out Service
	err := c.json(ctx, http.MethodPost, "/v1/services", map[string]string{"team_id": team, "name": name}, &out, 0)
	if err == nil {
		err = validateEntity(out.ID, out.Name, "")
		if out.TeamID != team {
			err = errors.New("OnPager returned an unexpected service owner")
		}
	}
	return out, err
}
func (c *Client) GetService(ctx context.Context, id string) (Service, error) {
	var out Service
	err := c.json(ctx, http.MethodGet, servicePath(id), nil, &out, 0)
	if err == nil {
		err = validateEntity(out.ID, out.Name, id)
		if !validResponseID(out.TeamID) {
			err = errors.New("OnPager returned an invalid service owner")
		}
	}
	return out, err
}
func (c *Client) RenameService(ctx context.Context, id, name string) (Receipt, error) {
	var out Receipt
	err := c.json(ctx, http.MethodPatch, servicePath(id), map[string]string{"name": name}, &out, 0)
	if err == nil {
		err = validateReceipt(out, id)
	}
	return out, err
}
func (c *Client) TransferService(ctx context.Context, id, team string) (Receipt, error) {
	var out Receipt
	err := c.json(ctx, http.MethodPost, servicePath(id)+"/transfer", map[string]string{"team_id": team}, &out, 0)
	if err == nil {
		err = validateReceipt(out, id)
	}
	return out, err
}
func (c *Client) DisableService(ctx context.Context, id string) error {
	return allowAbsent(c.json(ctx, http.MethodPost, servicePath(id)+"/disable", nil, nil, 0))
}
