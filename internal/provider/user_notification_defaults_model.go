package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type userNotificationDefaultsModel struct {
	ID       types.String                       `tfsdk:"id"`
	Revision types.Int64                        `tfsdk:"revision"`
	Enabled  bool                               `tfsdk:"enabled"`
	Contacts []*notificationDefaultContactModel `tfsdk:"contacts"`
	Policies notificationDefaultPoliciesModel   `tfsdk:"policies"`
}

type notificationDefaultContactModel struct {
	ID                  string  `tfsdk:"id"`
	ChannelID           string  `tfsdk:"channel_id"`
	DisplayName         string  `tfsdk:"display_name"`
	RequireVerification *bool   `tfsdk:"require_verification"`
	LookupAttribute     *string `tfsdk:"lookup_attribute"`
}

type notificationDefaultPoliciesModel struct {
	Default   notificationDefaultPolicyModel `tfsdk:"default"`
	Important notificationDefaultPolicyModel `tfsdk:"important"`
}

type notificationDefaultPolicyModel struct {
	Configured bool                            `tfsdk:"configured"`
	Steps      []*notificationDefaultStepModel `tfsdk:"steps"`
}

type notificationDefaultStepModel struct {
	Kind        string  `tfsdk:"kind"`
	ContactID   *string `tfsdk:"contact_id"`
	WaitSeconds *int64  `tfsdk:"wait_seconds"`
}

func (p notificationDefaultPolicyModel) input() (client.NotificationDefaultPolicy, error) {
	out := client.NotificationDefaultPolicy{Configured: p.Configured}
	if p.Steps != nil {
		out.Steps = make([]client.NotificationDefaultStep, len(p.Steps))
	}
	for i, step := range p.Steps {
		if step == nil {
			return out, fmt.Errorf("steps[%d] must be a non-null policy step", i)
		}
		out.Steps[i] = client.NotificationDefaultStep(*step)
	}
	return out, nil
}

func (m userNotificationDefaultsModel) input() (client.UserNotificationDefaultsInput, error) {
	out := client.UserNotificationDefaultsInput{Enabled: m.Enabled}
	if m.Contacts == nil {
		return out, errors.New("contacts must be a list. Use [] for an empty contact list")
	}
	out.Contacts = make([]client.NotificationDefaultContact, len(m.Contacts))
	for i, c := range m.Contacts {
		if c == nil {
			return out, fmt.Errorf("contacts[%d] must be a non-null contact slot", i)
		}
		contact := client.NotificationDefaultContact{ID: c.ID, ChannelID: c.ChannelID, DisplayName: c.DisplayName, RequireVerification: true}
		if c.RequireVerification != nil {
			contact.RequireVerification = *c.RequireVerification
		}
		if c.LookupAttribute != nil {
			contact.LookupAttribute = *c.LookupAttribute
		}
		out.Contacts[i] = contact
	}

	var err error
	if out.Policies.Default, err = m.Policies.Default.input(); err != nil {
		return out, fmt.Errorf("policies.default: %w", err)
	}
	if out.Policies.Important, err = m.Policies.Important.input(); err != nil {
		return out, fmt.Errorf("policies.important: %w", err)
	}
	return out, client.ValidateUserNotificationDefaults(out)
}

// The config is checked when its inputs are known. Apply uses the resolved plan
// and repeats the checks before the create ownership read or any write.
func notificationDefaultsInput(ctx context.Context, source interface {
	Get(context.Context, any) diag.Diagnostics
}, d *diag.Diagnostics) client.UserNotificationDefaultsInput {
	var m userNotificationDefaultsModel
	d.Append(source.Get(ctx, &m)...)
	if d.HasError() {
		return client.UserNotificationDefaultsInput{}
	}
	in, err := m.input()
	if err != nil {
		d.AddError("Invalid user notification defaults", err.Error())
	}
	return in
}

func notificationDefaultPolicyState(v client.NotificationDefaultPolicy) notificationDefaultPolicyModel {
	out := notificationDefaultPolicyModel{Configured: v.Configured, Steps: make([]*notificationDefaultStepModel, len(v.Steps))}
	for i, step := range v.Steps {
		row := notificationDefaultStepModel(step)
		out.Steps[i] = &row
	}
	return out
}

func notificationDefaultsState(v client.UserNotificationDefaultsInput, revision types.Int64) userNotificationDefaultsModel {
	m := userNotificationDefaultsModel{
		ID: types.StringValue("tenant"), Revision: revision, Enabled: v.Enabled,
		Contacts: make([]*notificationDefaultContactModel, len(v.Contacts)),
		Policies: notificationDefaultPoliciesModel{Default: notificationDefaultPolicyState(v.Policies.Default), Important: notificationDefaultPolicyState(v.Policies.Important)},
	}
	for i, c := range v.Contacts {
		m.Contacts[i] = &notificationDefaultContactModel{ID: c.ID, ChannelID: c.ChannelID, DisplayName: c.DisplayName, RequireVerification: &c.RequireVerification, LookupAttribute: &c.LookupAttribute}
	}
	return m
}
