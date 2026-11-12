package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

var workCalendarCountryPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{1,15}$`)

func temporalConfirmRevision(committed, observed int64, diagnostics *diag.Diagnostics) bool {
	if committed == observed {
		return true
	}
	diagnostics.AddError("Concurrent calendar change", fmt.Sprintf("The write committed revision %d, but the subsequent source-projection read returned revision %d. The committed identity/revision remain in state; run a fresh plan before retrying.", committed, observed))
	return false
}

var workCalendarInstantPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,6})?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)?$`)

var workCalendarWeekdays = []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}
var workCalendarWindowTypes = map[string]attr.Type{"starts_at": types.StringType, "ends_at": types.StringType}
var workCalendarWindowType = types.ObjectType{AttrTypes: workCalendarWindowTypes}
var workCalendarWeeklyTypes = map[string]attr.Type{"reference_date": types.StringType, "intervals": types.SetType{ElemType: workCalendarWindowType}}
var workCalendarClockTypes = map[string]attr.Type{"reference_date": types.StringType, "windows": types.SetType{ElemType: workCalendarWindowType}}

type workCalendarWindow struct {
	StartsAt types.String `tfsdk:"starts_at"`
	EndsAt   types.String `tfsdk:"ends_at"`
}
type workCalendarModel struct {
	RequestTimezone           types.String `tfsdk:"request_timezone"`
	ID                        types.String `tfsdk:"id"`
	TeamID                    types.String `tfsdk:"team_id"`
	Name                      types.String `tfsdk:"name"`
	Revision                  types.Int64  `tfsdk:"revision"`
	AggregateRevision         types.Int64  `tfsdk:"aggregate_revision"`
	BaseSchedule              types.Object `tfsdk:"base_schedule"`
	WorkingWeekendSchedule    types.Object `tfsdk:"working_weekend_schedule"`
	InheritProductionCalendar types.Bool   `tfsdk:"inherit_production_calendar"`
	ProductionCountryCode     types.String `tfsdk:"production_country_code"`
}
type workCalendarEntryModel struct {
	RequestTimezone types.String `tfsdk:"request_timezone"`
	ID              types.String `tfsdk:"id"`
	TeamID          types.String `tfsdk:"team_id"`
	CalendarID      types.String `tfsdk:"calendar_id"`
	Revision        types.Int64  `tfsdk:"revision"`
	Name            types.String `tfsdk:"name"`
	Effect          types.String `tfsdk:"effect"`
	RuleType        types.String `tfsdk:"rule_type"`
	StartDate       types.String `tfsdk:"start_date"`
	EndDate         types.String `tfsdk:"end_date"`
	StartsAt        types.String `tfsdk:"starts_at"`
	EndsAt          types.String `tfsdk:"ends_at"`
	Weekdays        types.Set    `tfsdk:"weekdays"`
	Month           types.Int64  `tfsdk:"month"`
	Day             types.Int64  `tfsdk:"day"`
	Clock           types.Object `tfsdk:"clock"`
}

func workCalendarWindowsInput(ctx context.Context, value types.Object, key string, minute bool) (string, []client.WorkCalendarWindow, error) {
	if value.IsNull() || value.IsUnknown() {
		return "", nil, nil
	}
	fields := value.Attributes()
	reference, ok := fields["reference_date"].(types.String)
	if !ok || reference.IsNull() || reference.IsUnknown() {
		return "", nil, errors.New("reference_date must be explicitly known")
	}
	date, err := time.Parse(time.DateOnly, reference.ValueString())
	if err != nil || date.Year() < 1 {
		return "", nil, errors.New("reference_date must use YYYY-MM-DD")
	}
	collection, ok := fields[key].(types.Set)
	if !ok || collection.IsNull() || collection.IsUnknown() {
		return "", nil, fmt.Errorf("%s must be an explicit set; use [] to clear a weekly schedule", key)
	}
	var windows []workCalendarWindow
	if d := collection.ElementsAs(ctx, &windows, false); d.HasError() {
		return "", nil, errors.New("could not decode dated windows")
	}
	if len(windows) > 512 || key == "windows" && (len(windows) == 0 || len(windows) > 7) {
		return "", nil, errors.New("intervals allow at most 512 windows; recurring clocks require 1 through 7")
	}
	out := make([]client.WorkCalendarWindow, 0, len(windows))
	for _, window := range windows {
		start, explicitStart, startErr := temporalParse(window.StartsAt.ValueString())
		end, explicitEnd, endErr := temporalParse(window.EndsAt.ValueString())
		if startErr != nil || endErr != nil || !workCalendarInstantPattern.MatchString(window.StartsAt.ValueString()) || !workCalendarInstantPattern.MatchString(window.EndsAt.ValueString()) {
			return "", nil, errors.New("dated windows require local datetime or RFC3339 values, at most microsecond precision")
		}
		if minute && (start.Second() != 0 || end.Second() != 0 || start.Nanosecond() != 0 || end.Nanosecond() != 0) {
			return "", nil, errors.New("weekly schedule windows require exact minute precision")
		}
		if key == "windows" && (start.Nanosecond() != 0 || end.Nanosecond() != 0) {
			return "", nil, errors.New("recurring clock windows require whole-second precision")
		}
		if explicitStart == explicitEnd && !start.Before(end) {
			return "", nil, errors.New("window starts_at must precede ends_at")
		}
		out = append(out, client.WorkCalendarWindow{StartsAt: window.StartsAt.ValueString(), EndsAt: window.EndsAt.ValueString()})
	}
	slices.SortFunc(out, func(a, b client.WorkCalendarWindow) int {
		if n := strings.Compare(a.StartsAt, b.StartsAt); n != 0 {
			return n
		}
		return strings.Compare(a.EndsAt, b.EndsAt)
	})
	return reference.ValueString(), out, nil
}
func workCalendarWeeklyInput(ctx context.Context, value types.Object) (*client.WorkCalendarWeekly, error) {
	reference, windows, err := workCalendarWindowsInput(ctx, value, "intervals", true)
	if err != nil || windows == nil {
		return nil, err
	}
	if reference != client.WorkCalendarReferenceDate {
		return nil, errors.New("weekly reference_date must be the fixed UTC source Monday 2024-01-01")
	}
	return &client.WorkCalendarWeekly{ReferenceDate: reference, Intervals: windows}, nil
}
func workCalendarWindowsState(ctx context.Context, reference, key string, windows []client.WorkCalendarWindow) (types.Object, error) {
	attrs := workCalendarWeeklyTypes
	if key == "windows" {
		attrs = workCalendarClockTypes
	}
	values := make([]workCalendarWindow, 0, len(windows))
	for _, window := range windows {
		_, explicitStart, startErr := temporalParse(window.StartsAt)
		_, explicitEnd, endErr := temporalParse(window.EndsAt)
		if startErr != nil || endErr != nil || !explicitStart || !explicitEnd {
			return types.ObjectNull(attrs), errors.New("OnPager returned an invalid dated window")
		}
		values = append(values, workCalendarWindow{StartsAt: types.StringValue(window.StartsAt), EndsAt: types.StringValue(window.EndsAt)})
	}
	set, d := types.SetValueFrom(ctx, workCalendarWindowType, values)
	if d.HasError() {
		return types.ObjectNull(attrs), errors.New("could not store dated windows")
	}
	out := types.ObjectValueMust(attrs, map[string]attr.Value{"reference_date": types.StringValue(reference), key: set})
	if _, _, err := workCalendarWindowsInput(ctx, out, key, key == "intervals"); err != nil {
		return types.ObjectNull(attrs), err
	}
	return out, nil
}
func workCalendarWeeklyState(ctx context.Context, v client.WorkCalendarWeekly) (types.Object, error) {
	return workCalendarWindowsState(ctx, v.ReferenceDate, "intervals", v.Intervals)
}
func workCalendarCountryCodeError(value types.String) error {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	if !workCalendarCountryPattern.MatchString(value.ValueString()) {
		return errors.New("production_country_code must be a canonical code matching ^[A-Z][A-Z0-9_-]{1,15}$ with no surrounding whitespace")
	}
	return nil
}

func workCalendarInput(ctx context.Context, m workCalendarModel) (client.WorkCalendarInput, error) {
	if err := workCalendarCountryCodeError(m.ProductionCountryCode); err != nil {
		return client.WorkCalendarInput{}, err
	}
	base, err := workCalendarWeeklyInput(ctx, m.BaseSchedule)
	if err != nil {
		return client.WorkCalendarInput{}, err
	}
	weekend, err := workCalendarWeeklyInput(ctx, m.WorkingWeekendSchedule)
	if err != nil {
		return client.WorkCalendarInput{}, err
	}
	return client.WorkCalendarInput{Name: m.Name.ValueString(), BaseSchedule: base, WorkingWeekendSchedule: weekend, InheritProductionCalendar: m.InheritProductionCalendar.ValueBool(), ProductionCountryCode: m.ProductionCountryCode.ValueString()}, nil
}
func workCalendarState(ctx context.Context, v client.WorkCalendar) (workCalendarModel, error) {
	out := workCalendarModel{ID: types.StringValue(v.ID), TeamID: types.StringValue(v.TeamID), Name: types.StringValue(v.Name), Revision: types.Int64Value(v.Revision), AggregateRevision: types.Int64Value(v.AggregateRevision), InheritProductionCalendar: types.BoolValue(v.InheritProductionCalendar), ProductionCountryCode: types.StringValue(v.ProductionCountryCode)}
	var err error
	out.BaseSchedule, err = workCalendarWeeklyState(ctx, *v.BaseSchedule)
	if err != nil {
		return out, err
	}
	out.WorkingWeekendSchedule, err = workCalendarWeeklyState(ctx, *v.WorkingWeekendSchedule)
	return out, err
}
func workCalendarNullableString(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
func workCalendarNullableInt(v int64) types.Int64 {
	if v == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(v)
}
func workCalendarEquivalentInstant(prior types.String, actual string) types.String {
	a, ea := time.Parse(time.RFC3339Nano, prior.ValueString())
	b, eb := time.Parse(time.RFC3339Nano, actual)
	if ea == nil && eb == nil && a.Equal(b) {
		return prior
	}
	return workCalendarNullableString(actual)
}
func workCalendarEntryInput(ctx context.Context, m workCalendarEntryModel) (client.WorkCalendarEntryInput, error) {
	in := client.WorkCalendarEntryInput{Name: m.Name.ValueString(), Effect: m.Effect.ValueString(), RuleType: m.RuleType.ValueString(), StartDate: m.StartDate.ValueString(), EndDate: m.EndDate.ValueString(), StartsAt: m.StartsAt.ValueString(), EndsAt: m.EndsAt.ValueString(), Month: m.Month.ValueInt64(), Day: m.Day.ValueInt64()}
	if !m.Weekdays.IsNull() && !m.Weekdays.IsUnknown() {
		if d := m.Weekdays.ElementsAs(ctx, &in.Weekdays, false); d.HasError() {
			return in, errors.New("could not decode weekdays")
		}
		slices.SortFunc(in.Weekdays, func(a, b string) int {
			return slices.Index(workCalendarWeekdays, a) - slices.Index(workCalendarWeekdays, b)
		})
	}
	allowed := map[string]bool{}
	required := []string{}
	switch in.RuleType {
	case "date":
		required = []string{"start_date"}
	case "date_range":
		required = []string{"start_date", "end_date"}
	case "datetime_interval":
		required = []string{"starts_at", "ends_at"}
	case "weekly":
		required = []string{"weekdays"}
		allowed["start_date"] = true
		allowed["end_date"] = true
		allowed["clock"] = true
	case "yearly":
		required = []string{"month", "day"}
		allowed["start_date"] = true
		allowed["end_date"] = true
		allowed["clock"] = true
	default:
		return in, errors.New("unsupported calendar entry rule_type")
	}
	for _, key := range required {
		allowed[key] = true
	}
	supplied := map[string]bool{"start_date": !m.StartDate.IsNull() && !m.StartDate.IsUnknown(), "end_date": !m.EndDate.IsNull() && !m.EndDate.IsUnknown(), "starts_at": !m.StartsAt.IsNull() && !m.StartsAt.IsUnknown(), "ends_at": !m.EndsAt.IsNull() && !m.EndsAt.IsUnknown(), "weekdays": !m.Weekdays.IsNull() && !m.Weekdays.IsUnknown(), "month": !m.Month.IsNull() && !m.Month.IsUnknown(), "day": !m.Day.IsNull() && !m.Day.IsUnknown(), "clock": !m.Clock.IsNull() && !m.Clock.IsUnknown()}
	for key, exists := range supplied {
		if exists && !allowed[key] {
			return in, fmt.Errorf("%s is unsupported for rule_type %s", key, in.RuleType)
		}
	}
	for _, key := range required {
		if !supplied[key] {
			return in, fmt.Errorf("%s is required for rule_type %s", key, in.RuleType)
		}
	}
	for key, value := range map[string]string{"start_date": in.StartDate, "end_date": in.EndDate, "starts_at": in.StartsAt, "ends_at": in.EndsAt} {
		if supplied[key] && value == "" {
			return in, fmt.Errorf("%s must not be empty", key)
		}
	}
	for _, v := range []string{in.StartDate, in.EndDate} {
		if v != "" {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return in, errors.New("source dates must use YYYY-MM-DD")
			}
		}
	}
	if in.StartDate != "" && in.EndDate != "" && in.EndDate < in.StartDate {
		return in, errors.New("end_date must not precede start_date")
	}
	for _, v := range []string{in.StartsAt, in.EndsAt} {
		if v != "" {
			if _, _, err := temporalParse(v); err != nil || !workCalendarInstantPattern.MatchString(v) {
				return in, errors.New("timestamps must use YYYY-MM-DDTHH:mm:ss with optional RFC3339 offset and at most microsecond precision")
			}
		}
	}
	if in.StartsAt != "" && in.EndsAt != "" {
		a, explicitA, _ := temporalParse(in.StartsAt)
		b, explicitB, _ := temporalParse(in.EndsAt)
		if explicitA == explicitB && !a.Before(b) {
			return in, errors.New("starts_at must be before ends_at")
		}
	}
	if in.RuleType == "weekly" && len(in.Weekdays) == 0 {
		return in, errors.New("weekly entries require at least one weekday")
	}
	for _, day := range in.Weekdays {
		if !slices.Contains(workCalendarWeekdays, day) {
			return in, errors.New("invalid weekday")
		}
	}
	if in.RuleType == "yearly" {
		date := time.Date(2024, time.Month(in.Month), int(in.Day), 0, 0, 0, 0, time.UTC)
		if int64(date.Month()) != in.Month || int64(date.Day()) != in.Day {
			return in, errors.New("invalid yearly month/day")
		}
	}
	if supplied["clock"] {
		reference, windows, err := workCalendarWindowsInput(ctx, m.Clock, "windows", false)
		if err != nil {
			return in, err
		}
		expected := client.WorkCalendarReferenceDate
		if in.RuleType == "yearly" {
			expected = fmt.Sprintf("2024-%02d-%02d", in.Month, in.Day)
		}
		if reference != expected {
			return in, fmt.Errorf("clock reference_date must be the fixed source date %s", expected)
		}
		in.Clock = &client.WorkCalendarClock{ReferenceDate: reference, Windows: windows}
	}

	return in, nil
}
func workCalendarEntryState(ctx context.Context, team string, v client.WorkCalendarEntry) (workCalendarEntryModel, error) {
	out := workCalendarEntryModel{ID: types.StringValue(v.ID), TeamID: types.StringValue(team), CalendarID: types.StringValue(v.CalendarID), Revision: types.Int64Value(v.Revision), Name: types.StringValue(v.Name), Effect: types.StringValue(v.Effect), RuleType: types.StringValue(v.RuleType), StartDate: workCalendarNullableString(v.StartDate), EndDate: workCalendarNullableString(v.EndDate), StartsAt: workCalendarNullableString(v.StartsAt), EndsAt: workCalendarNullableString(v.EndsAt), Month: workCalendarNullableInt(v.Month), Day: workCalendarNullableInt(v.Day), Weekdays: types.SetNull(types.StringType), Clock: types.ObjectNull(workCalendarClockTypes)}
	if len(v.Weekdays) > 0 {
		value, d := types.SetValueFrom(ctx, types.StringType, v.Weekdays)
		if d.HasError() {
			return out, errors.New("could not store weekdays")
		}
		out.Weekdays = value
	}
	if v.Clock != nil {
		var err error
		out.Clock, err = workCalendarWindowsState(ctx, v.Clock.ReferenceDate, "windows", v.Clock.Windows)
		if err != nil {
			return out, err
		}
	}

	return out, nil
}

// temporalParse validates syntax without resolving a local time in any zone.
// A parsed local value is only a civil-clock comparison value, never an instant.
func temporalParse(raw string) (time.Time, bool, error) {
	if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return at, true, nil
	}
	at, err := time.Parse("2006-01-02T15:04:05.999999999", raw)
	return at, false, err
}

type temporalBinding struct {
	Input    string `json:"input"`
	Response string `json:"response"`
}
type temporalBindings map[string]temporalBinding

const temporalPrivateKey = "temporal_bindings"

type temporalPrivateReader interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}
type temporalPrivateWriter interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}

func temporalLoad(ctx context.Context, data temporalPrivateReader, d *diag.Diagnostics) temporalBindings {
	out := temporalBindings{}
	raw, diagnostics := data.GetKey(ctx, temporalPrivateKey)
	d.Append(diagnostics...)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			d.AddError("Invalid temporal private state", "Could not read the saved server timestamp bindings.")
		}
	}
	return out
}
func (b temporalBindings) store(ctx context.Context, data temporalPrivateWriter, d *diag.Diagnostics) {
	raw, err := json.Marshal(b)
	if err != nil {
		d.AddError("Invalid temporal private state", "Could not store the server timestamp bindings.")
		return
	}
	d.Append(data.SetKey(ctx, temporalPrivateKey, raw)...)
}

// value preserves local configuration by its previous authoritative API instant.
// Only a just-written input may establish a new binding by matching the server's
// returned local clock. Refresh never guesses an offset from the selected zone.
func (b temporalBindings) value(key string, prior types.String, actual string, written bool) types.String {
	out := workCalendarEquivalentInstant(prior, actual)
	input, explicit, err := temporalParse(prior.ValueString())
	response, responseExplicit, responseErr := temporalParse(actual)
	if err == nil && !explicit && responseErr == nil && responseExplicit {
		old, exists := b[key]
		oldAt, oldErr := time.Parse(time.RFC3339Nano, old.Response)
		same := exists && old.Input == prior.ValueString() && oldErr == nil && oldAt.Equal(response)
		if written {
			same = input.Year() == response.Year() && input.Month() == response.Month() && input.Day() == response.Day() && input.Hour() == response.Hour() && input.Minute() == response.Minute() && input.Second() == response.Second() && input.Nanosecond() == response.Nanosecond()
		}
		if same {
			out = prior
		}
	}
	if actual == "" {
		delete(b, key)
	} else {
		b[key] = temporalBinding{Input: out.ValueString(), Response: actual}
	}
	return out
}

func temporalTimezoneAttribute() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: "Explicit provider request timezone last applied to local datetime inputs. Null when the provider timezone is omitted or all inputs have explicit offsets. Changing the explicit provider timezone plans an update for local inputs; refresh alone preserves saved instants. The backend interprets local clocks and reports DST ambiguity or nonexistent times."}
}
func temporalRequestTimezone(zone string, values ...attr.Value) types.String {
	if zone != "" {
		for _, value := range values {
			if temporalUnknown(value) {
				return types.StringUnknown()
			}
			if temporalContainsLocal(value) {
				return types.StringValue(zone)
			}
		}
	}
	return types.StringNull()
}
func temporalContainsLocal(value attr.Value) bool {
	if value == nil || value.IsNull() || value.IsUnknown() {
		return false
	}
	switch v := value.(type) {
	case types.String:
		_, explicit, err := temporalParse(v.ValueString())
		return err == nil && !explicit
	case types.Object:
		for _, child := range v.Attributes() {
			if temporalContainsLocal(child) {
				return true
			}
		}
	case types.Set:
		for _, child := range v.Elements() {
			if temporalContainsLocal(child) {
				return true
			}
		}
	case types.List:
		for _, child := range v.Elements() {
			if temporalContainsLocal(child) {
				return true
			}
		}
	}
	return false
}

type temporalWindowSnapshot struct {
	Reference string                      `json:"reference"`
	Windows   []client.WorkCalendarWindow `json:"windows"`
}

func temporalSnapshot(ctx context.Context, value types.Object, key string) (temporalWindowSnapshot, bool) {
	ref, windows, err := workCalendarWindowsInput(ctx, value, key, key == "intervals")
	return temporalWindowSnapshot{Reference: ref, Windows: windows}, err == nil && windows != nil
}
func temporalWindowUnion(windows []client.WorkCalendarWindow) ([][2]time.Time, bool) {
	intervals := make([][2]time.Time, 0, len(windows))
	for _, w := range windows {
		a, ea := time.Parse(time.RFC3339Nano, w.StartsAt)
		b, eb := time.Parse(time.RFC3339Nano, w.EndsAt)
		if ea != nil || eb != nil || !a.Before(b) {
			return nil, false
		}
		intervals = append(intervals, [2]time.Time{a, b})
	}
	slices.SortFunc(intervals, func(a, b [2]time.Time) int { return a[0].Compare(b[0]) })
	merged := [][2]time.Time{}
	for _, v := range intervals {
		last := len(merged) - 1
		if last >= 0 && !v[0].After(merged[last][1]) {
			if v[1].After(merged[last][1]) {
				merged[last][1] = v[1]
			}
		} else {
			merged = append(merged, v)
		}
	}
	return merged, true
}
func temporalWindowsEquivalent(a, b temporalWindowSnapshot) bool {
	if a.Reference != b.Reference {
		return false
	}
	x, validX := temporalWindowUnion(a.Windows)
	y, validY := temporalWindowUnion(b.Windows)
	if !validX || !validY || len(x) != len(y) {
		return false
	}
	for i := range x {
		if !x[i][0].Equal(y[i][0]) || !x[i][1].Equal(y[i][1]) {
			return false
		}
	}
	return true
}
func (b temporalBindings) windows(ctx context.Context, key string, prior, actual types.Object, windowKey string, written bool) types.Object {
	wanted, hasWanted := temporalSnapshot(ctx, prior, windowKey)
	got, hasGot := temporalSnapshot(ctx, actual, windowKey)
	if !hasGot {
		delete(b, key)
		return actual
	}
	wantedJSON, _ := json.Marshal(wanted)
	actualJSON, _ := json.Marshal(got)
	preserve := hasWanted && temporalWindowsEquivalent(wanted, got)
	if hasWanted && temporalContainsLocal(prior) {
		old, exists := b[key]
		var saved temporalWindowSnapshot
		if exists && old.Input == string(wantedJSON) && json.Unmarshal([]byte(old.Response), &saved) == nil && temporalWindowsEquivalent(saved, got) {
			preserve = true
		}
		// A successful mutation is the authority for resolving local inputs. The
		// provider records that exact response and never calculates its timezone.
		if written && wanted.Reference == got.Reference {
			preserve = true
		}
	}
	next := actual
	if preserve {
		next = prior
	} else {
		wantedJSON = actualJSON
	}
	b[key] = temporalBinding{Input: string(wantedJSON), Response: string(actualJSON)}
	return next
}

func workCalendarReconcileState(ctx context.Context, prior, next workCalendarModel, b temporalBindings, written bool) workCalendarModel {
	next.BaseSchedule = b.windows(ctx, "base_schedule", prior.BaseSchedule, next.BaseSchedule, "intervals", written)
	next.WorkingWeekendSchedule = b.windows(ctx, "working_weekend_schedule", prior.WorkingWeekendSchedule, next.WorkingWeekendSchedule, "intervals", written)
	next.RequestTimezone = prior.RequestTimezone
	return next
}
func workCalendarEntryReconcileState(ctx context.Context, prior, next workCalendarEntryModel, b temporalBindings, written bool) workCalendarEntryModel {
	next.StartsAt = b.value("starts_at", prior.StartsAt, next.StartsAt.ValueString(), written)
	next.EndsAt = b.value("ends_at", prior.EndsAt, next.EndsAt.ValueString(), written)
	next.Clock = b.windows(ctx, "clock", prior.Clock, next.Clock, "windows", written)
	next.RequestTimezone = prior.RequestTimezone
	return next
}
func workCalendarClockProjectionMatches(ctx context.Context, value types.Object, actual *client.WorkCalendarClock) bool {
	prior, ok := temporalSnapshot(ctx, value, "windows")
	return ok && actual.ReferenceDate == prior.Reference
}

func temporalUnknown(value attr.Value) bool {
	if value == nil || value.IsNull() {
		return false
	}
	if value.IsUnknown() {
		return true
	}
	switch v := value.(type) {
	case types.Object:
		for _, child := range v.Attributes() {
			if temporalUnknown(child) {
				return true
			}
		}
	case types.Set:
		for _, child := range v.Elements() {
			if temporalUnknown(child) {
				return true
			}
		}
	case types.List:
		for _, child := range v.Elements() {
			if temporalUnknown(child) {
				return true
			}
		}
	}
	return false
}
