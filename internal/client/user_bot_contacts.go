package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// UserBotContact is an employee's method in the administrative bot-contact API.
type UserBotContact struct {
	ID          string
	BotID       string
	DisplayName string
	RecipientID string
	Verified    bool
	Created     bool
	Committed   bool
}

type UserBotContactInput struct {
	BotID       *string `json:"bot_id,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	RecipientID *string `json:"recipient_id,omitempty"`
	Verified    *bool   `json:"verified,omitempty"`
}

func validContactName(s string) bool {
	return s != "" && len(s) <= 200 && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func validContactRecipient(s string) bool {
	return s != "" && len(s) <= 1024 && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

// Validate checks only supplied fields. The server can confirm an existing
// recipient even when a POST or PATCH omits recipient_id.
func (in UserBotContactInput) Validate() error {
	switch {
	case in.BotID != nil && !validResponseID(*in.BotID):
		return errors.New("bot_id must be a nonzero canonical lowercase UUID")
	case in.DisplayName != nil && !validContactName(*in.DisplayName):
		return errors.New("display_name must be 1–200 UTF-8 bytes without NUL or surrounding whitespace")
	case in.RecipientID != nil && !validContactRecipient(*in.RecipientID):
		return errors.New("recipient_id must be 1–1024 UTF-8 bytes without whitespace or control characters")
	}
	return nil
}

// Matches checks the explicitly declared fields without imposing values for
// omitted recipient and verification settings on existing methods.
func (in UserBotContactInput) Matches(v UserBotContact) bool {
	return (in.BotID == nil || *in.BotID == v.BotID) &&
		(in.DisplayName == nil || *in.DisplayName == v.DisplayName) &&
		(in.RecipientID == nil || *in.RecipientID == v.RecipientID) &&
		(in.Verified == nil || *in.Verified == v.Verified)
}

func (c *Client) userBotContactRequest(ctx context.Context, method, user, id string, in *UserBotContactInput) (UserBotContact, error) {
	var out UserBotContact
	if !validResponseID(user) || method != http.MethodPost && !validResponseID(id) {
		return out, errors.New("user bot contacts require canonical nonzero user and method UUIDs")
	}
	route := userPath(user) + "/bot-contacts"
	if id != "" {
		route += "/" + id
	}
	var body any
	if in != nil {
		if err := in.Validate(); err != nil {
			return out, err
		}
		body = in
	}
	raw, status, err := c.unversionedJSONRequest(ctx, method, route, body)
	out.Created = method == http.MethodPost && status == http.StatusCreated
	out.Committed = status >= 200 && status < 300 && method != http.MethodGet
	if out.Committed {
		out.ID = id
	}
	if err != nil {
		return out, err
	}
	if method == http.MethodDelete {
		if status != http.StatusNoContent || len(bytes.TrimSpace(raw)) != 0 {
			return out, errors.New("OnPager did not confirm user bot contact deletion with an empty HTTP 204 response")
		}
		return out, nil
	}

	// Decode the method UUID independently: a malformed field in an otherwise
	// successful upsert must not discard a recoverable identity.
	var identity struct {
		ID    string          `json:"id"`
		BotID json.RawMessage `json:"bot_id"`
	}
	if json.Unmarshal(raw, &identity) != nil || !validResponseID(identity.ID) || id != "" && id != identity.ID {
		return out, errors.New("OnPager returned an invalid user bot contact identity")
	}
	var saved UserBotContact
	if len(identity.BotID) > 0 && (json.Unmarshal(identity.BotID, &saved.BotID) != nil || !validResponseID(saved.BotID) || in != nil && in.BotID != nil && saved.BotID != *in.BotID) {
		return out, errors.New("OnPager returned an unexpected bot UUID for the contact")
	}
	out.ID = identity.ID
	if status != http.StatusOK && !(method == http.MethodPost && status == http.StatusCreated) {
		return out, errors.New("OnPager returned an unexpected user bot contact response status")
	}
	var fields struct {
		DisplayName string          `json:"display_name"`
		RecipientID json.RawMessage `json:"recipient_id"`
		Verified    *bool           `json:"verified"`
	}
	if saved.BotID == "" || json.Unmarshal(raw, &fields) != nil || !validContactName(fields.DisplayName) || fields.Verified == nil {
		return out, errors.New("OnPager returned an incomplete or invalid user bot contact")
	}
	if len(fields.RecipientID) > 0 && (json.Unmarshal(fields.RecipientID, &saved.RecipientID) != nil || !validContactRecipient(saved.RecipientID)) || *fields.Verified && saved.RecipientID == "" {
		return out, errors.New("OnPager returned an invalid user bot contact recipient or verification")
	}
	saved.ID, saved.Created, saved.Committed = out.ID, out.Created, out.Committed
	saved.DisplayName, saved.Verified = fields.DisplayName, *fields.Verified
	if out.Created && (in.RecipientID == nil && saved.RecipientID != "" || in.Verified == nil && saved.Verified) {
		return out, errors.New("OnPager returned an unexpected recipient or verification for a newly created bot contact")
	}
	if in != nil && !in.Matches(saved) {
		return out, errors.New("OnPager did not confirm the requested user bot contact fields")
	}
	return saved, nil
}

func (c *Client) UpsertUserBotContact(ctx context.Context, user string, in UserBotContactInput) (UserBotContact, error) {
	if in.BotID == nil || in.DisplayName == nil {
		return UserBotContact{}, errors.New("a bot UUID and display name are required to declare a user bot contact")
	}
	return c.userBotContactRequest(ctx, http.MethodPost, user, "", &in)
}
func (c *Client) GetUserBotContact(ctx context.Context, user, id string) (UserBotContact, error) {
	return c.userBotContactRequest(ctx, http.MethodGet, user, id, nil)
}
func (c *Client) UpdateUserBotContact(ctx context.Context, user, id string, in UserBotContactInput) (UserBotContact, error) {
	return c.userBotContactRequest(ctx, http.MethodPatch, user, id, &in)
}
func (c *Client) DeleteUserBotContact(ctx context.Context, user, id string) error {
	_, err := c.userBotContactRequest(ctx, http.MethodDelete, user, id, nil)
	return allowAbsent(err)
}
