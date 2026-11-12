package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// UpdateRoleAssignment atomically changes a full tuple under its existing UUID.
// Expected is the caller's saved tuple; it must never be silently refreshed or
// retried after a stale or unknown outcome. This endpoint has no ETag/revision.
func (c *Client) UpdateRoleAssignment(ctx context.Context, id string, desired, expected RoleAssignmentInput) (RoleAssignment, error) {
	var out RoleAssignment
	for _, input := range []RoleAssignmentInput{desired, expected} {
		if !validResponseID(id) || !validResponseID(input.RoleID) || !validRoleRecipient(input.Recipient.Kind) || !validResponseID(input.Recipient.ID) || input.TargetTeamID != nil && !validResponseID(*input.TargetTeamID) {
			return out, errors.New("role assignment update requires canonical nonzero UUIDs and known recipient kinds in both complete tuples")
		}
	}
	body, err := json.Marshal(struct {
		RoleAssignmentInput
		Expected RoleAssignmentInput `json:"expected"`
	}{RoleAssignmentInput: desired, Expected: expected})
	if err != nil {
		return out, errors.New("could not encode role assignment update")
	}
	res, err := c.do(ctx, http.MethodPut, roleAssignmentPath(id), bytes.NewReader(body), nil)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	out.Committed = res.StatusCode >= 200 && res.StatusCode < 300
	if out.Committed {
		// The addressed UUID is already known, even when the response is broken.
		out.ID = id
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		return out, errors.New("could not read OnPager response")
	}
	if len(raw) > 4<<20 {
		return out, errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if !out.Committed {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		code := envelope.Error.Code
		if !safeCode.MatchString(code) {
			code = ""
		}
		return out, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	if res.StatusCode != http.StatusOK {
		return out, errors.New("OnPager returned an unexpected role assignment update status")
	}
	var fields map[string]json.RawMessage
	var value RoleAssignment
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &value) != nil {
		return out, errors.New("OnPager returned an invalid role assignment update response")
	}
	for _, name := range []string{"id", "role_id", "recipient", "target_team_id", "protected"} {
		field, ok := fields[name]
		if !ok || name != "target_team_id" && bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return out, errors.New("OnPager returned an incomplete role assignment update response")
		}
	}
	if err := validateRoleAssignment(value, id); err != nil {
		return out, err
	}
	if value.RoleID != desired.RoleID || value.Recipient != desired.Recipient || !sameOptionalID(value.TargetTeamID, desired.TargetTeamID) {
		return out, errors.New("OnPager did not confirm the requested role assignment tuple")
	}
	value.Committed = true
	return value, nil
}
