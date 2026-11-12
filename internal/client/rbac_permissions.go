package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
)

// RBACPermission is public permission catalog metadata, not an access grant or
// an explanation of any principal's current authority.
type RBACPermission struct {
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Section         string   `json:"section"`
	Scopes          []string `json:"scopes"`
	Actors          []string `json:"actors"`
	Target          string   `json:"target"`
	Sensitive       bool     `json:"sensitive"`
	CustomGrantable bool     `json:"custom_grantable"`
}

// ListRBACPermissions reads the complete, unpaginated authenticated catalog.
// Exact future permission keys and section names are preserved without a local
// allowlist. Shared transport retains its response bound and safe errors.
func (c *Client) ListRBACPermissions(ctx context.Context) ([]RBACPermission, error) {
	var rows []json.RawMessage
	if err := c.json(ctx, http.MethodGet, "/v1/rbac/permissions", nil, &rows, 0); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("OnPager returned an incomplete permission catalog")
	}
	permissions := make([]RBACPermission, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, raw := range rows {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, errors.New("OnPager returned invalid permission metadata")
		}
		for _, name := range []string{"key", "name", "description", "section", "scopes", "actors", "target", "sensitive", "custom_grantable"} {
			value, ok := fields[name]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, errors.New("OnPager returned incomplete permission metadata")
			}
		}
		var permission RBACPermission
		if err := json.Unmarshal(raw, &permission); err != nil {
			return nil, errors.New("OnPager returned invalid permission metadata")
		}
		if permission.Key == "" || seen[permission.Key] ||
			!permissionMetadataSet(permission.Scopes, "tenant", "team") ||
			!permissionMetadataSet(permission.Actors, "user", "service_account") ||
			!slices.Contains([]string{"tenant", "team", "self"}, permission.Target) {
			return nil, errors.New("OnPager returned invalid or duplicate permission metadata")
		}
		seen[permission.Key] = true
		permissions = append(permissions, permission)
	}
	return permissions, nil
}

func permissionMetadataSet(values []string, allowed ...string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] || !slices.Contains(allowed, value) {
			return false
		}
		seen[value] = true
	}
	return values != nil
}
