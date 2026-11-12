package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestRoleAssignmentUpdateAtomicTuple(t *testing.T) {
	team := "33333333-3333-4333-8333-333333333333"
	for _, toTenant := range []bool{false, true} {
		t.Run(fmt.Sprint(toTenant), func(t *testing.T) {
			old := client.RoleAssignmentInput{RoleID: iamID, Recipient: client.RoleRecipient{Kind: "user", ID: iamRecipient}}
			desired := client.RoleAssignmentInput{RoleID: iamRecipient, Recipient: client.RoleRecipient{Kind: "group", ID: team}, TargetTeamID: &team}
			if toTenant {
				old, desired = desired, old
			}
			calls := 0
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "PUT" || r.URL.Path != "/v1/rbac/assignments/"+iamID || r.URL.RawQuery != "" || r.Header.Get("If-Match") != "" || r.Header.Get("Authorization") != "Bearer iam-test-secret" {
					t.Error("update must be one direct authenticated PUT without invented CAS headers")
				}
				var raw map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Error(err)
				}
				var input struct {
					client.RoleAssignmentInput
					Expected client.RoleAssignmentInput `json:"expected"`
				}
				encoded, _ := json.Marshal(raw)
				if err := json.Unmarshal(encoded, &input); err != nil || !reflect.DeepEqual(input.RoleAssignmentInput, desired) || !reflect.DeepEqual(input.Expected, old) || len(raw) != 4 {
					t.Error("desired and expected full tuples were not sent independently")
				}
				var expected map[string]json.RawMessage
				_ = json.Unmarshal(raw["expected"], &expected)
				if len(expected) != 3 || len(raw["target_team_id"]) == 0 || len(expected["target_team_id"]) == 0 || toTenant && string(raw["target_team_id"]) != "null" || !toTenant && string(expected["target_team_id"]) != "null" {
					t.Error("both nullable targets must be explicit")
				}
				_ = json.NewEncoder(w).Encode(client.RoleAssignment{ID: iamID, RoleAssignmentInput: desired})
			})
			v, err := c.UpdateRoleAssignment(t.Context(), iamID, desired, old)
			if err != nil || !v.Committed || v.ID != iamID || !reflect.DeepEqual(v.RoleAssignmentInput, desired) || calls != 1 {
				t.Fatalf("atomic update result=%+v err=%v calls=%d", v, err, calls)
			}
		})
	}
}

func TestRoleAssignmentUpdateRejectsUnconfirmedReceiptsWithoutLosingCommit(t *testing.T) {
	full := `{"id":"` + iamID + `","role_id":"` + iamID + `","recipient":{"kind":"service_account","id":"` + iamRecipient + `"},"target_team_id":null,"protected":false}`
	for _, body := range []string{
		`{`, `null`, `{}`, `{"id":"` + iamID + `"}`,
		strings.Replace(full, `"id":"`+iamID+`"`, `"id":"`+iamRecipient+`"`, 1),
		strings.Replace(full, `"role_id":"`+iamID+`"`, `"role_id":"`+iamRecipient+`"`, 1),
		strings.Replace(full, `"kind":"service_account"`, `"kind":"user"`, 1),
		strings.Replace(full, `"id":"`+iamRecipient+`"`, `"id":"`+iamID+`"`, 1),
		strings.Replace(full, `"target_team_id":null`, `"target_team_id":"`+iamRecipient+`"`, 1),
		strings.Replace(full, `"target_team_id":null,`, ``, 1),
		strings.Replace(full, `"protected":false`, `"protected":null`, 1),
		strings.Replace(full, `"protected":false`, `"protected":true`, 1),
		strings.Replace(full, `,"protected":false`, ``, 1),
		strings.Replace(full, iamID, "11111111-1111-4111-8111-11111111111A", 1),
		full + `{}`,
		strings.Repeat("x", (4<<20)+1),
	} {
		t.Run(fmt.Sprintf("receipt-%d", len(body)), func(t *testing.T) {
			calls := 0
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) })
			in := client.RoleAssignmentInput{RoleID: iamID, Recipient: client.RoleRecipient{Kind: "service_account", ID: iamRecipient}}
			v, err := c.UpdateRoleAssignment(t.Context(), iamID, in, in)
			if err == nil || !v.Committed || v.ID != iamID || calls != 1 {
				t.Fatalf("unconfirmed receipt accepted or committed UUID lost: id=%s committed=%t err=%v calls=%d", v.ID, v.Committed, err, calls)
			}
		})
	}
}

func TestRoleAssignmentUpdateFailuresNeverReplayOrLeak(t *testing.T) {
	in := client.RoleAssignmentInput{RoleID: iamID, Recipient: client.RoleRecipient{Kind: "service_account", ID: iamRecipient}}
	for _, status := range []int{201, 204, 400, 401, 403, 404, 409, 412, 422, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				if status != 204 {
					fmt.Fprint(w, `{"error":{"code":"conflict","message":"private-marker"}}`)
				}
			})
			v, err := c.UpdateRoleAssignment(t.Context(), iamID, in, in)
			var api *client.APIError
			committed := status < 300
			if err == nil || v.Committed != committed || calls != 1 || strings.Contains(err.Error(), "private-marker") || !committed && (!errors.As(err, &api) || api.StatusCode != status || v.ID != "") {
				t.Fatalf("wrong failure result id=%s committed=%t err=%v calls=%d", v.ID, v.Committed, err, calls)
			}
		})
	}
	for _, field := range []string{"id", "desired role", "expected role", "desired kind", "expected kind", "desired recipient", "expected recipient", "desired target", "expected target"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
			desired, expected, id := in, in, iamID
			bad := "00000000-0000-0000-0000-000000000000"
			switch field {
			case "id":
				id = bad
			case "desired role":
				desired.RoleID = bad
			case "expected role":
				expected.RoleID = bad
			case "desired kind":
				desired.Recipient.Kind = "unknown"
			case "expected kind":
				expected.Recipient.Kind = "unknown"
			case "desired recipient":
				desired.Recipient.ID = bad
			case "expected recipient":
				expected.Recipient.ID = bad
			case "desired target":
				desired.TargetTeamID = &bad
			case "expected target":
				expected.TargetTeamID = &bad
			}
			v, err := c.UpdateRoleAssignment(t.Context(), id, desired, expected)
			if err == nil || v.Committed || calls != 0 {
				t.Fatal("invalid update reached the API")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached API") })
	v, err := c.UpdateRoleAssignment(ctx, iamID, in, in)
	if !errors.Is(err, context.Canceled) || v.Committed {
		t.Fatal("context cancellation lost")
	}
}

func TestRoleAssignmentUpdateCommittedUnreadableBodyKeepsUUID(t *testing.T) {
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, `{`)
	})
	in := client.RoleAssignmentInput{RoleID: iamID, Recipient: client.RoleRecipient{Kind: "service_account", ID: iamRecipient}}
	v, err := c.UpdateRoleAssignment(t.Context(), iamID, in, in)
	if err == nil || !v.Committed || v.ID != iamID {
		t.Fatal("successful status was lost after body read failure")
	}
}
