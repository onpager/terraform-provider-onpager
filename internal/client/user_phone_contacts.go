package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// UserPhoneContact contains only the public projection of the phone binding.
type UserPhoneContact struct {
	ID, UserID, ChannelID, TargetID string
	DisplayName, MaskedPhone        string
	Verified, Enabled, Draft        bool
	Generation                      int64
	Created, Committed              bool
}

type UserPhoneContactInput struct {
	RequestID         *string `json:"request_id,omitempty"`
	TemplateContactID *string `json:"template_contact_id,omitempty"`
	ChannelID         *string `json:"channel_id,omitempty"`
	DisplayName       *string `json:"display_name,omitempty"`
	Phone             *string `json:"phone,omitempty"`
	Verified          *bool   `json:"verified,omitempty"`
}

var managedPhoneNumber = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
var maskedPhoneNumber = regexp.MustCompile(`^\+…[0-9]{4}$`)

func (in UserPhoneContactInput) Validate() error {
	if in.DisplayName != nil {
		if err := ValidateManagedContactName(*in.DisplayName); err != nil {
			return err
		}
	}
	switch {
	case in.RequestID != nil && !validResponseID(*in.RequestID):
		return errors.New("request_id must be a nonzero canonical lowercase UUID")
	case in.TemplateContactID != nil && !validResponseID(*in.TemplateContactID):
		return errors.New("template_contact_id must be a nonzero canonical lowercase UUID")
	case in.ChannelID != nil && !validResponseID(*in.ChannelID):
		return errors.New("channel_id must be a nonzero canonical lowercase UUID")
	case in.Phone != nil && !managedPhoneNumber.MatchString(*in.Phone):
		return errors.New("phone must be in E.164 format with a plus sign and 8–15 digits")
	case in.Verified != nil && *in.Verified && (in.Phone == nil || in.ChannelID == nil):
		return errors.New("confirmation requires the exact phone and channel_id")
	}
	return nil
}

func (c *Client) CreateUserPhoneContact(ctx context.Context, user string, in UserPhoneContactInput) (UserPhoneContact, error) {
	if in.RequestID == nil || in.ChannelID == nil || in.DisplayName == nil || in.Phone == nil {
		return UserPhoneContact{}, errors.New("request_id, channel_id, display_name and phone are required to create a phone contact")
	}
	return c.userPhoneContactRequest(ctx, http.MethodPost, user, "", &in)
}

func (c *Client) GetUserPhoneContact(ctx context.Context, user, id string) (UserPhoneContact, error) {
	return c.userPhoneContactRequest(ctx, http.MethodGet, user, id, nil)
}

func (c *Client) UpdateUserPhoneContact(ctx context.Context, user, id string, in UserPhoneContactInput) (UserPhoneContact, error) {
	if in.RequestID != nil || in.TemplateContactID != nil {
		return UserPhoneContact{}, errors.New("request_id and template_contact_id are creation-only")
	}
	return c.userPhoneContactRequest(ctx, http.MethodPatch, user, id, &in)
}

func (c *Client) DeleteUserPhoneContact(ctx context.Context, user, id string) error {
	_, err := c.userPhoneContactRequest(ctx, http.MethodDelete, user, id, nil)
	return allowAbsent(err)
}

func (c *Client) userPhoneContactRequest(ctx context.Context, method, user, id string, in *UserPhoneContactInput) (UserPhoneContact, error) {
	var result UserPhoneContact
	if !validResponseID(user) || method != http.MethodPost && !validResponseID(id) {
		return result, errors.New("user phone contacts require canonical nonzero user and method UUIDs")
	}
	route := userPath(user) + "/phone-contacts"
	if id != "" {
		route += "/" + id
	}
	var body any
	if in != nil {
		if err := in.Validate(); err != nil {
			return result, err
		}
		body = in
	}
	raw, status, err := c.unversionedJSONRequest(ctx, method, route, body)
	result.Created = method == http.MethodPost && status == http.StatusCreated
	result.Committed = method != http.MethodGet && status >= 200 && status < 300
	if err != nil {
		return result, err
	}
	if method == http.MethodDelete {
		if status != http.StatusNoContent || len(bytes.TrimSpace(raw)) != 0 {
			return result, errors.New("OnPager did not confirm user phone contact deletion with an empty HTTP 204 response")
		}
		return result, nil
	}

	// Recover the method only when the receipt establishes the expected scope,
	// even when a different field in the committed response is malformed.
	var scope struct {
		ID        string `json:"id"`
		UserID    string `json:"user_id"`
		ChannelID string `json:"channel_id"`
	}
	if json.Unmarshal(raw, &scope) != nil || !validResponseID(scope.ID) || scope.UserID != user || !validResponseID(scope.ChannelID) ||
		id != "" && scope.ID != id || in != nil && in.ChannelID != nil && scope.ChannelID != *in.ChannelID {
		return result, errors.New("OnPager returned an invalid user phone contact identity or channel")
	}
	result.ID, result.UserID, result.ChannelID = scope.ID, scope.UserID, scope.ChannelID
	if status != http.StatusOK && !result.Created {
		return result, errors.New("OnPager returned an unexpected user phone contact response status")
	}
	var card struct {
		TargetID    string  `json:"target_id"`
		DisplayName string  `json:"display_name"`
		MaskedPhone *string `json:"masked_phone"`
		Verified    *bool   `json:"verified"`
		Enabled     *bool   `json:"enabled"`
		Draft       *bool   `json:"draft"`
		Generation  *int64  `json:"generation"`
	}
	if json.Unmarshal(raw, &card) != nil || !validResponseID(card.TargetID) || ValidateManagedContactName(card.DisplayName) != nil ||
		card.MaskedPhone == nil || card.Enabled == nil || card.Verified == nil || card.Draft == nil || card.Generation == nil || *card.Generation < 0 {
		return result, errors.New("OnPager returned an incomplete or invalid user phone contact")
	}
	if *card.Draft {
		if *card.MaskedPhone != "" || *card.Verified {
			return result, errors.New("OnPager returned an inconsistent phone draft")
		}
	} else if !maskedPhoneNumber.MatchString(*card.MaskedPhone) {
		return result, errors.New("OnPager returned an invalid masked phone")
	}

	// A 200 POST is indistinguishably a draft fill or a replay of a live
	// method which another writer may already have changed. Only a fresh 201
	// or a scoped PATCH is authoritative about the address submitted here.
	if result.Created || method == http.MethodPatch {
		if in.DisplayName != nil && card.DisplayName != *in.DisplayName ||
			in.Phone != nil && (*card.Draft || !strings.HasSuffix(*in.Phone, strings.TrimPrefix(*card.MaskedPhone, "+…"))) ||
			in.Verified != nil && *card.Verified != *in.Verified ||
			result.Created && (!*card.Enabled || in.Verified == nil && *card.Verified) {
			return result, errors.New("OnPager did not confirm the requested phone contact fields")
		}
	}
	result.TargetID, result.DisplayName, result.MaskedPhone = card.TargetID, card.DisplayName, *card.MaskedPhone
	result.Enabled, result.Verified, result.Draft, result.Generation = *card.Enabled, *card.Verified, *card.Draft, *card.Generation
	return result, nil
}
