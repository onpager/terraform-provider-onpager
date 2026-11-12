package provider

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	"testing"
)

func testCalendarWindowObject(reference, key string, windows ...client.WorkCalendarWindow) types.Object {
	values := []attr.Value{}
	for _, w := range windows {
		values = append(values, types.ObjectValueMust(map[string]attr.Type{"starts_at": types.StringType, "ends_at": types.StringType}, map[string]attr.Value{"starts_at": types.StringValue(w.StartsAt), "ends_at": types.StringValue(w.EndsAt)}))
	}
	fields := map[string]attr.Type{"reference_date": types.StringType, key: types.SetType{ElemType: types.ObjectType{AttrTypes: map[string]attr.Type{"starts_at": types.StringType, "ends_at": types.StringType}}}}
	return types.ObjectValueMust(fields, map[string]attr.Value{"reference_date": types.StringValue(reference), key: types.SetValueMust(types.ObjectType{AttrTypes: map[string]attr.Type{"starts_at": types.StringType, "ends_at": types.StringType}}, values)})
}
func TestWorkCalendarWeeklyDatedLocalInputAndEmpty(t *testing.T) {
	value := testCalendarWindowObject("2024-01-01", "intervals", client.WorkCalendarWindow{StartsAt: "2024-01-01T09:00:00", EndsAt: "2024-01-01T17:00:00"})
	input, err := workCalendarWeeklyInput(t.Context(), value)
	if err != nil || input.Intervals[0].StartsAt != "2024-01-01T09:00:00" || input.ReferenceDate != "2024-01-01" {
		t.Fatalf("local calendar input rewritten: %+v %v", input, err)
	}
	empty := testCalendarWindowObject("2024-01-01", "intervals")
	input, err = workCalendarWeeklyInput(t.Context(), empty)
	if err != nil || input.Intervals == nil || len(input.Intervals) != 0 {
		t.Fatal("explicit empty intervals must be []", input, err)
	}
}
func TestWorkCalendarWeeklyResponsePreservesDatedView(t *testing.T) {
	response := client.WorkCalendarWeekly{ReferenceDate: "2024-01-01", Intervals: []client.WorkCalendarWindow{{StartsAt: "2024-01-08T00:00:00+03:00", EndsAt: "2024-01-08T03:00:00+03:00"}}}
	value, err := workCalendarWeeklyState(t.Context(), response)
	if err != nil {
		t.Fatal(err)
	}
	expected := testCalendarWindowObject("2024-01-01", "intervals", client.WorkCalendarWindow{StartsAt: "2024-01-08T00:00:00+03:00", EndsAt: "2024-01-08T03:00:00+03:00"})
	if !value.Equal(expected) {
		t.Fatal("backend dated view was converted", value)
	}
}
func TestWorkCalendarClockInputPreservesSourceSelectors(t *testing.T) {
	clock := testCalendarWindowObject("2024-01-01", "windows", client.WorkCalendarWindow{StartsAt: "2024-01-08T01:00:00", EndsAt: "2024-01-08T02:00:00"})
	m := workCalendarEntryModel{Name: types.StringValue("Sunday source"), Effect: types.StringValue("working"), RuleType: types.StringValue("weekly"), Weekdays: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("SU")}), Clock: clock}
	input, err := workCalendarEntryInput(t.Context(), m)
	if err != nil || input.Weekdays[0] != "SU" || input.Clock.Windows[0].StartsAt != "2024-01-08T01:00:00" {
		t.Fatalf("source weekday or local date reinterpreted: %+v %v", input, err)
	}
}
func TestWorkCalendarEntryRejectsZeroAndIncompatibleFields(t *testing.T) {
	ctx := context.Background()
	m := workCalendarEntryModel{Name: types.StringValue("Leap"), Effect: types.StringValue("working"), RuleType: types.StringValue("yearly"), Month: types.Int64Value(2), Day: types.Int64Value(29)}
	if _, err := workCalendarEntryInput(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Day = types.Int64Value(0)
	if _, err := workCalendarEntryInput(ctx, m); err == nil {
		t.Fatal("explicit zero day accepted")
	}
	m.Day = types.Int64Value(29)
	m.Weekdays = types.SetValueMust(types.StringType, []attr.Value{})
	if _, err := workCalendarEntryInput(ctx, m); err == nil {
		t.Fatal("incompatible explicit empty weekdays accepted")
	}
}
func TestWorkCalendarEquivalentInstantPreservesOffsetAndPrecision(t *testing.T) {
	old := types.StringValue("2026-12-01T05:00:00.123456+05:00")
	got := workCalendarEquivalentInstant(old, "2026-12-01T00:00:00.123456Z")
	if !old.Equal(got) {
		t.Fatal("equivalent instant changed configured representation")
	}
}

func TestWorkCalendarEntryRejectsExplicitEmptyRequiredDate(t *testing.T) {
	m := workCalendarEntryModel{Name: types.StringValue("Empty"), Effect: types.StringValue("working"), RuleType: types.StringValue("date"), StartDate: types.StringValue("")}
	if _, err := workCalendarEntryInput(context.Background(), m); err == nil {
		t.Fatal("explicit empty required date treated as omission")
	}
}

func TestWorkCalendarEntryRejectsSubMicrosecondInstants(t *testing.T) {
	m := workCalendarEntryModel{Name: types.StringValue("Precision"), Effect: types.StringValue("working"), RuleType: types.StringValue("datetime_interval"), StartsAt: types.StringValue("2026-12-01T00:00:00.1234567Z"), EndsAt: types.StringValue("2026-12-01T00:00:01Z")}
	if _, err := workCalendarEntryInput(context.Background(), m); err == nil {
		t.Fatal("unsupported precision accepted")
	}
}

func TestWorkCalendarClockRejectsInvalidDatedWindows(t *testing.T) {
	m := workCalendarEntryModel{Name: types.StringValue("Invalid"), Effect: types.StringValue("working"), RuleType: types.StringValue("yearly"), Month: types.Int64Value(2), Day: types.Int64Value(29), Clock: testCalendarWindowObject("2024-02-29", "windows", client.WorkCalendarWindow{StartsAt: "2024-02-29T09:00:00", EndsAt: "2024-02-29T08:00:00"})}
	if _, err := workCalendarEntryInput(t.Context(), m); err == nil {
		t.Fatal("backwards clock accepted")
	}
}
func TestWorkCalendarReadRejectsLossyPrecision(t *testing.T) {
	if _, err := workCalendarWeeklyState(context.Background(), client.WorkCalendarWeekly{ReferenceDate: "2024-01-01", Intervals: []client.WorkCalendarWindow{{StartsAt: "2024-01-01T00:00:01Z", EndsAt: "2024-01-01T01:00:00Z"}}}); err == nil {
		t.Fatal("weekly seconds silently truncated")
	}
}

// Local values must reach the backend byte-for-byte; DST resolution belongs to it.
func TestWorkCalendarLocalDatetimeInputPreserved(t *testing.T) {
	m := workCalendarEntryModel{Name: types.StringValue("Local"), Effect: types.StringValue("working"), RuleType: types.StringValue("datetime_interval"), StartsAt: types.StringValue("2027-01-04T09:00:00.123456"), EndsAt: types.StringValue("2027-01-04T10:00:00.123456")}
	in, err := workCalendarEntryInput(t.Context(), m)
	if err != nil || in.StartsAt != "2027-01-04T09:00:00.123456" || in.EndsAt != "2027-01-04T10:00:00.123456" {
		t.Fatalf("local input was rejected or rewritten: %+v %v", in, err)
	}
}

func TestWorkCalendarDatedViewBindingKeepsFragmentsAndDetectsDrift(t *testing.T) {
	prior := testCalendarWindowObject("2024-01-01", "intervals", client.WorkCalendarWindow{StartsAt: "2024-01-07T22:00:00", EndsAt: "2024-01-08T02:00:00"})
	first := testCalendarWindowObject("2024-01-01", "intervals", client.WorkCalendarWindow{StartsAt: "2024-01-07T22:00:00+03:00", EndsAt: "2024-01-08T00:00:00+03:00"}, client.WorkCalendarWindow{StartsAt: "2024-01-08T00:00:00+03:00", EndsAt: "2024-01-08T02:00:00+03:00"})
	binding := temporalBindings{}
	got := binding.windows(t.Context(), "base", prior, first, "intervals", true)
	if !got.Equal(prior) {
		t.Fatal("local declaration changed after publication", got)
	}
	utc := testCalendarWindowObject("2024-01-01", "intervals", client.WorkCalendarWindow{StartsAt: "2024-01-07T19:00:00Z", EndsAt: "2024-01-07T23:00:00Z"})
	got = binding.windows(t.Context(), "base", got, utc, "intervals", false)
	if !got.Equal(prior) {
		t.Fatal("request timezone or fragment representation changed input", got)
	}
	changed := testCalendarWindowObject("2024-01-01", "intervals", client.WorkCalendarWindow{StartsAt: "2024-01-07T19:00:00Z", EndsAt: "2024-01-07T22:00:00Z"})
	got = binding.windows(t.Context(), "base", got, changed, "intervals", false)
	if !got.Equal(changed) {
		t.Fatal("real calendar drift was hidden", got)
	}
}

func TestTemporalRequestTimezonePlansOnlyLocalInputs(t *testing.T) {
	for _, tc := range []struct {
		name, zone string
		value      attr.Value
		want       types.String
	}{
		{"local", "Europe/Moscow", types.StringValue("2027-01-04T09:00:00"), types.StringValue("Europe/Moscow")},
		{"absolute", "Europe/Moscow", types.StringValue("2027-01-04T09:00:00+03:00"), types.StringNull()},
		{"omitted_timezone", "", types.StringValue("2027-01-04T09:00:00"), types.StringNull()},
		{"unknown_input", "Europe/Moscow", types.StringUnknown(), types.StringUnknown()},
		{"source_date", "Europe/Moscow", types.StringValue("2024-01-01"), types.StringNull()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := temporalRequestTimezone(tc.zone, tc.value); !got.Equal(tc.want) {
				t.Fatalf("unexpected timezone plan: %v", got)
			}
		})
	}
}
