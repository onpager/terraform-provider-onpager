package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"unicode/utf8"
)

type UserWebhookContactCreate struct {
	RequestID         string            `json:"request_id"`
	TemplateContactID *string           `json:"template_contact_id,omitempty"`
	WebhookID         string            `json:"webhook_id"`
	DisplayName       string            `json:"display_name"`
	Values            map[string]string `json:"values"`
}

type UserWebhookContactPatch struct {
	DisplayName        *string            `json:"display_name,omitempty"`
	Values             *map[string]string `json:"values,omitempty"`
	Enabled            *bool              `json:"enabled,omitempty"`
	ExpectedGeneration *int64             `json:"expected_generation,omitempty"`
}

type UserWebhookContact struct {
	ID, UserID, WebhookID, ETag, DisplayName string
	Enabled, Draft, Verified                 bool
	Generation                               int64
	Values                                   map[string]string
	Committed                                bool
}

var webhookContactFieldKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func ValidateWebhookContactValues(values map[string]string) error {
	if values == nil || len(values) > 20 {
		return errors.New("values must be a map of at most 20 recipient fields")
	}
	total := 0
	for k, v := range values {
		if !webhookContactFieldKey.MatchString(k) || !utf8.ValidString(v) || len(v) > 1024 {
			return errors.New("recipient keys must match [A-Za-z][A-Za-z0-9_]{0,63} and each value must be at most 1024 UTF-8 bytes")
		}
		total += len(v)
	}
	if total > 16<<10 {
		return errors.New("recipient values exceed the combined limit of 16 KiB")
	}
	return nil
}

// Control adds empty entries for omitted optional fields of the definition.
func webhookContactValuesMatch(want, got map[string]string) bool {
	for k, v := range want {
		if actual, ok := got[k]; !ok || actual != v {
			return false
		}
	}
	for k, v := range got {
		if _, configured := want[k]; !configured && v != "" {
			return false
		}
	}
	return true
}

func (c *Client) userWebhookContactRequest(ctx context.Context, method, user, id, webhook, tag string, body any) (UserWebhookContact, error) {
	var out UserWebhookContact
	if !validResponseID(user) || method != http.MethodPost && !validResponseID(id) || webhook != "" && !validResponseID(webhook) {
		return out, errors.New("user webhook contacts require nonzero canonical user, method and webhook UUIDs")
	}
	if (method == http.MethodPatch || method == http.MethodDelete) && !ValidOutgoingWebhookETag(tag) {
		return out, errors.New("a saved strong numeric method ETag is required for this operation")
	}
	route := userPath(user) + "/webhook-contacts"
	if id != "" {
		route += "/" + id
	}
	var h http.Header
	raw, status, err := c.rawJSONRequestHeaders(ctx, method, route, body, tag, &h)
	out.Committed = method == http.MethodPatch && status == http.StatusOK || method == http.MethodPost && (status == http.StatusOK || status == http.StatusCreated)
	if err != nil {
		return out, err
	}
	if method == http.MethodDelete {
		if status != http.StatusNoContent || len(bytes.TrimSpace(raw)) != 0 {
			return out, errors.New("OnPager did not confirm user webhook contact deletion with HTTP 204")
		}
		return out, nil
	}
	if status != http.StatusOK && !(method == http.MethodPost && status == http.StatusCreated) {
		return out, errors.New("OnPager returned an unexpected user webhook contact status")
	}

	// A malformed receipt can still identify a committed create. Only expose its
	// UUID after checking the complete immutable binding.
	var identity struct {
		ID        string `json:"id"`
		UserID    string `json:"user_id"`
		WebhookID string `json:"webhook_id"`
	}
	if json.Unmarshal(raw, &identity) != nil || identity.UserID != user || !validResponseID(identity.WebhookID) || webhook != "" && identity.WebhookID != webhook || !validResponseID(identity.ID) || id != "" && identity.ID != id {
		return out, errors.New("OnPager returned an unexpected user, method or webhook UUID")
	}
	out.UserID, out.ID, out.WebhookID = user, identity.ID, identity.WebhookID
	var v struct {
		DisplayName string             `json:"display_name"`
		Values      map[string]*string `json:"values"`
		Enabled     *bool              `json:"enabled"`
		Draft       *bool              `json:"draft"`
		Verified    *bool              `json:"verified"`
		Generation  *int64             `json:"generation"`
		ETag        string             `json:"etag"`
	}
	if json.Unmarshal(raw, &v) != nil || ValidateManagedContactName(v.DisplayName) != nil || v.Enabled == nil || v.Verified == nil || v.Draft == nil || v.Generation == nil || *v.Generation < 0 || v.Values == nil || *v.Draft && (*v.Verified || len(v.Values) != 0) {
		return out, errors.New("OnPager returned incomplete or invalid user webhook contact fields")
	}
	if len(h.Values("ETag")) != 1 || !ValidOutgoingWebhookETag(v.ETag) || v.ETag != h.Get("ETag") || v.ETag == tag {
		return out, errors.New("OnPager returned an invalid or inconsistent user webhook contact ETag")
	}
	values := make(map[string]string, len(v.Values))
	for k, value := range v.Values {
		if value == nil {
			return out, errors.New("OnPager returned an invalid webhook recipient value")
		}
		values[k] = *value
	}
	if err := ValidateWebhookContactValues(values); err != nil {
		return out, err
	}
	switch in := body.(type) {
	case UserWebhookContactCreate:
		// A replay returns the current method, which can have drifted since POST.
		if status == http.StatusCreated && (*v.Draft || !*v.Verified || !*v.Enabled || in.DisplayName != v.DisplayName || !webhookContactValuesMatch(in.Values, values)) {
			return out, errors.New("OnPager did not confirm the new webhook contact configuration")
		}
	case UserWebhookContactPatch:
		if in.DisplayName != nil && *in.DisplayName != v.DisplayName || in.Enabled != nil && *in.Enabled != *v.Enabled || in.Values != nil && (*v.Draft || !*v.Verified || !webhookContactValuesMatch(*in.Values, values)) {
			return out, errors.New("OnPager did not confirm the updated webhook contact configuration")
		}
	}
	out.DisplayName, out.ETag, out.Values = v.DisplayName, v.ETag, values
	out.Enabled, out.Draft, out.Verified, out.Generation = *v.Enabled, *v.Draft, *v.Verified, *v.Generation
	return out, nil
}

func (c *Client) CreateUserWebhookContact(ctx context.Context, user string, in UserWebhookContactCreate) (UserWebhookContact, error) {
	if !validResponseID(in.RequestID) || !validResponseID(in.WebhookID) || in.TemplateContactID != nil && !validResponseID(*in.TemplateContactID) {
		return UserWebhookContact{}, errors.New("create requires canonical nonzero request_id, webhook_id and any supplied template_contact_id")
	}
	if err := ValidateManagedContactName(in.DisplayName); err != nil {
		return UserWebhookContact{}, err
	}
	if err := ValidateWebhookContactValues(in.Values); err != nil {
		return UserWebhookContact{}, err
	}
	return c.userWebhookContactRequest(ctx, http.MethodPost, user, "", in.WebhookID, "", in)
}

// expectedWebhook is empty on import, before the immutable definition is known.
func (c *Client) GetUserWebhookContact(ctx context.Context, user, id, expectedWebhook string) (UserWebhookContact, error) {
	return c.userWebhookContactRequest(ctx, http.MethodGet, user, id, expectedWebhook, "", nil)
}

func (c *Client) UpdateUserWebhookContact(ctx context.Context, user, id, webhook, tag string, in UserWebhookContactPatch) (UserWebhookContact, error) {
	if !validResponseID(webhook) || in == (UserWebhookContactPatch{}) || in.ExpectedGeneration != nil && (*in.ExpectedGeneration < 0 || in.Values == nil) {
		return UserWebhookContact{}, errors.New("updating a user webhook contact requires its webhook UUID and changed fields; a draft generation requires values")
	}
	if in.DisplayName != nil {
		if err := ValidateManagedContactName(*in.DisplayName); err != nil {
			return UserWebhookContact{}, err
		}
	}
	if in.Values != nil {
		if err := ValidateWebhookContactValues(*in.Values); err != nil {
			return UserWebhookContact{}, err
		}
	}
	return c.userWebhookContactRequest(ctx, http.MethodPatch, user, id, webhook, tag, in)
}

func (c *Client) DeleteUserWebhookContact(ctx context.Context, user, id, tag string) error {
	_, err := c.userWebhookContactRequest(ctx, http.MethodDelete, user, id, "", tag, nil)
	return allowAbsent(err)
}
