package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// AlertFilterInput publishes the desired content and active state atomically.
// Active is always serialized, including false, to support inactive owners.
type AlertFilterInput struct {
	CELSource string `json:"cel_source"`
	Active    bool   `json:"active"`
}
type AlertFilter struct {
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	Revision   int64  `json:"revision"`
	CELSource  string `json:"cel_source"`
	Active     bool   `json:"active"`
}

func routingFilterPath(kind, id string) (string, error) {
	switch kind {
	case "team":
		return teamPath(id) + "/alert-filter", nil
	case "service":
		return servicePath(id) + "/alert-filter", nil
	}
	return "", errors.New("invalid alert-filter scope")
}
func filterValidate(v AlertFilter, kind, id string) error {
	if v.TargetKind != kind || v.TargetID != id || !validResponseID(v.TargetID) || v.Revision < 1 || strings.TrimSpace(v.CELSource) == "" {
		return errors.New("OnPager returned an incomplete alert filter or unexpected identity")
	}
	return nil
}
func (c *Client) GetAlertFilter(ctx context.Context, kind, id string) (AlertFilter, error) {
	var out AlertFilter
	p, e := routingFilterPath(kind, id)
	if e != nil {
		return out, e
	}
	e = c.json(ctx, http.MethodGet, p, nil, &out, 0)
	if e == nil {
		e = filterValidate(out, kind, id)
	}
	return out, e
}
func (c *Client) PublishAlertFilter(ctx context.Context, kind, id string, in AlertFilterInput) (AlertFilter, error) {
	var out AlertFilter
	p, e := routingFilterPath(kind, id)
	if e != nil {
		return out, e
	}
	e = c.json(ctx, http.MethodPost, p+"/revisions", in, &out, 0)
	if e == nil {
		e = filterValidate(out, kind, id)
	}
	return out, e
}
func (c *Client) DisableAlertFilter(ctx context.Context, kind, id string) error {
	p, e := routingFilterPath(kind, id)
	if e != nil {
		return e
	}
	return allowAbsent(c.json(ctx, http.MethodPost, p+"/disable", nil, nil, 0))
}

type EscalationRuleInput struct {
	Name      string `json:"name"`
	CELSource string `json:"cel_source"`
	ChainID   string `json:"chain_id"`
	Active    bool   `json:"active"`
}
type EscalationRule struct {
	RuleID   string `json:"rule_id"`
	TeamID   string `json:"team_id"`
	Revision int64  `json:"revision"`
	// ActivePresent distinguishes a returned false from an omitted create field.
	ActivePresent bool `json:"-"`
	EscalationRuleInput
}

func routingRulePath(team, id string) string {
	p := teamPath(team) + "/escalation-rules"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}
func ruleValidate(v EscalationRule, team, id string) error {
	if !validResponseID(v.RuleID) || v.TeamID != team || (id != "" && v.RuleID != id) || !validResponseID(v.ChainID) || v.Revision < 1 || strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.CELSource) == "" {
		return errors.New("OnPager returned an incomplete escalation rule or unexpected identity")
	}
	return nil
}
func (c *Client) CreateEscalationRule(ctx context.Context, team string, in EscalationRuleInput) (EscalationRule, error) {
	var receipt struct {
		EscalationRule
		Active *bool `json:"active"`
	}
	e := c.json(ctx, http.MethodPost, routingRulePath(team, ""), in, &receipt, 0)
	out := receipt.EscalationRule
	out.ActivePresent = receipt.Active != nil
	if receipt.Active != nil {
		out.Active = *receipt.Active
	}
	if e == nil {
		e = ruleValidate(out, team, "")
	}
	return out, e
}
func (c *Client) PublishEscalationRule(ctx context.Context, team, id string, in EscalationRuleInput) (EscalationRule, error) {
	var out EscalationRule
	e := c.json(ctx, http.MethodPost, routingRulePath(team, id)+"/revisions", in, &out, 0)
	if e == nil {
		e = ruleValidate(out, team, id)
	}
	return out, e
}
func (c *Client) GetEscalationRule(ctx context.Context, team, id string) (EscalationRule, error) {
	var out EscalationRule
	e := c.json(ctx, http.MethodGet, routingRulePath(team, id), nil, &out, 0)
	if e == nil {
		e = ruleValidate(out, team, id)
	}
	return out, e
}
func (c *Client) DisableEscalationRule(ctx context.Context, team, id string) error {
	return allowAbsent(c.json(ctx, http.MethodPost, routingRulePath(team, id)+"/disable", nil, nil, 0))
}

// ChannelBindingInput references delivery catalog identities explicitly. A binding's
// own ID is distinct from ChannelID, including in the URL's channel_id position.
type ChannelBindingInput struct {
	ChannelID string `json:"channel_id"`
	TargetID  string `json:"target_id"`
}
type ChannelBinding struct {
	ID string `json:"id"`
	ChannelBindingInput
	Unavailable bool `json:"unavailable"`
	// UnavailablePresent distinguishes a returned false from an omitted create field.
	UnavailablePresent bool `json:"-"`
}

func routingBindingPath(kind, owner, id string) (string, error) {
	var p string
	switch kind {
	case "team_alert":
		p = teamPath(owner) + "/alert-channels"
	case "service_alert":
		p = servicePath(owner) + "/alert-channels"
	case "team_oncall":
		p = teamPath(owner) + "/oncall-channels"
	default:
		return "", errors.New("invalid channel-binding scope")
	}
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p, nil
}
func bindingValidate(v ChannelBinding, id string) error {
	if !validResponseID(v.ID) || (id != "" && v.ID != id) || !validResponseID(v.ChannelID) || !validResponseID(v.TargetID) {
		return errors.New("OnPager returned an incomplete channel binding or unexpected identity")
	}
	return nil
}
func (c *Client) CreateChannelBinding(ctx context.Context, kind, owner string, in ChannelBindingInput) (ChannelBinding, error) {
	var receipt struct {
		ChannelBinding
		Unavailable *bool `json:"unavailable"`
	}
	p, e := routingBindingPath(kind, owner, "")
	if e != nil {
		return ChannelBinding{}, e
	}
	e = c.json(ctx, http.MethodPost, p, in, &receipt, 0)
	out := receipt.ChannelBinding
	out.UnavailablePresent = receipt.Unavailable != nil
	if receipt.Unavailable != nil {
		out.Unavailable = *receipt.Unavailable
	}
	if e == nil {
		e = bindingValidate(out, "")
	}
	return out, e
}
func (c *Client) GetChannelBinding(ctx context.Context, kind, owner, id string) (ChannelBinding, error) {
	var out ChannelBinding
	p, e := routingBindingPath(kind, owner, id)
	if e != nil {
		return out, e
	}
	e = c.json(ctx, http.MethodGet, p, nil, &out, 0)
	if e == nil {
		e = bindingValidate(out, id)
	}
	return out, e
}
func (c *Client) UpdateChannelBinding(ctx context.Context, kind, owner, id string, in ChannelBindingInput) (ChannelBinding, error) {
	var out ChannelBinding
	if kind == "team_oncall" {
		return out, errors.New("oncall channel bindings require replacement")
	}
	p, e := routingBindingPath(kind, owner, id)
	if e != nil {
		return out, e
	}
	e = c.json(ctx, http.MethodPut, p, in, &out, 0)
	if e == nil {
		e = bindingValidate(out, id)
	}
	return out, e
}
func (c *Client) DeleteChannelBinding(ctx context.Context, kind, owner, id string) error {
	p, e := routingBindingPath(kind, owner, id)
	if e != nil {
		return e
	}
	return allowAbsent(c.json(ctx, http.MethodDelete, p, nil, nil, 0))
}
