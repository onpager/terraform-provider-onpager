package client

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var botUpdatePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func botScopeKind(m string) string {
	switch m {
	case "telegram", "pachca":
		return "bot_account"
	case "slack":
		return "workspace"
	case "mattermost":
		return "team"
	case "discord":
		return "guild"
	}
	return ""
}
func botText(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func validBotScope(m, s string) bool {
	return botText(s, 512) && strings.TrimSpace(s) == s && (s != "" || m == "telegram")
}
func botProviderURL(s string, credentials bool) bool {
	u, e := url.Parse(s)
	return e == nil && u.IsAbs() && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.Opaque == "" && (credentials || u.User == nil && u.String() == s) && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && botText(s, 16384)
}
func botLink(s string) bool {
	if s == "" {
		return true
	}
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == "" && u.String() == s && strings.TrimSpace(s) == s && botText(s, 4096)
}
func botSecretAllowed(m, k string) bool {
	switch k {
	case "token", "proxy_url":
		return true
	case "app_token":
		return m == "slack"
	case "action_secret":
		return m == "mattermost"
	case "webhook_secret":
		return m == "telegram" || m == "pachca"
	}
	return false
}
func validateBotSecretKeys(m, mode string, keys []string) error {
	present := map[string]bool{}
	for _, key := range keys {
		if !botSecretAllowed(m, key) || present[key] {
			return errors.New("invalid bot secret keys")
		}
		present[key] = true
	}
	if !present["token"] || m == "slack" && !present["app_token"] || m == "mattermost" && !present["action_secret"] || mode == "webhook" && !present["webhook_secret"] {
		return errors.New("required bot secrets are missing")
	}
	return nil
}

// ValidateBotInput validates public replacements and named changes without
// requiring knowledge of retained secret values. The API validates their result.
func ValidateBotInput(m string, in BotInput) error {
	invalid := errors.New("invalid bot configuration; check messenger fields, byte limits, scopes, pacing, team grants and secret actions")
	if botScopeKind(m) == "" || in.DisplayName == "" || strings.TrimSpace(in.DisplayName) != in.DisplayName || !botText(in.DisplayName, 200) || !botText(in.PublicHandle, 200) || !botLink(in.EnrollmentURL) || m == "telegram" && in.EnrollmentURL == "" || (in.Visibility != "global" && in.Visibility != "team") || len(in.TeamIDs) > 1000 || in.Visibility == "global" && len(in.TeamIDs) > 0 {
		return invalid
	}
	for _, id := range in.TeamIDs {
		if !validResponseID(id) {
			return invalid
		}
	}
	p := in.Pacing
	if p.Requests < 1 || p.Requests > 2147483647 || p.WindowMS < 1 || p.WindowMS > 86400000 || p.SearchRequests < 0 || p.SearchRequests > p.Requests/10 {
		return invalid
	}
	cfg := in.Configuration
	if m == "telegram" || m == "pachca" {
		if cfg.Mode != "" && cfg.Mode != "polling" && cfg.Mode != "webhook" {
			return invalid
		}
	} else if cfg.Mode != "" {
		return invalid
	}
	if m == "mattermost" {
		if !botProviderURL(cfg.BaseURL, false) || strings.HasSuffix(cfg.BaseURL, "/") {
			return invalid
		}
	} else if cfg.BaseURL != "" {
		return invalid
	}
	if m != "telegram" && len(cfg.AllowedUpdates) > 0 || len(cfg.AllowedUpdates) > 32 {
		return invalid
	}
	for _, update := range cfg.AllowedUpdates {
		if !botUpdatePattern.MatchString(update) {
			return invalid
		}
	}
	for key, change := range in.Secrets {
		if !botSecretAllowed(m, key) {
			return invalid
		}
		switch change.Action {
		case "keep", "delete":
			if change.Action == "delete" && (key == "token" || key == "app_token" || key == "action_secret" || key == "webhook_secret" && cfg.Mode == "webhook") {
				return invalid
			}
			if change.Value != "" {
				return invalid
			}
		case "replace":
			if change.Value == "" || strings.TrimSpace(change.Value) == "" || !botText(change.Value, 16384) || key == "proxy_url" && !botProviderURL(change.Value, true) {
				return invalid
			}
		default:
			return invalid
		}
	}
	return nil
}
func ValidateBotCreateInput(in BotCreateInput) error {
	if e := ValidateBotInput(in.Messenger, in.BotInput); e != nil {
		return e
	}
	if !validBotScope(in.Messenger, in.AddressingScopeID) || in.Messenger == "telegram" && in.AddressingScopeID != "" {
		return errors.New("omit addressing_scope_id for Telegram; other messengers require their immutable scope ID")
	}
	keys := []string{}
	for key, change := range in.Secrets {
		if change.Action == "replace" {
			keys = append(keys, key)
		}
	}
	return validateBotSecretKeys(in.Messenger, in.Configuration.Mode, keys)
}
