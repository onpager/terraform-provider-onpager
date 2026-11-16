package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
)

// ManagedUser adds the direct directory metadata needed to manage local users.
// Credential secrets and action hints are deliberately excluded.
type ManagedUser struct {
	User
	LocalLogin string
	Protected  bool
	Committed  bool
}

type LocalUserInput struct {
	DisplayName            string `json:"display_name"`
	Login                  string `json:"login"`
	Email                  string `json:"email,omitempty"`
	Password               string `json:"password"`
	PasswordChangeRequired *bool  `json:"password_change_required,omitempty"`
}

func (LocalUserInput) String() string   { return "[REDACTED]" }
func (LocalUserInput) GoString() string { return "[REDACTED]" }

type LocalUserPatch struct {
	DisplayName *string
	Email       *string
	EmailSet    bool
}

func (v LocalUserPatch) MarshalJSON() ([]byte, error) {
	fields := map[string]any{}
	if v.DisplayName != nil {
		fields["display_name"] = *v.DisplayName
	}
	if v.EmailSet {
		fields["email"] = v.Email
	}
	return json.Marshal(fields)
}

type LocalUserReceipt struct {
	ID        string
	Committed bool
}

func userPath(id string) string { return "/v1/users/" + url.PathEscape(id) }

// userRequest retains successful HTTP mutation outcomes even if the receipt is
// malformed. No user endpoint uses If-Match or an automatic mutation retry.
func (c *Client) userRequest(ctx context.Context, method, route string, input any) ([]byte, bool, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, false, errors.New("could not encode OnPager user request")
		}
		body = bytes.NewReader(data)
	}
	res, err := c.do(ctx, method, route, body, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	committed := success && method != http.MethodGet
	data, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		return nil, committed, errors.New("could not read OnPager response")
	}
	if len(data) > 4<<20 {
		return nil, committed, errors.New("OnPager response exceeds the 4 MiB client limit")
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
		return nil, false, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	wantStatus := http.StatusOK
	if method == http.MethodPost {
		wantStatus = http.StatusNoContent
		if route == "/v1/users" {
			wantStatus = http.StatusCreated
		}
	}
	if res.StatusCode != wantStatus || wantStatus == http.StatusNoContent && len(bytes.TrimSpace(data)) != 0 {
		return data, committed, errors.New("OnPager returned an unexpected user response status or body")
	}
	return data, committed, nil
}

func decodeManagedUser(data []byte, id string, committed, directory bool) (ManagedUser, error) {
	out := ManagedUser{Committed: committed}
	// Read identity separately so other malformed fields cannot discard a
	// successfully allocated UUID. Never adopt a different addressed UUID.
	var receipt Receipt
	_ = json.Unmarshal(data, &receipt)
	if validResponseID(receipt.ID) && (id == "" || receipt.ID == id) {
		out.ID = receipt.ID
	}
	var v struct {
		User
		Display *struct {
			LocalLogin *string `json:"local_login"`
			Protected  *bool   `json:"protected"`
		} `json:"display"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || json.Unmarshal(data, &v) != nil || fields == nil {
		return out, errors.New("OnPager returned invalid user JSON")
	}
	for _, name := range []string{"id", "display_name", "active"} {
		if raw, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return out, errors.New("OnPager returned an incomplete user")
		}
	}
	if err := validateEntity(v.ID, v.DisplayName, id); err != nil {
		return out, err
	}
	if directory {
		var display map[string]json.RawMessage
		if v.Display == nil || v.Display.Protected == nil || json.Unmarshal(fields["display"], &display) != nil {
			return out, errors.New("OnPager returned incomplete user directory metadata")
		}
		if _, present := display["local_login"]; !present {
			return out, errors.New("OnPager omitted local user identity metadata")
		}
		out.Protected = *v.Display.Protected
		if v.Display.LocalLogin != nil {
			if *v.Display.LocalLogin == "" {
				return out, errors.New("OnPager returned an empty local login")
			}
			out.LocalLogin = *v.Display.LocalLogin
		}
	}
	out.User = v.User
	return out, nil
}

func (c *Client) CreateLocalUser(ctx context.Context, in LocalUserInput) (ManagedUser, error) {
	data, committed, err := c.userRequest(ctx, http.MethodPost, "/v1/users", in)
	out, decodeErr := decodeManagedUser(data, "", committed, false)
	if err != nil {
		return out, err
	}
	return out, decodeErr
}

func (c *Client) GetManagedUser(ctx context.Context, id string) (ManagedUser, error) {
	data, _, err := c.userRequest(ctx, http.MethodGet, userPath(id), nil)
	if err != nil {
		return ManagedUser{}, err
	}
	return decodeManagedUser(data, id, false, true)
}

func (c *Client) UpdateLocalUser(ctx context.Context, id string, in LocalUserPatch) (LocalUserReceipt, error) {
	data, committed, err := c.userRequest(ctx, http.MethodPatch, userPath(id), in)
	out := LocalUserReceipt{ID: id, Committed: committed}
	if err != nil {
		return out, err
	}
	var receipt Receipt
	if json.Unmarshal(data, &receipt) != nil || validateReceipt(receipt, id) != nil {
		return out, errors.New("OnPager returned an invalid user mutation receipt")
	}
	return out, nil
}

func (c *Client) SetUserActive(ctx context.Context, id string, active bool) (LocalUserReceipt, error) {
	command := "/deactivate"
	if active {
		command = "/reactivate"
	}
	_, committed, err := c.userRequest(ctx, http.MethodPost, userPath(id)+command, nil)
	if !active {
		err = allowAbsent(err)
	}
	return LocalUserReceipt{ID: id, Committed: committed}, err
}

func (c *Client) DeactivateUser(ctx context.Context, id string) error {
	_, err := c.SetUserActive(ctx, id, false)
	return err
}

func (c *Client) ReactivateUser(ctx context.Context, id string) error {
	_, err := c.SetUserActive(ctx, id, true)
	return err
}
