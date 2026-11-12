package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// User is safe human-account metadata. Local credential names and OIDC
// identities are selectors, not interchangeable login fields in this response.
type User struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	EmailNormalized string `json:"email_normalized"`
	Active          bool   `json:"active"`
}

// DeliveryChannel deliberately excludes provider config, credential references,
// secrets and target addresses even when unexpected fields appear in a response.
type DeliveryChannel struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"display_name"`
	Kind              string   `json:"kind"`
	ContractVersion   string   `json:"contract_version"`
	Capabilities      []string `json:"capabilities"`
	Enabled           bool     `json:"enabled"`
	UnavailableReason string   `json:"unavailable_reason"`
}

type exactFilter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// exactID is a singular, explicitly filtered catalog lookup, never a scan.
// Limit=2 plus has_more is sufficient to prove or disprove singularity.
func (c *Client) exactID(ctx context.Context, route, field, value, owner string, allowed ...string) (string, error) {
	valid := field == "id"
	for _, candidate := range allowed {
		valid = valid || field == candidate
	}
	if !valid {
		return "", errors.New("unsupported OnPager exact selector")
	}
	if field == "id" {
		if !validResponseID(value) {
			return "", errors.New("OnPager ID must be a nonzero canonical lowercase UUID")
		}
		return value, nil
	}
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return "", errors.New("OnPager exact selector must be a nonblank UTF-8 string")
	}
	if len(value) > 4096 {
		return "", errors.New("OnPager exact selector exceeds 4096 UTF-8 bytes; use the UUID selector")
	}
	filters := []exactFilter{{Field: field, Op: "equal", Value: value}}
	if owner != "" {
		filters = append(filters, exactFilter{Field: "team_id", Op: "equal", Value: owner})
	}
	encoded, err := json.Marshal(filters)
	if err != nil {
		return "", errors.New("could not encode OnPager exact selector")
	}
	if len(encoded) > 8192 {
		return "", errors.New("OnPager exact filter exceeds 8192 UTF-8 bytes; use the UUID selector")
	}
	query := url.Values{"limit": {"2"}, "filter": {string(encoded)}}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		HasMore *bool `json:"has_more"`
	}
	if err = c.json(ctx, http.MethodGet, route+"?"+query.Encode(), nil, &page, 0); err != nil {
		return "", err
	}
	if page.Items == nil || page.HasMore == nil {
		return "", errors.New("OnPager returned an incomplete exact lookup page")
	}
	if len(page.Items) > 1 || *page.HasMore {
		return "", errors.New("OnPager exact lookup is ambiguous; use a unique selector or UUID")
	}
	if len(page.Items) == 0 {
		return "", errors.New("OnPager exact lookup found no visible match; check the selector and read permissions")
	}
	if !validResponseID(page.Items[0].ID) {
		return "", errors.New("OnPager exact lookup returned an invalid identity")
	}
	return page.Items[0].ID, nil
}

func (c *Client) dataSourceDetail(ctx context.Context, route string, out any, required ...string) error {
	var raw json.RawMessage
	if err := c.json(ctx, http.MethodGet, route, nil, &raw, 0); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("OnPager returned invalid data-source metadata")
	}
	for _, name := range required {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("OnPager returned incomplete data-source metadata")
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("OnPager returned invalid data-source metadata")
	}
	return nil
}

func (c *Client) LookupUser(ctx context.Context, field, value string) (User, error) {
	var out User
	id, err := c.exactID(ctx, "/v1/users", field, value, "", "local_login", "email")
	if err == nil {
		err = c.dataSourceDetail(ctx, "/v1/users/"+url.PathEscape(id), &out, "id", "display_name", "active")
	}
	if err == nil {
		err = validateEntity(out.ID, out.DisplayName, id)
	}
	return out, err
}

func (c *Client) LookupTeam(ctx context.Context, field, value string) (Team, error) {
	var out Team
	id, err := c.exactID(ctx, "/v1/teams", field, value, "", "name")
	if err == nil {
		err = c.dataSourceDetail(ctx, teamPath(id), &out, "id", "name", "active")
	}
	if err == nil {
		err = validateEntity(out.ID, out.Name, id)
	}
	if err == nil && field == "name" && out.Name != value {
		err = errors.New("OnPager team no longer matches the exact name selector; run a fresh plan")
	}
	return out, err
}

func (c *Client) LookupService(ctx context.Context, field, value, teamID string) (Service, error) {
	var out Service
	if teamID != "" && !validResponseID(teamID) {
		return out, errors.New("OnPager expected service owner must be a nonzero canonical lowercase UUID")
	}
	id, err := c.exactID(ctx, "/v1/services", field, value, teamID, "name")
	if err == nil {
		err = c.dataSourceDetail(ctx, servicePath(id), &out, "id", "team_id", "name", "active")
	}
	if err == nil {
		err = validateEntity(out.ID, out.Name, id)
	}
	if err == nil && (!validResponseID(out.TeamID) || (teamID != "" && out.TeamID != teamID)) {
		err = errors.New("OnPager service has an invalid or unexpected owning team")
	}
	if err == nil && field == "name" && out.Name != value {
		err = errors.New("OnPager service no longer matches the exact name selector; run a fresh plan")
	}
	return out, err
}

func (c *Client) LookupRBACRole(ctx context.Context, field, value string) (RBACRole, error) {
	var out RBACRole
	id, err := c.exactID(ctx, "/v1/rbac/roles", field, value, "", "key")
	if err == nil {
		err = c.dataSourceDetail(ctx, rbacRolePath(id), &out, "id", "key", "name", "description", "scope_type", "permission_keys", "builtin", "revision")
	}
	if err == nil {
		err = validateEntity(out.ID, out.Name, id)
	}
	// Read-only sources include builtins and need no mutation ETag. Managed
	// role CRUD deliberately retains its separate built-in rejection contract.
	if err == nil && (!iamRoleKeyPattern.MatchString(out.Key) || (out.ScopeType != "tenant" && out.ScopeType != "team") || out.PermissionKeys == nil) {
		err = errors.New("OnPager returned invalid role metadata")
	}
	if err == nil {
		err = validMetadataSet(out.PermissionKeys)
	}
	if err == nil && field == "key" && out.Key != value {
		err = errors.New("OnPager returned a role with an unexpected key")
	}
	return out, err
}

func (c *Client) LookupGroup(ctx context.Context, field, value string) (Group, error) {
	var out Group
	id, err := c.exactID(ctx, "/v1/groups", field, value, "", "key", "name")
	if err == nil {
		err = c.dataSourceDetail(ctx, groupPath(id), &out, "id", "key", "name", "description", "system", "revision")
	}
	if err == nil {
		err = validateGroup(out, id)
	}
	if err == nil && ((field == "key" && out.Key != value) || (field == "name" && out.Name != value)) {
		err = errors.New("OnPager group no longer matches the exact selector; run a fresh plan")
	}
	return out, err
}

func (c *Client) GetDeliveryChannel(ctx context.Context, id string) (DeliveryChannel, error) {
	var out DeliveryChannel
	if !validResponseID(id) {
		return out, errors.New("OnPager channel ID must be a nonzero canonical lowercase UUID")
	}
	err := c.dataSourceDetail(ctx, "/v1/delivery/channels/"+url.PathEscape(id), &out, "id", "display_name", "kind", "contract_version", "capabilities", "enabled")
	if err == nil {
		err = validateEntity(out.ID, out.DisplayName, id)
	}
	if err == nil && (strings.TrimSpace(out.Kind) == "" || strings.TrimSpace(out.ContractVersion) == "" || out.Capabilities == nil) {
		err = errors.New("OnPager returned invalid delivery-channel metadata")
	}
	if err == nil {
		err = validMetadataSet(out.Capabilities)
	}
	return out, err
}

func validMetadataSet(values []string) error {
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) == "" || seen[v] {
			return fmt.Errorf("OnPager returned invalid metadata set values")
		}
		seen[v] = true
	}
	return nil
}
