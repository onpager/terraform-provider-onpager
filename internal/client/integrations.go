package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

type IntegrationPolling struct {
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	IntervalSeconds int64  `json:"interval_seconds"`
	AuthKind        string `json:"auth_kind"`
	Username        string `json:"username"`
	HasSecret       bool   `json:"has_secret"`
}

type IntegrationPollingInput struct {
	Enabled         bool    `json:"enabled"`
	BaseURL         string  `json:"base_url,omitempty"`
	IntervalSeconds int64   `json:"interval_seconds,omitempty"`
	AuthKind        string  `json:"auth_kind,omitempty"`
	Username        string  `json:"username,omitempty"`
	Password        *string `json:"password,omitempty"`
	Token           *string `json:"token,omitempty"`
}

type Integration struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	SourceKind  string              `json:"source_kind"`
	Enabled     bool                `json:"enabled"`
	Revision    int64               `json:"revision"`
	WebhookPath string              `json:"webhook_path"`
	WebhookURL  string              `json:"webhook_url"`
	Token       string              `json:"token,omitempty"`
	Polling     *IntegrationPolling `json:"polling"`
}

type IntegrationInput struct {
	Name       string                   `json:"name"`
	SourceKind string                   `json:"source_kind"`
	Polling    *IntegrationPollingInput `json:"polling,omitempty"`
}

type IntegrationPatch struct {
	Name    *string                  `json:"name,omitempty"`
	Enabled *bool                    `json:"enabled,omitempty"`
	Polling *IntegrationPollingInput `json:"polling,omitempty"`
}

type IntegrationReceipt struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

func integrationPath(id string) string { return "/v1/integrations/" + url.PathEscape(id) }

func validIntegrationKind(kind string) bool {
	switch kind {
	case "alertmanager", "grafana_unified", "grafana_legacy", "generic_onpager_v1":
		return true
	default:
		return false
	}
}

func validateIntegration(v Integration, expected string) error {
	if !validResponseID(v.ID) || (expected != "" && v.ID != expected) || v.Name == "" || v.Revision < 1 || !validIntegrationKind(v.SourceKind) || v.WebhookPath == "" {
		return errors.New("OnPager returned an incomplete integration or unexpected identity")
	}
	if p := v.Polling; p != nil && (p.IntervalSeconds < 15 || p.IntervalSeconds > 3600 || (p.AuthKind != "none" && p.AuthKind != "basic" && p.AuthKind != "bearer")) {
		return errors.New("OnPager returned invalid integration polling settings")
	}
	return nil
}

func (c *Client) CreateIntegration(ctx context.Context, input IntegrationInput) (Integration, error) {
	var v Integration
	err := c.json(ctx, http.MethodPost, "/v1/integrations", input, &v, 0)
	if err == nil {
		err = validateIntegration(v, "")
		if v.Token == "" {
			err = errors.New("OnPager omitted the new integration's one-time token")
		}
	}
	return v, err
}

func (c *Client) GetIntegration(ctx context.Context, id string) (Integration, error) {
	var v Integration
	err := c.json(ctx, http.MethodGet, integrationPath(id), nil, &v, 0)
	if err == nil {
		err = validateIntegration(v, id)
	}
	return v, err
}

func (c *Client) UpdateIntegration(ctx context.Context, id string, revision int64, patch IntegrationPatch) (IntegrationReceipt, error) {
	if revision < 1 {
		return IntegrationReceipt{}, errors.New("a current integration revision is required")
	}
	var v IntegrationReceipt
	err := c.json(ctx, http.MethodPatch, integrationPath(id), patch, &v, revision)
	if err == nil && (v.ID != id || v.Revision < revision) {
		err = errors.New("OnPager returned an invalid integration mutation receipt")
	}
	return v, err
}

func (c *Client) DeleteIntegration(ctx context.Context, id string, revision int64) error {
	if revision < 1 {
		return errors.New("a current integration revision is required")
	}
	return allowAbsent(c.json(ctx, http.MethodDelete, integrationPath(id), nil, nil, revision))
}
