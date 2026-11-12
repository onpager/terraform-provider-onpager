package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// Schedule contains metadata only; rotation collections have independent ownership.
type Schedule struct {
	ID             string  `json:"id"`
	TeamID         string  `json:"team_id"`
	Name           string  `json:"name"`
	Enabled        bool    `json:"enabled"`
	WorkCalendarID *string `json:"work_calendar_id"`
	Revision       int64   `json:"revision"`
}

// UnmarshalJSON requires the enabled flag: an incomplete read must never be
// interpreted as a successful disable.
func (s *Schedule) UnmarshalJSON(data []byte) error {
	type scheduleAlias Schedule
	var wire struct {
		*scheduleAlias
		Enabled *bool `json:"enabled"`
	}
	wire.scheduleAlias = (*scheduleAlias)(s)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Enabled == nil {
		return errors.New("schedule response omitted enabled state")
	}
	s.Enabled = *wire.Enabled
	return nil
}

type ScheduleSettings struct {
	Name           *string         `json:"name,omitempty"`
	Enabled        *bool           `json:"enabled,omitempty"`
	WorkCalendarID json.RawMessage `json:"work_calendar_id,omitempty"`
}

func schedulePath(id string) string { return "/v1/schedules/" + url.PathEscape(id) }
func scheduleValidate(v Schedule, expected, team string) error {
	if err := validateEntity(v.ID, v.Name, expected); err != nil {
		return err
	}
	if !validResponseID(v.TeamID) || team != "" && v.TeamID != team || v.Revision < 1 || v.WorkCalendarID != nil && !validResponseID(*v.WorkCalendarID) {
		return errors.New("OnPager returned invalid schedule metadata")
	}
	return nil
}
func (c *Client) CreateSchedule(ctx context.Context, team, name string, calendar *string) (Schedule, error) {
	var out Schedule
	input := struct {
		Name           string  `json:"name"`
		WorkCalendarID *string `json:"work_calendar_id"`
	}{name, calendar}
	err := c.json(ctx, http.MethodPost, teamPath(team)+"/schedules", input, &out, 0)
	if err == nil {
		err = scheduleValidate(out, "", team)
	}
	return out, err
}
func (c *Client) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	var out Schedule
	err := c.json(ctx, http.MethodGet, schedulePath(id), nil, &out, 0)
	if err == nil {
		err = scheduleValidate(out, id, "")
	}
	return out, err
}
func (c *Client) UpdateSchedule(ctx context.Context, id string, revision int64, input ScheduleSettings) (Schedule, error) {
	var out Schedule
	err := c.json(ctx, http.MethodPatch, schedulePath(id), input, &out, revision)
	if err == nil {
		err = scheduleValidate(out, id, "")
	}
	return out, err
}
func (c *Client) DisableSchedule(ctx context.Context, id string, revision int64) (Schedule, error) {
	var out Schedule
	err := c.json(ctx, http.MethodPost, schedulePath(id)+"/disable", nil, &out, revision)
	if IsNotFound(err) {
		return out, nil
	}
	if err == nil {
		err = scheduleValidate(out, id, "")
	}
	if err == nil && out.Enabled {
		err = errors.New("OnPager did not confirm the schedule was disabled")
	}
	return out, err
}
