package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

type NotificationDefaultContact struct {
	ID                  string `json:"id"`
	ChannelID           string `json:"channel_id"`
	DisplayName         string `json:"display_name"`
	RequireVerification bool   `json:"require_verification"`
	LookupAttribute     string `json:"lookup_attribute,omitempty"`
}

type NotificationDefaultStep struct {
	Kind        string  `json:"kind"`
	ContactID   *string `json:"contact_id,omitempty"`
	WaitSeconds *int64  `json:"wait_seconds,omitempty"`
}

type NotificationDefaultPolicy struct {
	Configured bool                      `json:"configured"`
	Steps      []NotificationDefaultStep `json:"steps"`
}

type NotificationDefaultPolicies struct {
	Default   NotificationDefaultPolicy `json:"default"`
	Important NotificationDefaultPolicy `json:"important"`
}

type UserNotificationDefaultsInput struct {
	Enabled  bool                         `json:"enabled"`
	Contacts []NotificationDefaultContact `json:"contacts"`
	Policies NotificationDefaultPolicies  `json:"policies"`
}

type UserNotificationDefaults struct {
	UserNotificationDefaultsInput
	Revision  int64 `json:"revision"`
	Committed bool  `json:"-"`
}

func EmptyUserNotificationDefaults() UserNotificationDefaultsInput {
	return UserNotificationDefaultsInput{
		Contacts: []NotificationDefaultContact{},
		Policies: NotificationDefaultPolicies{
			Default:   NotificationDefaultPolicy{Steps: []NotificationDefaultStep{}},
			Important: NotificationDefaultPolicy{Steps: []NotificationDefaultStep{}},
		},
	}
}

// IsEmpty describes ownership-relevant content independently of the observed revision.
func (v UserNotificationDefaultsInput) IsEmpty() bool {
	return !v.Enabled && len(v.Contacts) == 0 && !v.Policies.Default.Configured && !v.Policies.Important.Configured
}

// ValidateUserNotificationDefaults checks the self-contained template. Control
// checks the channel capabilities and availability at publication time.
func ValidateUserNotificationDefaults(v UserNotificationDefaultsInput) error {
	if v.Contacts == nil {
		return errors.New("contacts must be a list, which can be empty")
	}
	slots := make(map[string]bool, len(v.Contacts))
	channels := make(map[string]bool, len(v.Contacts))
	for i, contact := range v.Contacts {
		if !validResponseID(contact.ID) || !validResponseID(contact.ChannelID) || slots[contact.ID] || channels[contact.ChannelID] {
			return fmt.Errorf("contacts[%d] requires a unique slot id and a unique channel_id, both canonical nonzero UUIDs", i)
		}
		name := contact.DisplayName
		if name == "" || name != strings.TrimSpace(name) || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 {
			return fmt.Errorf("contacts[%d].display_name must contain 1 to 200 Unicode characters without surrounding whitespace", i)
		}
		slots[contact.ID], channels[contact.ChannelID] = true, true
	}
	if err := validateNotificationDefaultPolicy(v.Policies.Default, slots); err != nil {
		return fmt.Errorf("policies.default: %w", err)
	}
	if err := validateNotificationDefaultPolicy(v.Policies.Important, slots); err != nil {
		return fmt.Errorf("policies.important: %w", err)
	}
	return nil
}

func validateNotificationDefaultPolicy(policy NotificationDefaultPolicy, slots map[string]bool) error {
	if policy.Steps == nil || !policy.Configured && len(policy.Steps) > 0 {
		return errors.New("steps must be a list and must be empty when configured is false")
	}
	for i, step := range policy.Steps {
		switch step.Kind {
		case "notify":
			if step.ContactID == nil || !slots[*step.ContactID] || step.WaitSeconds != nil {
				return fmt.Errorf("steps[%d] with kind notify requires a contact_id referencing a configured slot and must omit wait_seconds", i)
			}
		case "wait":
			if step.ContactID != nil || step.WaitSeconds == nil || *step.WaitSeconds < 1 || *step.WaitSeconds > int64(math.MaxInt64/time.Second) {
				return fmt.Errorf("steps[%d] with kind wait requires wait_seconds between 1 and 9223372036 and must omit contact_id", i)
			}
		default:
			return fmt.Errorf("steps[%d].kind must be notify or wait", i)
		}
	}
	return nil
}

// Check required fields whose absence would silently become a valid Go zero value.
func completeNotificationDefaults(raw []byte) bool {
	var shape struct {
		Contacts json.RawMessage            `json:"contacts"`
		Policies map[string]json.RawMessage `json:"policies"`
	}
	return outgoingWebhookCompleteObject(raw, "enabled", "contacts", "policies", "revision") &&
		json.Unmarshal(raw, &shape) == nil &&
		outgoingWebhookCompleteArray(shape.Contacts, "id", "channel_id", "display_name", "require_verification") &&
		outgoingWebhookCompleteObject(shape.Policies["default"], "configured", "steps") &&
		outgoingWebhookCompleteObject(shape.Policies["important"], "configured", "steps")
}

func (c *Client) userNotificationDefaultsRequest(ctx context.Context, input *UserNotificationDefaultsInput) (UserNotificationDefaults, error) {
	var out UserNotificationDefaults
	method := http.MethodGet
	var body io.Reader
	if input != nil {
		if err := ValidateUserNotificationDefaults(*input); err != nil {
			return out, err
		}
		raw, err := json.Marshal(input)
		if err != nil || len(raw) > 128<<10 {
			return out, errors.New("notification defaults must encode to a JSON object of at most 128 KiB")
		}
		method, body = http.MethodPut, bytes.NewReader(raw)
	}
	res, err := c.do(ctx, method, "/v1/settings/user-notification-defaults", body, nil)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	if success && input != nil {
		out.Committed = true
		out.UserNotificationDefaultsInput = *input
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("could not read OnPager response")
	}
	if len(data) > 4<<20 {
		return out, errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if !success {
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
		return out, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	if res.StatusCode != http.StatusOK {
		return out, errors.New("OnPager returned an unexpected user notification defaults success status")
	}
	var saved UserNotificationDefaults
	if !utf8.Valid(data) || !completeNotificationDefaults(data) || json.Unmarshal(data, &saved) != nil || ValidateUserNotificationDefaults(saved.UserNotificationDefaultsInput) != nil {
		return out, errors.New("OnPager returned incomplete or invalid user notification defaults")
	}
	if saved.Revision < 0 || saved.Revision == 0 && (input != nil || !saved.IsEmpty()) {
		return out, errors.New("OnPager returned an invalid user notification defaults revision")
	}
	if input != nil && !reflect.DeepEqual(saved.UserNotificationDefaultsInput, *input) {
		return out, errors.New("OnPager did not confirm the complete requested user notification defaults")
	}
	saved.Committed = out.Committed
	return saved, nil
}

func (c *Client) GetUserNotificationDefaults(ctx context.Context) (UserNotificationDefaults, error) {
	return c.userNotificationDefaultsRequest(ctx, nil)
}

func (c *Client) ReplaceUserNotificationDefaults(ctx context.Context, in UserNotificationDefaultsInput) (UserNotificationDefaults, error) {
	return c.userNotificationDefaultsRequest(ctx, &in)
}
