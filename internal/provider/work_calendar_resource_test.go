package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

var calendarWindowTypes = map[string]attr.Type{"starts_at": types.StringType, "ends_at": types.StringType}
var calendarWindowType = types.ObjectType{AttrTypes: calendarWindowTypes}
var calendarWeeklyTypes = map[string]attr.Type{"reference_date": types.StringType, "intervals": types.SetType{ElemType: calendarWindowType}}
var calendarClockTypes = map[string]attr.Type{"reference_date": types.StringType, "windows": types.SetType{ElemType: calendarWindowType}}

func calendarDatedValue(key, reference string, windows ...[2]string) types.Object {
	fields := calendarWeeklyTypes
	if key == "windows" {
		fields = calendarClockTypes
	}
	values := []attr.Value{}
	for _, w := range windows {
		values = append(values, types.ObjectValueMust(calendarWindowTypes, map[string]attr.Value{"starts_at": types.StringValue(w[0]), "ends_at": types.StringValue(w[1])}))
	}
	return types.ObjectValueMust(fields, map[string]attr.Value{"reference_date": types.StringValue(reference), key: types.SetValueMust(calendarWindowType, values)})
}
func workCalendarValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fService), "team_id": types.StringValue(fTeam), "name": types.StringValue("Work"), "revision": types.Int64Value(7), "aggregate_revision": types.Int64Value(99), "request_timezone": types.StringNull(), "base_schedule": calendarDatedValue("intervals", "2024-01-01"), "working_weekend_schedule": calendarDatedValue("intervals", "2024-01-01"), "inherit_production_calendar": types.BoolValue(false), "production_country_code": types.StringValue("RU")}
}
func TestWorkCalendarReadAndArchiveFailureState(t *testing.T) {
	for _, status := range []int{403, 404, 409, 412, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewWorkCalendarResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/v1/teams/"+fTeam+"/work-calendars/"+fService {
					t.Fatal("unexpected collection read")
				}
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprintf(w, `{"id":%q,"team_id":%q,"name":"Work","revision":7,"aggregate_revision":99,"base_schedule":{"reference_date":"2024-01-01","intervals":[]},"working_weekend_schedule":{"reference_date":"2024-01-01","intervals":[]},"production_country_code":"RU"}`, fService, fTeam)
				} else {
					fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
				}
			})
			state := valueState(t, s, workCalendarValues())
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if status == 404 {
				if !resp.State.Raw.IsNull() {
					t.Fatal("confirmed absence retained")
				}
				return
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("failure removed state")
			}
			if (status != 200) != resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
		})
	}
}
func TestWorkCalendarRetiredDefinitionIsLogicalAbsence(t *testing.T) {
	r := onpager.NewWorkCalendarResource()
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, `{"id":%q,"team_id":%q,"name":"Work","revision":8,"aggregate_revision":100,"retired_at":"2026-11-04T00:00:00Z","base_schedule":{"intervals":[]},"working_weekend_schedule":{"intervals":[]},"production_country_code":"RU"}`, fService, fTeam)
	})
	state := valueState(t, s, workCalendarValues())
	resp := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
	if !resp.State.Raw.IsNull() || resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
}
func TestWorkCalendarDeleteConflictRetainsStateAndUsesOwnRevision(t *testing.T) {
	for _, status := range []int{403, 409, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewWorkCalendarResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "DELETE" || req.Header.Get("If-Match") != `"7"` {
					t.Fatal("wrong deletion/version")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"calendar_in_use"}}`)
			})
			state := valueState(t, s, workCalendarValues())
			resp := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || calls != 1 {
				t.Fatal("failed archive abandoned or retried")
			}
		})
	}
}
func TestWorkCalendarCompositeImport(t *testing.T) {
	for _, entry := range []bool{false, true} {
		var r resource.Resource = onpager.NewWorkCalendarResource()
		id := fTeam + "/" + fService
		if entry {
			r = onpager.NewWorkCalendarEntryResource()
			id += "/" + fOtherTeam
		}
		var sr resource.SchemaResponse
		r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
		resp := resource.ImportStateResponse{}
		resp.State.Schema = sr.Schema
		resp.State.Raw = tftypes.NewValue(sr.Schema.Type().TerraformType(t.Context()), nil)
		r.(resource.ResourceWithImportState).ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		var got types.String
		resp.State.GetAttribute(t.Context(), path.Root("id"), &got)
		if !strings.HasSuffix(id, got.ValueString()) {
			t.Fatal("wrong UUID import")
		}
	}
}

func workCalendarEntryValues() map[string]attr.Value {
	return map[string]attr.Value{"id": types.StringValue(fOtherTeam), "team_id": types.StringValue(fTeam), "calendar_id": types.StringValue(fService), "revision": types.Int64Value(13), "request_timezone": types.StringNull(), "name": types.StringValue("Exception"), "effect": types.StringValue("working"), "rule_type": types.StringValue("date"), "start_date": types.StringValue("2027-05-01"), "end_date": types.StringNull(), "starts_at": types.StringNull(), "ends_at": types.StringNull(), "weekdays": types.SetNull(types.StringType), "month": types.Int64Null(), "day": types.Int64Null(), "clock": types.ObjectNull(calendarClockTypes)}
}
func TestWorkCalendarEntryReadErrorsAndOwnDeleteRevision(t *testing.T) {
	for _, status := range []int{403, 404, 409, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewWorkCalendarEntryResource()
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/v1/teams/"+fTeam+"/work-calendars/"+fService+"/entries/"+fOtherTeam {
					t.Fatal("not direct scoped entry URL")
				}
				if req.Method == "DELETE" && req.Header.Get("If-Match") != `"13"` {
					t.Fatal("entry deletion used parent revision")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			})
			state := valueState(t, s, workCalendarEntryValues())
			read := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &read)
			if (status == 404) != read.State.Raw.IsNull() {
				t.Fatal("only confirmed absence may remove entry")
			}
			if (status != 404) != read.Diagnostics.HasError() {
				t.Fatal(read.Diagnostics)
			}
			deleted := resource.DeleteResponse{State: state}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &deleted)
			if (status != 404) != deleted.Diagnostics.HasError() {
				t.Fatal(deleted.Diagnostics)
			}
		})
	}
}

func TestWorkCalendarConfigValidatesBeforeRemoteWrites(t *testing.T) {
	r := onpager.NewWorkCalendarResource()
	v, ok := r.(resource.ResourceWithValidateConfig)
	if !ok {
		t.Fatal("calendar must validate typed configuration before remote writes")
	}
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	values := workCalendarValues()
	values["base_schedule"] = calendarDatedValue("intervals", "2024-01-01", [2]string{"2024-01-01T17:00:00Z", "2024-01-01T09:00:00Z"})
	state := valueState(t, sr.Schema, values)
	var resp resource.ValidateConfigResponse
	v.ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("backwards interval accepted")
	}
}

func TestWorkCalendarEntryUpdateFailureRetainsState(t *testing.T) {
	for _, status := range []int{403, 409, 412} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := onpager.NewWorkCalendarEntryResource()
			calls := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "PUT" || req.Header.Get("If-Match") != `"13"` {
					t.Fatal("wrong entry replace/revision")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"revision_mismatch"}}`)
			})
			state := valueState(t, s, workCalendarEntryValues())
			values := workCalendarEntryValues()
			values["name"] = types.StringValue("Changed")
			plan := valueState(t, s, values)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() || calls != 1 {
				t.Fatal("failed replace retried or removed state")
			}
			var name types.String
			resp.State.GetAttribute(t.Context(), path.Root("name"), &name)
			if name.ValueString() != "Exception" {
				t.Fatal("failed update saved desired values")
			}
		})
	}
}

func TestWorkCalendarCountryConfigurationRequiresCanonicalCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value types.String
		valid bool
	}{
		{"lowercase", types.StringValue("ru"), false},
		{"padded", types.StringValue(" RU "), false},
		{"empty", types.StringValue(""), false},
		{"single character", types.StringValue("R"), false},
		{"too long", types.StringValue("ABCDEFGHIJKLMNOPQ"), false},
		{"punctuation", types.StringValue("RU!"), false},
		{"digit prefix", types.StringValue("1RU"), false},
		{"builtin", types.StringValue("RU"), true},
		{"custom", types.StringValue("CUSTOM_1-REGION"), true},
		{"omitted default", types.StringNull(), true},
		{"unknown default", types.StringUnknown(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onpager.NewWorkCalendarResource()
			var sr resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
			values := workCalendarValues()
			values["production_country_code"] = tc.value
			state := valueState(t, sr.Schema, values)
			var resp resource.ValidateConfigResponse
			r.(resource.ResourceWithValidateConfig).ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: state.Raw}}, &resp)
			if tc.valid == resp.Diagnostics.HasError() {
				t.Fatalf("canonical country validity %t: %v", tc.valid, resp.Diagnostics)
			}
		})
	}
}

func TestWorkCalendarRejectsNoncanonicalCountryBeforeWrites(t *testing.T) {
	for _, code := range []string{"ru", " RU ", ""} {
		t.Run(fmt.Sprintf("%q", code), func(t *testing.T) {
			r := onpager.NewWorkCalendarResource()
			writes := 0
			s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) { writes++; w.WriteHeader(500) })
			values := workCalendarValues()
			values["production_country_code"] = types.StringValue(code)
			state := valueState(t, s, values)
			create := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &create)
			update := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: s, Raw: state.Raw}}, &update)
			if !create.Diagnostics.HasError() || !update.Diagnostics.HasError() || writes != 0 {
				t.Fatalf("noncanonical country reached HTTP: writes=%d create=%v update=%v", writes, create.Diagnostics, update.Diagnostics)
			}
		})
	}
}

func temporalTestPrivate(response any) {
	field := reflect.ValueOf(response).Elem().FieldByName("Private")
	field.Set(reflect.New(field.Type().Elem()))
}
func TestWorkCalendarLocalLifecycleAndTimezonePlan(t *testing.T) {
	r := onpager.NewWorkCalendarResource()
	zone := "Europe/Moscow"
	actual := `{"reference_date":"2024-01-01","intervals":[{"starts_at":"2024-01-01T09:00:00+03:00","ends_at":"2024-01-01T17:00:00+03:00"}]}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if len(req.Header.Values("Onpager-Timezone")) != 1 || req.Header.Get("Onpager-Timezone") != zone {
			t.Error("request lost selected timezone")
		}
		if req.Method == "POST" {
			var body struct {
				BaseSchedule client.WorkCalendarWeekly `json:"base_schedule"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.BaseSchedule.Intervals[0].StartsAt != "2024-01-01T09:00:00" {
				t.Fatal("provider converted local clock")
			}
			fmt.Fprintf(w, `{"id":%q,"team_id":%q,"name":"Work","revision":7,"aggregate_revision":99,"base_schedule":{"reference_date":"2026-11-02","intervals":[]},"working_weekend_schedule":{"reference_date":"2026-11-02","intervals":[]},"production_country_code":"RU"}`, fService, fTeam)
			return
		}
		if req.Method != "GET" || req.URL.Query().Get("reference_date") != "2024-01-01" {
			t.Fatal("read did not pin the explicit source week")
		}
		fmt.Fprintf(w, `{"id":%q,"team_id":%q,"name":"Work","revision":7,"aggregate_revision":99,"base_schedule":%s,"working_weekend_schedule":{"reference_date":"2024-01-01","intervals":[]},"production_country_code":"RU"}`, fService, fTeam, actual)
	}))
	defer server.Close()
	configureZone := func() {
		c, err := client.New(server.URL, "fixture-token", client.WithTimezone(zone))
		if err != nil {
			t.Fatal(err)
		}
		var response resource.ConfigureResponse
		r.(resource.ResourceWithConfigure).Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &response)
		if response.Diagnostics.HasError() {
			t.Fatal(response.Diagnostics)
		}
	}
	configureZone()
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	values := workCalendarValues()
	values["base_schedule"] = calendarDatedValue("intervals", "2024-01-01", [2]string{"2024-01-01T09:00:00", "2024-01-01T17:00:00"})
	values["id"] = types.StringUnknown()
	values["request_timezone"] = types.StringValue("Europe/Moscow")
	plan := valueState(t, schema.Schema, values)
	create := resource.CreateResponse{State: tfsdk.State{Schema: schema.Schema, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
	temporalTestPrivate(&create)
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema.Schema, Raw: plan.Raw}}, &create)
	if create.Diagnostics.HasError() {
		t.Fatal(create.Diagnostics)
	}
	var base types.Object
	create.State.GetAttribute(t.Context(), path.Root("base_schedule"), &base)
	if !base.Equal(values["base_schedule"]) || calls != 2 {
		t.Fatalf("create changed local declaration or skipped fixed projection: %v calls=%d", base, calls)
	}
	zone = "UTC"
	configureZone()
	actual = `{"reference_date":"2024-01-01","intervals":[{"starts_at":"2024-01-01T06:00:00Z","ends_at":"2024-01-01T14:00:00Z"}]}`
	read := resource.ReadResponse{State: create.State, Private: create.Private}
	r.Read(t.Context(), resource.ReadRequest{State: create.State, Private: create.Private}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	read.State.GetAttribute(t.Context(), path.Root("base_schedule"), &base)
	if !base.Equal(values["base_schedule"]) {
		t.Fatal("refresh reinterpreted local declaration under new timezone")
	}
	modify := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: schema.Schema, Raw: read.State.Raw}}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), resource.ModifyPlanRequest{State: read.State, Plan: modify.Plan}, &modify)
	if modify.Diagnostics.HasError() {
		t.Fatal(modify.Diagnostics)
	}
	var plannedZone types.String
	modify.Plan.GetAttribute(t.Context(), path.Root("request_timezone"), &plannedZone)
	if plannedZone.ValueString() != "UTC" {
		t.Fatal("timezone change did not plan local-input update", plannedZone)
	}
}

func TestWorkCalendarEntryLocalLifecycleAndTypedErrors(t *testing.T) {
	r := onpager.NewWorkCalendarEntryResource()
	output := fmt.Sprintf(`{"id":%q,"calendar_id":%q,"revision":13,"name":"Exception","effect":"working","rule_type":"datetime_interval","starts_at":"2027-01-04T09:00:00.123456+03:00","ends_at":"2027-01-04T10:00:00.123456+03:00"}`, fOtherTeam, fService)
	calls := 0
	s := configuredResource(t, r, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Method == "GET" {
			fmt.Fprint(w, output)
			return
		}
		var body client.WorkCalendarEntryInput
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.StartsAt != "2027-01-04T09:00:00.123456" {
			t.Fatal("local entry converted before API request")
		}
		if req.Method == "PUT" {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"error":{"code":"ambiguous_local_time","field":"starts_at","details":{"time_zone":"Europe/Berlin","candidates":["2026-10-25T02:30:00+02:00","2026-10-25T02:30:00+01:00"]}}}`)
			return
		}
		fmt.Fprint(w, output)
	})
	values := workCalendarEntryValues()
	values["rule_type"] = types.StringValue("datetime_interval")
	values["start_date"] = types.StringNull()
	values["starts_at"] = types.StringValue("2027-01-04T09:00:00.123456")
	values["ends_at"] = types.StringValue("2027-01-04T10:00:00.123456")
	values["id"] = types.StringUnknown()
	plan := valueState(t, s, values)
	create := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(plan.Raw.Type(), nil)}}
	temporalTestPrivate(&create)
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan.Raw}}, &create)
	if create.Diagnostics.HasError() {
		t.Fatal(create.Diagnostics)
	}
	output = strings.ReplaceAll(output, "09:00:00.123456+03:00", "06:00:00.123456Z")
	output = strings.ReplaceAll(output, "10:00:00.123456+03:00", "07:00:00.123456Z")
	read := resource.ReadResponse{State: create.State, Private: create.Private}
	r.Read(t.Context(), resource.ReadRequest{State: create.State, Private: create.Private}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var start types.String
	read.State.GetAttribute(t.Context(), path.Root("starts_at"), &start)
	if start.ValueString() != "2027-01-04T09:00:00.123456" {
		t.Fatal("zone representation changed configured entry", start)
	}
	failed := resource.UpdateResponse{State: read.State, Private: read.Private}
	r.Update(t.Context(), resource.UpdateRequest{State: read.State, Plan: tfsdk.Plan{Schema: s, Raw: read.State.Raw}, Private: read.Private}, &failed)
	if !failed.Diagnostics.HasError() || !failed.State.Raw.Equal(read.State.Raw) || calls != 3 {
		t.Fatal("typed error retried or discarded existing entry")
	}
	if !strings.Contains(failed.Diagnostics.Errors()[0].Detail(), "ambiguous_local_time") {
		t.Fatal("backend DST error was lost", failed.Diagnostics)
	}
}

func TestWorkCalendarOmittedScheduleIsNotReplayedAfterTimezoneChange(t *testing.T) {
	r := onpager.NewWorkCalendarResource()
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "PATCH" {
			t.Error("unexpected secondary request")
		}
		writes++
		var body map[string]json.RawMessage
		if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["base_schedule"]; ok {
			t.Error("omitted base schedule was replayed")
		}
		if _, ok := body["working_weekend_schedule"]; ok {
			t.Error("omitted weekend schedule was replayed")
		}
		fmt.Fprintf(w, `{"id":%q,"team_id":%q,"name":"Renamed","revision":8,"aggregate_revision":100,"inherit_production_calendar":false,"base_schedule":{"reference_date":"2024-01-01","intervals":[{"starts_at":"2024-01-01T06:00:00Z","ends_at":"2024-01-01T14:00:00Z"}]},"working_weekend_schedule":{"reference_date":"2024-01-01","intervals":[]},"production_country_code":"RU"}`, fService, fTeam)
	}))
	defer server.Close()
	c, err := client.New(server.URL, "fixture-token", client.WithTimezone("UTC"))
	if err != nil {
		t.Fatal(err)
	}
	configured := resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &configured)
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	values := workCalendarValues()
	values["base_schedule"] = calendarDatedValue("intervals", "2024-01-01", [2]string{"2024-01-01T09:00:00", "2024-01-01T17:00:00"})
	values["request_timezone"] = types.StringValue("Europe/Moscow")
	old := valueState(t, schema.Schema, values)
	values["name"] = types.StringValue("Renamed")
	values["request_timezone"] = types.StringNull()
	plan := valueState(t, schema.Schema, values)
	values["base_schedule"] = types.ObjectNull(calendarWeeklyTypes)
	values["working_weekend_schedule"] = types.ObjectNull(calendarWeeklyTypes)
	cfg := valueState(t, schema.Schema, values)
	response := resource.UpdateResponse{State: old}
	temporalTestPrivate(&response)
	r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: schema.Schema, Raw: plan.Raw}, Config: tfsdk.Config{Schema: schema.Schema, Raw: cfg.Raw}}, &response)
	var zone types.String
	response.State.GetAttribute(t.Context(), path.Root("request_timezone"), &zone)
	if response.Diagnostics.HasError() || !zone.IsNull() || writes != 1 {
		t.Fatalf("omitted config changed timezone/state: %v %v", zone, response.Diagnostics)
	}
}

func TestWorkCalendarPostWriteReadCannotBindConcurrentRevision(t *testing.T) {
	for _, entry := range []bool{false, true} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			var r resource.Resource = onpager.NewWorkCalendarResource()
			if entry {
				r = onpager.NewWorkCalendarEntryResource()
			}
			calls := 0
			schema := configuredResource(t, r, func(w http.ResponseWriter, q *http.Request) {
				calls++
				rev, reference := int64(8), "2026-11-02"
				if q.Method == "GET" {
					rev, reference = 9, "2024-01-01"
				}
				if entry {
					if q.Method != "GET" {
						rev = 14
					} else {
						rev = 15
					}
					fmt.Fprintf(w, `{"id":%q,"calendar_id":%q,"name":"Exception","revision":%d,"effect":"working","rule_type":"weekly","weekdays":["MO"],"clock":{"reference_date":%q,"windows":[{"starts_at":"2024-01-01T08:00:00Z","ends_at":"2024-01-01T09:00:00Z"}]}}`, fOtherTeam, fService, rev, reference)
				} else {
					fmt.Fprintf(w, `{"id":%q,"team_id":%q,"name":"Work","revision":%d,"aggregate_revision":99,"inherit_production_calendar":false,"base_schedule":{"reference_date":%q,"intervals":[{"starts_at":"2024-01-01T08:00:00Z","ends_at":"2024-01-01T16:00:00Z"}]},"working_weekend_schedule":{"reference_date":%q,"intervals":[]},"production_country_code":"RU"}`, fService, fTeam, rev, reference, reference)
				}
			})
			values := workCalendarValues()
			wantRevision := int64(8)
			if entry {
				values = workCalendarEntryValues()
				values["rule_type"] = types.StringValue("weekly")
				values["start_date"] = types.StringNull()
				values["weekdays"] = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("MO")})
				values["clock"] = calendarDatedValue("windows", "2024-01-01", [2]string{"2024-01-01T09:00:00", "2024-01-01T10:00:00"})
				wantRevision = 14
			} else {
				values["base_schedule"] = calendarDatedValue("intervals", "2024-01-01", [2]string{"2024-01-01T09:00:00", "2024-01-01T17:00:00"})
			}
			old := valueState(t, schema, values)
			response := resource.UpdateResponse{State: old}
			temporalTestPrivate(&response)
			r.Update(t.Context(), resource.UpdateRequest{State: old, Plan: tfsdk.Plan{Schema: schema, Raw: old.Raw}}, &response)
			var revision types.Int64
			response.State.GetAttribute(t.Context(), path.Root("revision"), &revision)
			if !response.Diagnostics.HasError() || calls != 2 || revision.ValueInt64() != wantRevision {
				t.Fatalf("concurrent state was bound or committed revision lost: %v %v", revision, response.Diagnostics)
			}
		})
	}
}
