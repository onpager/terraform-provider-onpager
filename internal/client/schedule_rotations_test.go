package client_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const rotationFixtureID = "44444444-4444-4444-8444-444444444444"
const rotationStart = "2026-01-01T09:00:00Z"
const rotationEnd = "2026-01-02T09:00:00Z"
const rotationEffective = "2040-01-01T00:00:00Z"
const primaryRuleJSON = `{"starts_at":"2026-01-01T09:00:00Z","ends_at":null,"user_ids":["11111111-1111-4111-8111-111111111111"],"recurrence":{"every":1,"unit":"days"},"weekdays":null,"max_shift_duration_seconds":null}`
const overrideRuleJSON = `{"starts_at":"2026-01-01T09:00:00Z","ends_at":"2026-01-02T09:00:00Z","user_id":"11111111-1111-4111-8111-111111111111"}`

func primaryRotationRule() client.PrimaryRotationRule {
	return client.PrimaryRotationRule{StartsAt: rotationStart, UserIDs: []string{"11111111-1111-4111-8111-111111111111"}, Recurrence: client.ScheduleRecurrence{Every: 1, Unit: "days"}}
}
func overrideRotationRule() client.OverrideRotationRule {
	return client.OverrideRotationRule{StartsAt: rotationStart, EndsAt: rotationEnd, UserID: "11111111-1111-4111-8111-111111111111"}
}
func declarationJSON(kind, rule string) string {
	effective := rotationEffective
	if kind == "override" {
		effective = rotationStart
	}
	return `{"id":"` + rotationFixtureID + `","schedule_id":"` + scheduleFixtureID + `","kind":"` + kind + `","revision":2,"aggregate_revision":99,"effective_from":"` + effective + `","configuration":` + rule + `}`
}

// Using the occurrence endpoint or aggregate revision here would replace the
// wrong plan or lose concurrency protection when unrelated siblings change.
func TestRotationDeclarationLifecycleRequests(t *testing.T) {
	for _, kind := range []string{"primary", "override"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			base := "/v1/schedules/" + scheduleFixtureID + "/" + kind + "-rotations"
			rule := primaryRuleJSON
			if kind == "override" {
				rule = overrideRuleJSON
			}
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch calls {
				case 1, 3:
					wantMethod, wantPath, wantTag := "POST", base, ""
					wantBody := rule
					if kind == "primary" {
						wantBody = strings.TrimSuffix(rule, "}") + `,"effective_from":"` + rotationEffective + `"}`
					}
					if calls == 3 {
						wantMethod, wantPath, wantTag = "PUT", base+"/"+rotationFixtureID+"/declaration", `"2"`
						wantBody = `{"configuration":` + rule + `}`
						if kind == "primary" {
							wantBody = `{"effective_from":"` + rotationEffective + `","configuration":` + rule + `}`
						}
					}
					if r.Method != wantMethod || r.URL.Path != wantPath || r.Header.Get("If-Match") != wantTag || r.URL.RawQuery != "" {
						t.Errorf("wrong write: %s %s %q", r.Method, r.URL, r.Header.Get("If-Match"))
					}
					var got, want any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(wantBody), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("body=%#v; want %#v", got, want)
					}
					io.WriteString(w, `{"rotation_id":"`+rotationFixtureID+`","revision":2,"aggregate_revision":99}`)
				case 2:
					if r.Method != "GET" || r.URL.Path != base+"/"+rotationFixtureID+"/declaration" || r.URL.RawQuery != "" || r.Header.Get("If-Match") != "" {
						t.Errorf("wrong declaration read: %s %s", r.Method, r.URL)
					}
					io.WriteString(w, declarationJSON(kind, rule))
				case 4:
					if r.Method != "DELETE" || r.URL.Path != base+"/"+rotationFixtureID || r.URL.RawQuery != "mode=all" || r.Header.Get("If-Match") != `"2"` {
						t.Errorf("wrong delete: %s %s %q", r.Method, r.URL, r.Header.Get("If-Match"))
					}
					b, _ := io.ReadAll(r.Body)
					if len(b) != 0 {
						t.Errorf("delete body=%s", b)
					}
					io.WriteString(w, `{"rotation_id":"`+rotationFixtureID+`","revision":3,"aggregate_revision":100}`)
				default:
					t.Error("unexpected extra request")
				}
			})
			if kind == "primary" {
				receipt, err := c.CreatePrimaryRotation(t.Context(), scheduleFixtureID, rotationEffective, primaryRotationRule())
				if err != nil || receipt.RotationID != rotationFixtureID || receipt.Revision != 2 {
					t.Fatalf("create: %+v %v", receipt, err)
				}
				got, err := c.GetPrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID)
				if err != nil || got.ID != rotationFixtureID || got.Revision != 2 || got.EffectiveFrom != rotationEffective || !reflect.DeepEqual(got.Configuration, primaryRotationRule()) {
					t.Fatalf("read: %+v %v", got, err)
				}
				if _, err := c.UpdatePrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2, rotationEffective, primaryRotationRule()); err != nil {
					t.Fatal(err)
				}
				if err := c.DeletePrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2); err != nil {
					t.Fatal(err)
				}
			} else {
				receipt, err := c.CreateOverrideRotation(t.Context(), scheduleFixtureID, overrideRotationRule())
				if err != nil || receipt.RotationID != rotationFixtureID || receipt.Revision != 2 {
					t.Fatalf("create: %+v %v", receipt, err)
				}
				got, err := c.GetOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID)
				if err != nil || got.ID != rotationFixtureID || got.Revision != 2 || got.EffectiveFrom != rotationStart || !reflect.DeepEqual(got.Configuration, overrideRotationRule()) {
					t.Fatalf("read: %+v %v", got, err)
				}
				if _, err := c.UpdateOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2, overrideRotationRule()); err != nil {
					t.Fatal(err)
				}
				if err := c.DeleteOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 4 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestRotationDeclarationRejectsIncompleteOrInvalidResponses(t *testing.T) {
	primary := declarationJSON("primary", primaryRuleJSON)
	override := declarationJSON("override", overrideRuleJSON)
	cases := []struct{ name, kind, body string }{
		{"empty", "primary", `{}`},
		{"wrong rotation", "primary", strings.Replace(primary, rotationFixtureID, teamID, 1)},
		{"wrong owner", "primary", strings.Replace(primary, scheduleFixtureID, teamID, 1)},
		{"wrong kind", "primary", strings.Replace(primary, `"kind":"primary"`, `"kind":"override"`, 1)},
		{"zero revision", "primary", strings.Replace(primary, `"revision":2`, `"revision":0`, 1)},
		{"missing boundary", "primary", strings.Replace(primary, `"effective_from":"`+rotationEffective+`",`, "", 1)},
		{"fractional boundary", "primary", strings.Replace(primary, rotationEffective, "2040-01-01T00:00:00.1Z", 1)},
		{"missing nullable end", "primary", strings.Replace(primary, `"ends_at":null,`, "", 1)},
		{"missing weekdays", "primary", strings.Replace(primary, `"weekdays":null,`, "", 1)},
		{"missing duration", "primary", strings.Replace(primary, `,"max_shift_duration_seconds":null`, "", 1)},
		{"missing recurrence", "primary", strings.Replace(primary, `"recurrence":{"every":1,"unit":"days"},`, "", 1)},
		{"empty participants", "primary", strings.Replace(primary, `["11111111-1111-4111-8111-111111111111"]`, `[]`, 1)},
		{"duplicate participants", "primary", strings.Replace(primary, `["11111111-1111-4111-8111-111111111111"]`, `["11111111-1111-4111-8111-111111111111","11111111-1111-4111-8111-111111111111"]`, 1)},
		{"invalid participant", "primary", strings.Replace(primary, `11111111-1111-4111-8111-111111111111`, `invalid`, 1)},
		{"invalid anchor", "primary", strings.Replace(primary, rotationStart, "invalid", 1)},
		{"zero recurrence", "primary", strings.Replace(primary, `"every":1`, `"every":0`, 1)},
		{"unknown recurrence", "primary", strings.Replace(primary, `"unit":"days"`, `"unit":"minutes"`, 1)},
		{"empty weekdays", "primary", strings.Replace(primary, `"weekdays":null`, `"weekdays":[]`, 1)},
		{"invalid weekdays", "primary", strings.Replace(primary, `"weekdays":null`, `"weekdays":["Monday"]`, 1)},
		{"duplicate weekdays", "primary", strings.Replace(primary, `"weekdays":null`, `"weekdays":["MO","MO"]`, 1)},
		{"zero duration", "primary", strings.Replace(primary, `"max_shift_duration_seconds":null`, `"max_shift_duration_seconds":0`, 1)},
		{"duration overflow", "primary", strings.Replace(primary, `"max_shift_duration_seconds":null`, `"max_shift_duration_seconds":9223372036854775807`, 1)},
		{"override wrong kind", "override", strings.Replace(override, `"kind":"override"`, `"kind":"primary"`, 1)},
		{"override missing end", "override", strings.Replace(override, `"ends_at":"`+rotationEnd+`",`, "", 1)},
		{"override null end", "override", strings.Replace(override, `"ends_at":"`+rotationEnd+`"`, `"ends_at":null`, 1)},
		{"override missing user", "override", strings.Replace(override, `,"user_id":"11111111-1111-4111-8111-111111111111"`, "", 1)},
		{"override reversed interval", "override", strings.Replace(override, rotationEnd, "2025-01-01T00:00:00Z", 1)},
		{"override inconsistent effective", "override", strings.Replace(override, `"effective_from":"`+rotationStart+`"`, `"effective_from":"`+rotationEnd+`"`, 1)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tt.body) })
			var err error
			if tt.kind == "primary" {
				_, err = c.GetPrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID)
			} else {
				_, err = c.GetOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID)
			}
			if err == nil || client.IsNotFound(err) {
				t.Fatalf("malformed declaration became success/absence: %v", err)
			}
		})
	}
}

func TestRotationReceiptsKeepTrustworthyPartialCreateIdentity(t *testing.T) {
	for _, kind := range []string{"primary", "override"} {
		for _, body := range []string{
			`{"rotation_id":"` + rotationFixtureID + `"}`,
			`{"rotation_id":"` + rotationFixtureID + `","revision":"invalid"}`,
			`{"rotation_id":"invalid","revision":1}`,
			`{}`,
		} {
			t.Run(kind+body, func(t *testing.T) {
				c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
				var got client.RotationReceipt
				var err error
				if kind == "primary" {
					got, err = c.CreatePrimaryRotation(t.Context(), scheduleFixtureID, rotationEffective, primaryRotationRule())
				} else {
					got, err = c.CreateOverrideRotation(t.Context(), scheduleFixtureID, overrideRotationRule())
				}
				if err == nil {
					t.Fatal("malformed receipt accepted")
				}
				wantID := ""
				if strings.Contains(body, rotationFixtureID) {
					wantID = rotationFixtureID
				}
				if got.RotationID != wantID {
					t.Fatalf("partial identity=%q want %q", got.RotationID, wantID)
				}
			})
		}
	}
}

func TestRotationOwnRevisionDoesNotRequireAggregate(t *testing.T) {
	for _, kind := range []string{"primary", "override"} {
		t.Run(kind, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					rule := primaryRuleJSON
					if kind == "override" {
						rule = overrideRuleJSON
					}
					io.WriteString(w, strings.Replace(declarationJSON(kind, rule), `"aggregate_revision":99,`, "", 1))
					return
				}
				io.WriteString(w, `{"rotation_id":"`+rotationFixtureID+`","revision":1}`)
			})
			if kind == "primary" {
				if _, err := c.CreatePrimaryRotation(t.Context(), scheduleFixtureID, rotationEffective, primaryRotationRule()); err != nil {
					t.Fatal(err)
				}
				if _, err := c.GetPrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := c.CreateOverrideRotation(t.Context(), scheduleFixtureID, overrideRotationRule()); err != nil {
					t.Fatal(err)
				}
				if _, err := c.GetOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRotationDeleteOnlyConfirmedAbsenceIsIdempotent(t *testing.T) {
	for _, kind := range []string{"primary", "override"} {
		for _, tt := range []struct {
			status int
			code   string
			ok     bool
		}{
			{404, "not_found", true}, {404, "", false}, {404, "foreign_object", false}, {401, "unauthorized", false}, {403, "forbidden", false}, {412, "revision_mismatch", false}, {409, "conflict", false}, {500, "internal", false},
		} {
			t.Run(kind+tt.code, func(t *testing.T) {
				calls := 0
				c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(tt.status)
					io.WriteString(w, `{"error":{"code":"`+tt.code+`"}}`)
				})
				var err error
				if kind == "primary" {
					err = c.DeletePrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2)
				} else {
					err = c.DeleteOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2)
				}
				if (err == nil) != tt.ok || calls != 1 {
					t.Fatalf("delete: err=%v calls=%d", err, calls)
				}
			})
		}
	}
}

func TestRotationUpdatesRejectForeignOrIncompleteReceipt(t *testing.T) {
	for _, kind := range []string{"primary", "override"} {
		for _, body := range []string{
			`{}`,
			`{"rotation_id":"` + rotationFixtureID + `"}`,
			`{"rotation_id":"` + teamID + `","revision":3}`,
		} {
			t.Run(kind+body, func(t *testing.T) {
				c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
				var receipt client.RotationReceipt
				var err error
				if kind == "primary" {
					receipt, err = c.UpdatePrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2, rotationEffective, primaryRotationRule())
				} else {
					receipt, err = c.UpdateOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2, overrideRotationRule())
				}
				if err == nil {
					t.Fatal("malformed update receipt accepted")
				}
				if receipt.RotationID != "" && receipt.RotationID != rotationFixtureID {
					t.Fatalf("adopted foreign identity: %q", receipt.RotationID)
				}
			})
		}
	}
}

func TestRotationDeleteRejectsMalformedSuccess(t *testing.T) {
	for _, kind := range []string{"primary", "override"} {
		for _, body := range []string{
			`{}`, ``, `{"rotation_id":"` + teamID + `","revision":3}`, `{"rotation_id":"` + rotationFixtureID + `"}`,
		} {
			t.Run(kind+body, func(t *testing.T) {
				c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
				var err error
				if kind == "primary" {
					err = c.DeletePrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2)
				} else {
					err = c.DeleteOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2)
				}
				if err == nil || client.IsNotFound(err) {
					t.Fatalf("malformed delete became success/absence: %v", err)
				}
			})
		}
	}
}

func TestRotationReadsAndUpdatesSurfaceErrorsWithoutFallbackOrRetry(t *testing.T) {
	for _, kind := range []string{"primary", "override"} {
		for _, operation := range []string{"create", "read", "update"} {
			for _, tt := range []struct {
				status int
				code   string
			}{
				{404, "not_found"}, {404, ""}, {401, "unauthorized"}, {403, "forbidden"}, {412, "revision_mismatch"}, {500, "internal"},
			} {
				t.Run(kind+operation+tt.code, func(t *testing.T) {
					calls := 0
					c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
						calls++
						w.WriteHeader(tt.status)
						io.WriteString(w, `{"error":{"code":"`+tt.code+`"}}`)
					})
					var err error
					if kind == "primary" {
						switch operation {
						case "create":
							_, err = c.CreatePrimaryRotation(t.Context(), scheduleFixtureID, rotationEffective, primaryRotationRule())
						case "read":
							_, err = c.GetPrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID)
						case "update":
							_, err = c.UpdatePrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2, rotationEffective, primaryRotationRule())
						}
					} else {
						switch operation {
						case "create":
							_, err = c.CreateOverrideRotation(t.Context(), scheduleFixtureID, overrideRotationRule())
						case "read":
							_, err = c.GetOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID)
						case "update":
							_, err = c.UpdateOverrideRotation(t.Context(), scheduleFixtureID, rotationFixtureID, 2, overrideRotationRule())
						}
					}
					if err == nil || client.IsNotFound(err) != (tt.status == 404 && tt.code == "not_found") || calls != 1 {
						t.Fatalf("operation: err=%v calls=%d", err, calls)
					}
				})
			}
		}
	}
}

func TestPrimaryRotationRetainsBoundedDeclarationAndExplicitOptions(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, declarationJSON("primary", `{"starts_at":"2026-01-01T09:00:00+03:00","ends_at":"2040-02-01T09:00:00Z","user_ids":["11111111-1111-4111-8111-111111111111"],"recurrence":{"every":2,"unit":"weeks"},"weekdays":["MO","WE"],"max_shift_duration_seconds":3600}`))
	})
	got, err := c.GetPrimaryRotation(t.Context(), scheduleFixtureID, rotationFixtureID)
	if err != nil {
		t.Fatal(err)
	}
	rule := got.Configuration
	if rule.StartsAt != "2026-01-01T09:00:00+03:00" || rule.EndsAt == nil || *rule.EndsAt != "2040-02-01T09:00:00Z" || rule.Recurrence.Every != 2 || rule.Recurrence.Unit != "weeks" || !reflect.DeepEqual(rule.Weekdays, []string{"MO", "WE"}) || rule.MaxShiftDurationSeconds == nil || *rule.MaxShiftDurationSeconds != 3600 {
		t.Fatalf("declaration changed: %+v", rule)
	}
}
