package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ServiceAccountInput always sends owner_team_id: null clears management ownership.
type ServiceAccountInput struct {
	DisplayName string  `json:"display_name"`
	OwnerTeamID *string `json:"owner_team_id"`
}
type ServiceAccount struct {
	ID string `json:"id"`
	ServiceAccountInput
	Active bool `json:"active"`
}
type RBACRoleUpdate struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	PermissionKeys []string `json:"permission_keys"`
}
type RBACRoleInput struct {
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	ScopeType      string   `json:"scope_type"`
	PermissionKeys []string `json:"permission_keys"`
}
type RBACRole struct {
	ID string `json:"id"`
	RBACRoleInput
	Builtin  bool  `json:"builtin"`
	Revision int64 `json:"revision"`
}
type RoleRecipient struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type RoleAssignmentInput struct {
	RoleID       string        `json:"role_id"`
	Recipient    RoleRecipient `json:"recipient"`
	TargetTeamID *string       `json:"target_team_id"`
}
type RoleAssignment struct {
	ID string `json:"id"`
	RoleAssignmentInput
	Protected         bool `json:"protected"`
	TargetTeamPresent bool `json:"-"`
	// Committed identifies successful PUT responses, including malformed receipts.
	Committed bool `json:"-"`
}

func (v *RoleAssignment) UnmarshalJSON(data []byte) error {
	type plain RoleAssignment
	err := json.Unmarshal(data, (*plain)(v))
	var fields map[string]json.RawMessage
	if fieldErr := json.Unmarshal(data, &fields); fieldErr == nil {
		_, v.TargetTeamPresent = fields["target_team_id"]
	}
	return err
}

func serviceAccountPath(id string) string { return "/v1/service-accounts/" + url.PathEscape(id) }
func rbacRolePath(id string) string       { return "/v1/rbac/roles/" + url.PathEscape(id) }
func roleAssignmentPath(id string) string { return "/v1/rbac/assignments/" + url.PathEscape(id) }

// iamJSON reads response ETags in addition to JSON; role concurrency belongs to
// the whole custom role, rather than to the tenant IAM policy or its bindings.
func (c *Client) iamJSON(ctx context.Context, method, route string, input, output any, revision int64, required ...string) (string, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return "", errors.New("could not encode OnPager request")
		}
		body = bytes.NewReader(data)
	}
	headers := make(http.Header)
	if revision > 0 {
		headers.Set("If-Match", fmt.Sprintf(`"%d"`, revision))
	}
	resp, err := c.do(ctx, method, route, body, headers)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return "", errors.New("could not read OnPager response")
	}
	if len(data) > 4<<20 {
		return "", errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		code := envelope.Error.Code
		if !safeCode.MatchString(code) {
			code = ""
		}
		return "", &APIError{StatusCode: resp.StatusCode, Code: code}
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return "", errors.New("OnPager returned an invalid JSON response")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return "", errors.New("OnPager returned an invalid JSON response")
		}
		for _, name := range required {
			nullable := strings.HasPrefix(name, "?")
			name = strings.TrimPrefix(name, "?")
			if raw, ok := fields[name]; !ok || (!nullable && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))) {
				return resp.Header.Get("ETag"), errors.New("OnPager returned an incomplete IAM resource")
			}
		}
	}
	return resp.Header.Get("ETag"), nil
}

func validateServiceAccount(v ServiceAccount, id string) error {
	if err := validateEntity(v.ID, v.DisplayName, id); err != nil {
		return err
	}
	if v.OwnerTeamID != nil && !validResponseID(*v.OwnerTeamID) {
		return errors.New("OnPager returned an invalid service account owner")
	}
	return nil
}
func (c *Client) serviceAccount(ctx context.Context, method, route, id string, in any) (ServiceAccount, error) {
	var out ServiceAccount
	_, err := c.iamJSON(ctx, method, route, in, &out, 0, "id", "display_name", "?owner_team_id", "active")
	if err == nil {
		err = validateServiceAccount(out, id)
	}
	return out, err
}
func (c *Client) CreateServiceAccount(ctx context.Context, in ServiceAccountInput) (ServiceAccount, error) {
	return c.serviceAccount(ctx, http.MethodPost, "/v1/service-accounts", "", in)
}
func (c *Client) GetServiceAccount(ctx context.Context, id string) (ServiceAccount, error) {
	return c.serviceAccount(ctx, http.MethodGet, serviceAccountPath(id), id, nil)
}
func (c *Client) UpdateServiceAccount(ctx context.Context, id string, in ServiceAccountInput) (ServiceAccount, error) {
	return c.serviceAccount(ctx, http.MethodPut, serviceAccountPath(id), id, in)
}
func (c *Client) DeactivateServiceAccount(ctx context.Context, id string) error {
	return allowAbsent(c.json(ctx, http.MethodPost, serviceAccountPath(id)+"/deactivate", nil, nil, 0))
}
func (c *Client) ReactivateServiceAccount(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, serviceAccountPath(id)+"/reactivate", nil, nil, 0)
}

var iamRoleKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func validateRBACRole(v RBACRole, id, tag string) error {
	if err := validateEntity(v.ID, v.Name, id); err != nil {
		return err
	}
	if !iamRoleKeyPattern.MatchString(v.Key) || (v.ScopeType != "tenant" && v.ScopeType != "team") || v.PermissionKeys == nil || v.Revision <= 0 || tag != fmt.Sprintf(`"%d"`, v.Revision) {
		return errors.New("OnPager returned an invalid custom role or strong role ETag")
	}
	if v.Builtin {
		return errors.New("built-in OnPager roles cannot be managed as custom roles")
	}
	seen := make(map[string]bool)
	for _, key := range v.PermissionKeys {
		if strings.TrimSpace(key) == "" || seen[key] {
			return errors.New("OnPager returned invalid custom role permission keys")
		}
		seen[key] = true
	}
	return nil
}
func (c *Client) rbacRole(ctx context.Context, method, route, id string, revision int64, in any) (RBACRole, error) {
	var out RBACRole
	tag, err := c.iamJSON(ctx, method, route, in, &out, revision, "id", "key", "name", "description", "scope_type", "permission_keys", "builtin", "revision")
	if err == nil {
		err = validateRBACRole(out, id, tag)
	}
	if out.Revision <= 0 || tag != fmt.Sprintf(`"%d"`, out.Revision) {
		out.Revision = 0
	}
	return out, err
}
func (c *Client) CreateRBACRole(ctx context.Context, in RBACRoleInput) (RBACRole, error) {
	return c.rbacRole(ctx, http.MethodPost, "/v1/rbac/roles", "", 0, in)
}
func (c *Client) GetRBACRole(ctx context.Context, id string) (RBACRole, error) {
	return c.rbacRole(ctx, http.MethodGet, rbacRolePath(id), id, 0, nil)
}
func (c *Client) UpdateRBACRole(ctx context.Context, id string, revision int64, in RBACRoleUpdate) (RBACRole, error) {
	if revision <= 0 {
		return RBACRole{}, errors.New("a current positive custom role revision is required")
	}
	return c.rbacRole(ctx, http.MethodPut, rbacRolePath(id), id, revision, in)
}
func (c *Client) DeleteRBACRole(ctx context.Context, id string, revision int64) error {
	if revision <= 0 {
		return errors.New("a current positive custom role revision is required")
	}
	return allowAbsent(c.json(ctx, http.MethodDelete, rbacRolePath(id), nil, nil, revision))
}

func validRoleRecipient(kind string) bool {
	return kind == "user" || kind == "group" || kind == "team" || kind == "service_account"
}
func validateRoleAssignment(v RoleAssignment, id string) error {
	if !validResponseID(v.ID) || (id != "" && v.ID != id) || !validResponseID(v.RoleID) || !validRoleRecipient(v.Recipient.Kind) || !validResponseID(v.Recipient.ID) || (v.TargetTeamID != nil && !validResponseID(*v.TargetTeamID)) {
		return errors.New("OnPager returned an invalid role assignment or unexpected identity")
	}
	if v.Protected {
		return errors.New("protected OnPager role assignments cannot be managed")
	}
	return nil
}
func (c *Client) GetRoleAssignment(ctx context.Context, id string) (RoleAssignment, error) {
	var out RoleAssignment
	_, err := c.iamJSON(ctx, http.MethodGet, roleAssignmentPath(id), nil, &out, 0, "id", "role_id", "recipient", "?target_team_id", "protected")
	if err == nil {
		err = validateRoleAssignment(out, id)
	}
	return out, err
}
func (c *Client) CreateRoleAssignment(ctx context.Context, in RoleAssignmentInput) (RoleAssignment, error) {
	var out RoleAssignment
	_, err := c.iamJSON(ctx, http.MethodPost, "/v1/rbac/assignments", in, &out, 0, "id", "role_id", "recipient", "?target_team_id", "protected")
	if out.TargetTeamPresent && !sameOptionalID(out.TargetTeamID, in.TargetTeamID) {
		out.ID = ""
		return out, errors.New("OnPager returned a contradictory role assignment target")
	}
	if err == nil {
		err = validateRoleAssignment(out, "")
	}
	if err == nil && (out.RoleID != in.RoleID || out.Recipient != in.Recipient || !sameOptionalID(out.TargetTeamID, in.TargetTeamID)) {
		err = errors.New("OnPager returned an unexpected role assignment identity")
		out.ID = ""
	}
	return out, err
}
func sameOptionalID(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func (c *Client) DeleteRoleAssignment(ctx context.Context, id string) error {
	// Current API distinguishes denied retirement (403) from confirmed absence.
	return allowAbsent(c.json(ctx, http.MethodDelete, roleAssignmentPath(id), nil, nil, 0))
}
