package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type GroupInput struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type GroupUpdate struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}
type Group struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	System      bool   `json:"system"`
	Revision    int64  `json:"revision"`
	complete    bool
}

func (g *Group) UnmarshalJSON(data []byte) error {
	type plain Group
	var value plain
	err := json.Unmarshal(data, &value)
	// Preserve any decoded mutation identity even when another field has a
	// wrong type. The transport only decodes successful HTTP responses here.
	*g = Group(value)
	if err != nil {
		return err
	}
	var present struct {
		Description *string `json:"description"`
		System      *bool   `json:"system"`
	}
	if err := json.Unmarshal(data, &present); err != nil {
		return err
	}
	g.complete = present.Description != nil && present.System != nil
	return nil
}

var groupKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func groupPath(id string) string { return "/v1/groups/" + url.PathEscape(id) }
func validateGroup(g Group, id string) error {
	if !validResponseID(g.ID) || (id != "" && g.ID != id) || !groupKeyPattern.MatchString(g.Key) || strings.TrimSpace(g.Name) == "" || g.Revision <= 0 || !g.complete {
		return errors.New("OnPager returned an invalid or unexpected group")
	}
	return nil
}
func (c *Client) CreateGroup(ctx context.Context, in GroupInput) (Group, error) {
	var out Group
	err := c.json(ctx, http.MethodPost, "/v1/groups", in, &out, 0)
	if err == nil {
		err = validateGroup(out, "")
	}
	return out, err
}
func (c *Client) GetGroup(ctx context.Context, id string) (Group, error) {
	var out Group
	err := c.json(ctx, http.MethodGet, groupPath(id), nil, &out, 0)
	if err == nil {
		err = validateGroup(out, id)
	}
	return out, err
}
func (c *Client) UpdateGroup(ctx context.Context, id string, in GroupUpdate) (Group, error) {
	var out Group
	err := c.json(ctx, http.MethodPatch, groupPath(id), in, &out, 0)
	if err == nil {
		err = validateGroup(out, id)
	}
	return out, err
}
func (c *Client) DeleteGroup(ctx context.Context, id string, revision int64) error {
	if revision <= 0 {
		return errors.New("group deletion requires a positive metadata revision; refresh before retrying")
	}
	return allowAbsent(c.json(ctx, http.MethodDelete, groupPath(id), nil, nil, revision))
}

type GroupMembership struct {
	GroupID string `json:"group_id"`
	UserID  string `json:"user_id"`
}

func groupMembershipPath(group, user string) string {
	return groupPath(group) + "/manual-members/" + url.PathEscape(user)
}
func (c *Client) GetGroupMembership(ctx context.Context, group, user string) (GroupMembership, error) {
	var out GroupMembership
	err := c.json(ctx, http.MethodGet, groupMembershipPath(group, user), nil, &out, 0)
	if err == nil && (out.GroupID != group || out.UserID != user) {
		err = errors.New("OnPager returned an unexpected manual group membership identity")
	}
	return out, err
}
func (c *Client) EnsureGroupMembership(ctx context.Context, group, user string) error {
	return c.json(ctx, http.MethodPut, groupMembershipPath(group, user), nil, nil, 0)
}
func (c *Client) RemoveGroupMembership(ctx context.Context, group, user string) error {
	return allowAbsent(c.json(ctx, http.MethodDelete, groupMembershipPath(group, user), nil, nil, 0))
}

type OIDCGroupMappingInput struct {
	ProviderSlug string `json:"provider_slug"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ExternalKey  string `json:"external_key"`
}
type OIDCGroupMapping struct {
	ID           string `json:"id"`
	GroupID      string `json:"group_id"`
	ProviderSlug string `json:"provider_slug"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ExternalKey  string `json:"external_key"`
	Revision     int64  `json:"revision"`
}

var groupOIDCProviderPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func groupMappingsPath(group string) string { return groupPath(group) + "/oidc-mappings" }
func groupMappingPath(group, id string) string {
	return groupMappingsPath(group) + "/" + url.PathEscape(id)
}
func validateGroupMapping(v OIDCGroupMapping, group, id string) error {
	if !validResponseID(v.ID) || v.GroupID != group || (id != "" && v.ID != id) || v.Revision <= 0 || !groupOIDCProviderPattern.MatchString(v.ProviderSlug) || strings.TrimSpace(v.Issuer) == "" || v.ClientID == "" || v.ExternalKey == "" {
		return errors.New("OnPager returned an invalid or unexpected OIDC group mapping")
	}
	return nil
}
func (c *Client) CreateOIDCGroupMapping(ctx context.Context, group string, in OIDCGroupMappingInput) (OIDCGroupMapping, error) {
	var out OIDCGroupMapping
	err := c.json(ctx, http.MethodPost, groupMappingsPath(group), in, &out, 0)
	if err == nil {
		err = validateGroupMapping(out, group, "")
	}
	return out, err
}
func (c *Client) GetOIDCGroupMapping(ctx context.Context, group, id string) (OIDCGroupMapping, error) {
	var out OIDCGroupMapping
	err := c.json(ctx, http.MethodGet, groupMappingPath(group, id), nil, &out, 0)
	if err == nil {
		err = validateGroupMapping(out, group, id)
	}
	return out, err
}
func (c *Client) UpdateOIDCGroupMapping(ctx context.Context, group, id string, revision int64, in OIDCGroupMappingInput) (OIDCGroupMapping, error) {
	var out OIDCGroupMapping
	if revision <= 0 {
		return out, errors.New("OIDC mapping update requires a positive mapping revision; refresh before retrying")
	}
	err := c.json(ctx, http.MethodPut, groupMappingPath(group, id), in, &out, revision)
	if err == nil {
		err = validateGroupMapping(out, group, id)
	}
	return out, err
}
func (c *Client) DeleteOIDCGroupMapping(ctx context.Context, group, id string, revision int64) error {
	if revision <= 0 {
		return errors.New("OIDC mapping deletion requires a positive mapping revision; refresh before retrying")
	}
	return allowAbsent(c.json(ctx, http.MethodDelete, groupMappingPath(group, id), nil, nil, revision))
}
