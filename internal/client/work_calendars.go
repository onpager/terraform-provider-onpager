package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// WorkCalendarReferenceDate pins recurring projections to a fixed UTC week.
const WorkCalendarReferenceDate = "2024-01-01"

type WorkCalendarWindow struct {
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}
type WorkCalendarWeekly struct {
	ReferenceDate string               `json:"reference_date"`
	Intervals     []WorkCalendarWindow `json:"intervals"`
}
type WorkCalendarClock struct {
	ReferenceDate string               `json:"reference_date"`
	Windows       []WorkCalendarWindow `json:"windows"`
}
type WorkCalendarInput struct {
	Name                      string              `json:"name"`
	BaseSchedule              *WorkCalendarWeekly `json:"base_schedule,omitempty"`
	WorkingWeekendSchedule    *WorkCalendarWeekly `json:"working_weekend_schedule,omitempty"`
	InheritProductionCalendar bool                `json:"inherit_production_calendar"`
	ProductionCountryCode     string              `json:"production_country_code,omitempty"`
}
type WorkCalendar struct {
	ID                string     `json:"id"`
	TeamID            string     `json:"team_id"`
	Revision          int64      `json:"revision"`
	AggregateRevision int64      `json:"aggregate_revision"`
	RetiredAt         *time.Time `json:"retired_at,omitempty"`
	WorkCalendarInput
}
type WorkCalendarEntryInput struct {
	Name      string             `json:"name"`
	Effect    string             `json:"effect"`
	RuleType  string             `json:"rule_type"`
	StartDate string             `json:"start_date,omitempty"`
	EndDate   string             `json:"end_date,omitempty"`
	StartsAt  string             `json:"starts_at,omitempty"`
	EndsAt    string             `json:"ends_at,omitempty"`
	Weekdays  []string           `json:"weekdays,omitempty"`
	Month     int64              `json:"month,omitempty"`
	Day       int64              `json:"day,omitempty"`
	Clock     *WorkCalendarClock `json:"clock,omitempty"`
}
type WorkCalendarEntry struct {
	ID         string `json:"id"`
	CalendarID string `json:"calendar_id"`
	Revision   int64  `json:"revision"`
	WorkCalendarEntryInput
}

func workCalendarPath(team, id string) string {
	p := teamPath(team) + "/work-calendars"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}
func workCalendarEntryPath(team, calendar, id string) string {
	p := workCalendarPath(team, calendar) + "/entries"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}
func workCalendarProjection(p string) string {
	return p + "?reference_date=" + WorkCalendarReferenceDate
}
func workCalendarValidate(v WorkCalendar, team, id string) error {
	if err := validateEntity(v.ID, v.Name, id); err != nil {
		return err
	}
	if v.TeamID != team || v.Revision <= 0 || v.AggregateRevision < v.Revision || v.BaseSchedule == nil || v.WorkingWeekendSchedule == nil || v.ProductionCountryCode == "" {
		return errors.New("OnPager returned incomplete work calendar settings or unexpected owner")
	}
	return nil
}
func workCalendarEntryValidate(v WorkCalendarEntry, calendar, id string) error {
	if err := validateEntity(v.ID, v.Name, id); err != nil {
		return err
	}
	if v.CalendarID != calendar || v.Revision <= 0 {
		return errors.New("OnPager returned an incomplete calendar entry or unexpected owner")
	}
	return nil
}
func (c *Client) CreateWorkCalendar(ctx context.Context, team string, in WorkCalendarInput) (WorkCalendar, error) {
	var out WorkCalendar
	err := c.json(ctx, http.MethodPost, workCalendarPath(team, ""), in, &out, 0)
	if err == nil {
		err = workCalendarValidate(out, team, "")
	}
	return out, err
}
func (c *Client) GetWorkCalendar(ctx context.Context, team, id string) (WorkCalendar, error) {
	var out WorkCalendar
	err := c.json(ctx, http.MethodGet, workCalendarProjection(workCalendarPath(team, id)), nil, &out, 0)
	if err == nil {
		err = workCalendarValidate(out, team, id)
	}
	return out, err
}
func (c *Client) UpdateWorkCalendar(ctx context.Context, team, id string, revision int64, in WorkCalendarInput) (WorkCalendar, error) {
	var out WorkCalendar
	err := c.json(ctx, http.MethodPatch, workCalendarPath(team, id), in, &out, revision)
	if err == nil {
		err = workCalendarValidate(out, team, id)
	}
	return out, err
}
func (c *Client) ArchiveWorkCalendar(ctx context.Context, team, id string, revision int64) error {
	return allowAbsent(c.json(ctx, http.MethodDelete, workCalendarPath(team, id), nil, nil, revision))
}
func (c *Client) CreateWorkCalendarEntry(ctx context.Context, team, calendar string, in WorkCalendarEntryInput) (WorkCalendarEntry, error) {
	var out WorkCalendarEntry
	err := c.json(ctx, http.MethodPost, workCalendarEntryPath(team, calendar, ""), in, &out, 0)
	if err == nil {
		err = workCalendarEntryValidate(out, calendar, "")
	}
	return out, err
}
func (c *Client) GetWorkCalendarEntry(ctx context.Context, team, calendar, id string) (WorkCalendarEntry, error) {
	var out WorkCalendarEntry
	err := c.json(ctx, http.MethodGet, workCalendarProjection(workCalendarEntryPath(team, calendar, id)), nil, &out, 0)
	if err == nil {
		err = workCalendarEntryValidate(out, calendar, id)
	}
	return out, err
}
func (c *Client) UpdateWorkCalendarEntry(ctx context.Context, team, calendar, id string, revision int64, in WorkCalendarEntryInput) (WorkCalendarEntry, error) {
	var out WorkCalendarEntry
	err := c.json(ctx, http.MethodPut, workCalendarEntryPath(team, calendar, id), in, &out, revision)
	if err == nil {
		err = workCalendarEntryValidate(out, calendar, id)
	}
	return out, err
}
func (c *Client) DeleteWorkCalendarEntry(ctx context.Context, team, calendar, id string, revision int64) error {
	return allowAbsent(c.json(ctx, http.MethodDelete, workCalendarEntryPath(team, calendar, id), nil, nil, revision))
}
