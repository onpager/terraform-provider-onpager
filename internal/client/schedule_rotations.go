package client

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"time"
)

// ScheduleRecurrence describes the recurrence anchor's calendar interval.
type ScheduleRecurrence struct {
	Every int64  `json:"every"`
	Unit  string `json:"unit"`
}

// PrimaryRotationRule is a complete primary declaration. Nullable fields are
// explicit: omission must not silently turn a bounded or filtered plan into one
// with unlimited coverage.
type PrimaryRotationRule struct {
	StartsAt                string             `json:"starts_at"`
	EndsAt                  *string            `json:"ends_at"`
	UserIDs                 []string           `json:"user_ids"`
	Recurrence              ScheduleRecurrence `json:"recurrence"`
	Weekdays                []string           `json:"weekdays"`
	MaxShiftDurationSeconds *int64             `json:"max_shift_duration_seconds"`
}

type OverrideRotationRule struct {
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
	UserID   string `json:"user_id"`
}

// RotationReceipt carries the child's concurrency revision. AggregateRevision
// describes unrelated aggregate invalidation and is never used for writes.
type RotationReceipt struct {
	RotationID        string `json:"rotation_id"`
	Revision          int64  `json:"revision"`
	AggregateRevision int64  `json:"aggregate_revision"`
}

type PrimaryRotationDeclaration struct {
	ID                string              `json:"id"`
	ScheduleID        string              `json:"schedule_id"`
	Kind              string              `json:"kind"`
	Revision          int64               `json:"revision"`
	AggregateRevision int64               `json:"aggregate_revision"`
	EffectiveFrom     string              `json:"effective_from"`
	Configuration     PrimaryRotationRule `json:"configuration"`
}

type OverrideRotationDeclaration struct {
	ID                string               `json:"id"`
	ScheduleID        string               `json:"schedule_id"`
	Kind              string               `json:"kind"`
	Revision          int64                `json:"revision"`
	AggregateRevision int64                `json:"aggregate_revision"`
	EffectiveFrom     string               `json:"effective_from"`
	Configuration     OverrideRotationRule `json:"configuration"`
}

func rotationConfigurationFields(data []byte, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) != len(required) {
		return errors.New("OnPager returned incomplete or invalid rotation configuration")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return errors.New("OnPager returned incomplete rotation configuration")
		}
	}
	return nil
}
func (r *PrimaryRotationRule) UnmarshalJSON(data []byte) error {
	if err := rotationConfigurationFields(data, "starts_at", "ends_at", "user_ids", "recurrence", "weekdays", "max_shift_duration_seconds"); err != nil {
		return err
	}
	type rule PrimaryRotationRule
	return json.Unmarshal(data, (*rule)(r))
}
func (r *OverrideRotationRule) UnmarshalJSON(data []byte) error {
	if err := rotationConfigurationFields(data, "starts_at", "ends_at", "user_id"); err != nil {
		return err
	}
	type rule OverrideRotationRule
	return json.Unmarshal(data, (*rule)(r))
}

func rotationPath(schedule, kind, id string) string {
	p := schedulePath(schedule) + "/" + kind + "-rotations"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}
func rotationInstant(raw string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || at.Year() < 1 || at.Year() > 9999 || at.Nanosecond() != 0 || at.UTC().Year() < 1 || at.UTC().Year() > 9999 {
		return time.Time{}, errors.New("OnPager returned an invalid whole-second rotation timestamp")
	}
	return at, nil
}
func primaryRotationRuleValidate(rule PrimaryRotationRule) error {
	start, err := rotationInstant(rule.StartsAt)
	if err != nil {
		return err
	}
	if rule.EndsAt != nil {
		end, err := rotationInstant(*rule.EndsAt)
		if err != nil {
			return err
		}
		if !end.After(start) {
			return errors.New("OnPager returned an invalid primary rotation interval")
		}
	}
	if len(rule.UserIDs) == 0 {
		return errors.New("OnPager returned an empty primary rotation roster")
	}
	users := make(map[string]bool, len(rule.UserIDs))
	for _, id := range rule.UserIDs {
		if !validResponseID(id) || users[id] {
			return errors.New("OnPager returned invalid primary rotation participants")
		}
		users[id] = true
	}
	if rule.Recurrence.Every < 1 || rule.Recurrence.Every > math.MaxInt32 {
		return errors.New("OnPager returned invalid rotation recurrence")
	}
	switch rule.Recurrence.Unit {
	case "hours", "days", "weeks", "months":
	default:
		return errors.New("OnPager returned invalid rotation recurrence")
	}
	if rule.Weekdays != nil {
		if len(rule.Weekdays) == 0 || len(rule.Weekdays) > 7 {
			return errors.New("OnPager returned invalid rotation weekdays")
		}
		seen := map[string]bool{}
		for _, day := range rule.Weekdays {
			switch day {
			case "MO", "TU", "WE", "TH", "FR", "SA", "SU":
			default:
				return errors.New("OnPager returned invalid rotation weekdays")
			}
			if seen[day] {
				return errors.New("OnPager returned duplicate rotation weekdays")
			}
			seen[day] = true
		}
	}
	if rule.MaxShiftDurationSeconds != nil {
		seconds := *rule.MaxShiftDurationSeconds
		last := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		if seconds <= 0 || seconds > last.Unix()-start.Unix() {
			return errors.New("OnPager returned invalid rotation maximum shift duration")
		}
	}
	return nil
}
func overrideRotationRuleValidate(rule OverrideRotationRule) error {
	start, err := rotationInstant(rule.StartsAt)
	if err != nil {
		return err
	}
	end, err := rotationInstant(rule.EndsAt)
	if err != nil {
		return err
	}
	if !end.After(start) || !validResponseID(rule.UserID) {
		return errors.New("OnPager returned an invalid override rotation rule")
	}
	return nil
}
func rotationDeclarationValidate(id, schedule, kind string, revision int64, effective, expectedID, expectedSchedule, expectedKind string) error {
	if !validResponseID(id) || !validResponseID(schedule) || id != expectedID || schedule != expectedSchedule || kind != expectedKind || revision < 1 {
		return errors.New("OnPager returned an incomplete rotation declaration or unexpected identity")
	}
	_, err := rotationInstant(effective)
	return err
}
func rotationReceiptValidate(out RotationReceipt, expected string) error {
	if !validResponseID(out.RotationID) || out.Revision < 1 || expected != "" && out.RotationID != expected {
		return errors.New("OnPager returned an invalid rotation mutation receipt")
	}
	return nil
}

// Keep a trustworthy ID after malformed successful create responses so callers
// can retain partial state. Never adopt a foreign or invalid update identity.
func sanitizeRotationReceipt(out *RotationReceipt, expected string) {
	if !validResponseID(out.RotationID) || expected != "" && out.RotationID != expected {
		out.RotationID = ""
	}
}
func (c *Client) CreatePrimaryRotation(ctx context.Context, scheduleID, effectiveFrom string, rule PrimaryRotationRule) (RotationReceipt, error) {
	input := struct {
		PrimaryRotationRule
		EffectiveFrom string `json:"effective_from"`
	}{rule, effectiveFrom}
	return c.mutateRotation(ctx, http.MethodPost, rotationPath(scheduleID, "primary", ""), "", 0, input)
}
func (c *Client) GetPrimaryRotation(ctx context.Context, scheduleID, rotationID string) (PrimaryRotationDeclaration, error) {
	var out PrimaryRotationDeclaration
	err := c.json(ctx, http.MethodGet, rotationPath(scheduleID, "primary", rotationID)+"/declaration", nil, &out, 0)
	if err == nil {
		err = rotationDeclarationValidate(out.ID, out.ScheduleID, out.Kind, out.Revision, out.EffectiveFrom, rotationID, scheduleID, "primary")
	}
	if err == nil {
		err = primaryRotationRuleValidate(out.Configuration)
	}
	return out, err
}
func (c *Client) UpdatePrimaryRotation(ctx context.Context, scheduleID, rotationID string, revision int64, effectiveFrom string, rule PrimaryRotationRule) (RotationReceipt, error) {
	input := struct {
		EffectiveFrom string              `json:"effective_from"`
		Configuration PrimaryRotationRule `json:"configuration"`
	}{effectiveFrom, rule}
	return c.mutateRotation(ctx, http.MethodPut, rotationPath(scheduleID, "primary", rotationID)+"/declaration", rotationID, revision, input)
}
func (c *Client) DeletePrimaryRotation(ctx context.Context, scheduleID, rotationID string, revision int64) error {
	return c.deleteRotation(ctx, scheduleID, "primary", rotationID, revision)
}
func (c *Client) CreateOverrideRotation(ctx context.Context, scheduleID string, rule OverrideRotationRule) (RotationReceipt, error) {
	return c.mutateRotation(ctx, http.MethodPost, rotationPath(scheduleID, "override", ""), "", 0, rule)
}
func (c *Client) GetOverrideRotation(ctx context.Context, scheduleID, rotationID string) (OverrideRotationDeclaration, error) {
	var out OverrideRotationDeclaration
	err := c.json(ctx, http.MethodGet, rotationPath(scheduleID, "override", rotationID)+"/declaration", nil, &out, 0)
	if err == nil {
		err = rotationDeclarationValidate(out.ID, out.ScheduleID, out.Kind, out.Revision, out.EffectiveFrom, rotationID, scheduleID, "override")
	}
	if err == nil {
		err = overrideRotationRuleValidate(out.Configuration)
	}
	if err == nil {
		effective, _ := rotationInstant(out.EffectiveFrom)
		start, _ := rotationInstant(out.Configuration.StartsAt)
		if !effective.Equal(start) {
			err = errors.New("OnPager returned an override boundary inconsistent with its interval")
		}
	}
	return out, err
}
func (c *Client) UpdateOverrideRotation(ctx context.Context, scheduleID, rotationID string, revision int64, rule OverrideRotationRule) (RotationReceipt, error) {
	input := struct {
		Configuration OverrideRotationRule `json:"configuration"`
	}{rule}
	return c.mutateRotation(ctx, http.MethodPut, rotationPath(scheduleID, "override", rotationID)+"/declaration", rotationID, revision, input)
}
func (c *Client) DeleteOverrideRotation(ctx context.Context, scheduleID, rotationID string, revision int64) error {
	return c.deleteRotation(ctx, scheduleID, "override", rotationID, revision)
}
func (c *Client) mutateRotation(ctx context.Context, method, path, expectedID string, revision int64, input any) (RotationReceipt, error) {
	var out RotationReceipt
	err := c.json(ctx, method, path, input, &out, revision)
	if err == nil {
		err = rotationReceiptValidate(out, expectedID)
	}
	sanitizeRotationReceipt(&out, expectedID)
	return out, err
}
func (c *Client) deleteRotation(ctx context.Context, schedule, kind, id string, revision int64) error {
	_, err := c.mutateRotation(ctx, http.MethodDelete, rotationPath(schedule, kind, id)+"?mode=all", id, revision, nil)
	return allowAbsent(err)
}
