package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

var scheduleRecurrenceTypes = map[string]attr.Type{"every": types.Int64Type, "unit": types.StringType}
var scheduleWholeSecondPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.0+)?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)?$`)

type scheduleDeclarationModel struct {
	ID, ScheduleID, StartsAt, EndsAt, EffectiveFrom, UserID, RequestTimezone types.String
	Revision, MaxShiftDurationSeconds                                        types.Int64
	UserIDs                                                                  types.List
	Weekdays                                                                 types.Set
	Recurrence                                                               types.Object
}
type scheduleDeclarationGetter interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}

func scheduleDeclarationLoad(ctx context.Context, source scheduleDeclarationGetter, primary bool, d *diag.Diagnostics) scheduleDeclarationModel {
	m := scheduleDeclarationModel{EffectiveFrom: types.StringNull(), UserID: types.StringNull(), UserIDs: types.ListNull(types.StringType), Weekdays: types.SetNull(types.StringType), Recurrence: types.ObjectNull(scheduleRecurrenceTypes), MaxShiftDurationSeconds: types.Int64Null()}
	fields := map[string]any{"id": &m.ID, "schedule_id": &m.ScheduleID, "revision": &m.Revision, "starts_at": &m.StartsAt, "ends_at": &m.EndsAt, "request_timezone": &m.RequestTimezone}
	if primary {
		fields["effective_from"] = &m.EffectiveFrom
		fields["user_ids"] = &m.UserIDs
		fields["weekdays"] = &m.Weekdays
		fields["recurrence"] = &m.Recurrence
		fields["max_shift_duration_seconds"] = &m.MaxShiftDurationSeconds
	} else {
		fields["user_id"] = &m.UserID
	}
	for key, target := range fields {
		d.Append(source.GetAttribute(ctx, path.Root(key), target)...)
	}
	return m
}
func (m scheduleDeclarationModel) store(ctx context.Context, target *tfsdk.State, primary bool) diag.Diagnostics {
	values := map[string]attr.Value{"id": m.ID, "schedule_id": m.ScheduleID, "revision": m.Revision, "starts_at": m.StartsAt, "ends_at": m.EndsAt, "request_timezone": m.RequestTimezone}
	if primary {
		values["effective_from"] = m.EffectiveFrom
		values["user_ids"] = m.UserIDs
		values["weekdays"] = m.Weekdays
		values["recurrence"] = m.Recurrence
		values["max_shift_duration_seconds"] = m.MaxShiftDurationSeconds
	} else {
		values["user_id"] = m.UserID
	}
	attrTypes := make(map[string]attr.Type, len(values))
	for key, value := range values {
		attrTypes[key] = value.Type(ctx)
	}
	obj, d := types.ObjectValue(attrTypes, values)
	if d.HasError() {
		return d
	}
	return target.Set(ctx, obj)
}
func scheduleDeclarationInstant(value types.String, key string, required bool) (time.Time, error) {
	if value.IsUnknown() {
		return time.Time{}, nil
	}
	if value.IsNull() {
		if !required {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("%s is required", key)
	}
	raw := value.ValueString()
	v, explicit, err := temporalParse(raw)
	if err != nil || !scheduleWholeSecondPattern.MatchString(raw) || v.Nanosecond() != 0 || v.Year() < 1 || v.Year() > 9999 || explicit && (v.UTC().Year() < 1 || v.UTC().Year() > 9999) {
		return time.Time{}, fmt.Errorf("%s must be a whole-second local YYYY-MM-DDTHH:mm:ss or explicit-offset RFC3339 timestamp within years 0001 through 9999; the backend resolves local values in the request timezone", key)
	}
	return v, nil
}
func scheduleDeclarationUUID(value types.String, key string) error {
	if value.IsUnknown() {
		return nil
	}
	if value.IsNull() || !validID(value.ValueString()) {
		return fmt.Errorf("%s must be a nonzero canonical lowercase UUID", key)
	}
	return nil
}
func (m scheduleDeclarationModel) validate(primary bool) error {
	if err := scheduleDeclarationUUID(m.ScheduleID, "schedule_id"); err != nil {
		return err
	}
	start, err := scheduleDeclarationInstant(m.StartsAt, "starts_at", true)
	if err != nil {
		return err
	}
	end, err := scheduleDeclarationInstant(m.EndsAt, "ends_at", !primary)
	if err != nil {
		return err
	}
	if !m.StartsAt.IsUnknown() && !m.EndsAt.IsUnknown() && !m.EndsAt.IsNull() && scheduleDeclarationComparable(m.StartsAt.ValueString(), m.EndsAt.ValueString()) && !start.Before(end) {
		return errors.New("ends_at must follow starts_at")
	}
	if !primary {
		return scheduleDeclarationUUID(m.UserID, "user_id")
	}
	if _, err := scheduleDeclarationInstant(m.EffectiveFrom, "effective_from", true); err != nil {
		return err
	}
	if !m.UserIDs.IsUnknown() {
		if m.UserIDs.IsNull() || len(m.UserIDs.Elements()) == 0 {
			return errors.New("user_ids must be a nonempty ordered list of distinct UUIDs")
		}
		seen := map[string]bool{}
		for _, element := range m.UserIDs.Elements() {
			v, ok := element.(types.String)
			if !ok {
				return errors.New("user_ids must contain UUID strings")
			}
			if v.IsUnknown() {
				continue
			}
			if err := scheduleDeclarationUUID(v, "user_ids"); err != nil {
				return err
			}
			if seen[v.ValueString()] {
				return errors.New("user_ids must not contain duplicates")
			}
			seen[v.ValueString()] = true
		}
	}
	if !m.Recurrence.IsUnknown() {
		if m.Recurrence.IsNull() {
			return errors.New("recurrence is required")
		}
		fields := m.Recurrence.Attributes()
		every, ok := fields["every"].(types.Int64)
		if !ok || every.IsNull() || !every.IsUnknown() && (every.ValueInt64() < 1 || every.ValueInt64() > 2147483647) {
			return errors.New("recurrence.every must be from 1 through 2147483647")
		}
		unit, ok := fields["unit"].(types.String)
		if !ok || unit.IsNull() || !unit.IsUnknown() && !slices.Contains([]string{"hours", "days", "weeks", "months"}, unit.ValueString()) {
			return errors.New("recurrence.unit must be hours, days, weeks, or months")
		}
	}
	if !m.Weekdays.IsUnknown() && !m.Weekdays.IsNull() {
		if len(m.Weekdays.Elements()) == 0 {
			return errors.New("weekdays must be omitted, null, or a nonempty set")
		}
		for _, element := range m.Weekdays.Elements() {
			v, ok := element.(types.String)
			if !ok || v.IsNull() || !v.IsUnknown() && !slices.Contains(workCalendarWeekdays, v.ValueString()) {
				return errors.New("weekdays must contain only MO, TU, WE, TH, FR, SA, SU")
			}
		}
	}
	if !m.MaxShiftDurationSeconds.IsNull() && !m.MaxShiftDurationSeconds.IsUnknown() && m.MaxShiftDurationSeconds.ValueInt64() < 1 {
		return errors.New("max_shift_duration_seconds must be greater than zero")
	}
	return nil
}
func (m scheduleDeclarationModel) primaryRule(ctx context.Context) (client.PrimaryRotationRule, error) {
	in := client.PrimaryRotationRule{StartsAt: m.StartsAt.ValueString(), EndsAt: scheduleCalendar(m.EndsAt)}
	if err := m.validate(true); err != nil {
		return in, err
	}
	if m.Recurrence.IsUnknown() || m.StartsAt.IsUnknown() || m.EffectiveFrom.IsUnknown() || m.EndsAt.IsUnknown() || m.MaxShiftDurationSeconds.IsUnknown() {
		return in, errors.New("rotation configuration must be known before applying")
	}
	if d := m.UserIDs.ElementsAs(ctx, &in.UserIDs, false); d.HasError() {
		return in, errors.New("user_ids must be known UUIDs")
	}
	fields := m.Recurrence.Attributes()
	every := fields["every"].(types.Int64)
	unit := fields["unit"].(types.String)
	if every.IsUnknown() || unit.IsUnknown() {
		return in, errors.New("recurrence must be known before applying")
	}
	in.Recurrence = client.ScheduleRecurrence{Every: every.ValueInt64(), Unit: unit.ValueString()}
	if !m.Weekdays.IsNull() {
		if d := m.Weekdays.ElementsAs(ctx, &in.Weekdays, false); d.HasError() {
			return in, errors.New("weekdays must be known before applying")
		}
		slices.SortFunc(in.Weekdays, func(a, b string) int {
			return slices.Index(workCalendarWeekdays, a) - slices.Index(workCalendarWeekdays, b)
		})
	}
	if !m.MaxShiftDurationSeconds.IsNull() {
		v := m.MaxShiftDurationSeconds.ValueInt64()
		in.MaxShiftDurationSeconds = &v
	}
	return in, nil
}
func (m scheduleDeclarationModel) overrideRule() (client.OverrideRotationRule, error) {
	in := client.OverrideRotationRule{StartsAt: m.StartsAt.ValueString(), EndsAt: m.EndsAt.ValueString(), UserID: m.UserID.ValueString()}
	if err := m.validate(false); err != nil {
		return in, err
	}
	if m.StartsAt.IsUnknown() || m.EndsAt.IsUnknown() || m.UserID.IsUnknown() {
		return in, errors.New("override configuration must be known before applying")
	}
	return in, nil
}
func schedulePrimaryDeclarationState(ctx context.Context, prior scheduleDeclarationModel, v client.PrimaryRotationDeclaration, binding temporalBindings, written bool) (scheduleDeclarationModel, error) {
	m := prior
	m.ID = types.StringValue(v.ID)
	m.ScheduleID = types.StringValue(v.ScheduleID)
	m.Revision = types.Int64Value(v.Revision)
	m.EffectiveFrom = binding.value("EffectiveFrom", prior.EffectiveFrom, v.EffectiveFrom, written)
	m.StartsAt = binding.value("StartsAt", prior.StartsAt, v.Configuration.StartsAt, written)
	end := ""
	if v.Configuration.EndsAt != nil {
		end = *v.Configuration.EndsAt
	}
	m.EndsAt = binding.value("EndsAt", prior.EndsAt, end, written)
	var d diag.Diagnostics
	m.UserIDs, d = types.ListValueFrom(ctx, types.StringType, v.Configuration.UserIDs)
	if d.HasError() {
		return m, errors.New("invalid user_ids in rotation declaration")
	}
	m.Recurrence = types.ObjectValueMust(scheduleRecurrenceTypes, map[string]attr.Value{"every": types.Int64Value(v.Configuration.Recurrence.Every), "unit": types.StringValue(v.Configuration.Recurrence.Unit)})
	m.Weekdays = types.SetNull(types.StringType)
	if v.Configuration.Weekdays != nil {
		m.Weekdays, d = types.SetValueFrom(ctx, types.StringType, v.Configuration.Weekdays)
		if d.HasError() {
			return m, errors.New("invalid weekdays in rotation declaration")
		}
	}
	m.MaxShiftDurationSeconds = types.Int64Null()
	if v.Configuration.MaxShiftDurationSeconds != nil {
		m.MaxShiftDurationSeconds = types.Int64Value(*v.Configuration.MaxShiftDurationSeconds)
	}
	return m, m.validate(true)
}
func scheduleOverrideDeclarationState(prior scheduleDeclarationModel, v client.OverrideRotationDeclaration, binding temporalBindings, written bool) (scheduleDeclarationModel, error) {
	m := prior
	m.ID = types.StringValue(v.ID)
	m.ScheduleID = types.StringValue(v.ScheduleID)
	m.Revision = types.Int64Value(v.Revision)
	m.StartsAt = binding.value("StartsAt", prior.StartsAt, v.Configuration.StartsAt, written)
	m.EndsAt = binding.value("EndsAt", prior.EndsAt, v.Configuration.EndsAt, written)
	m.UserID = types.StringValue(v.Configuration.UserID)
	return m, m.validate(false)
}

func scheduleDeclarationComparable(a, b string) bool {
	_, explicitA, errA := temporalParse(a)
	_, explicitB, errB := temporalParse(b)
	return errA == nil && errB == nil && explicitA == explicitB
}
