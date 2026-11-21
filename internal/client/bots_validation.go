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
func validBotProxyURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.IsAbs() && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.Opaque == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && botText(s, 16384)
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
	return validateBotInput(m, in, true)
}

func validateBotInput(m string, in BotInput, writing bool) error {
	if botScopeKind(m) == "" {
		return errors.New("messenger must be telegram, pachca, slack, mattermost or discord")
	}
	if in.DisplayName == "" || strings.TrimSpace(in.DisplayName) != in.DisplayName || !botText(in.DisplayName, 200) {
		return errors.New("display_name must be nonempty valid UTF-8, at most 200 bytes, without control characters or surrounding whitespace")
	}
	if !botText(in.PublicHandle, 200) {
		return errors.New("public_handle must be valid UTF-8, at most 200 bytes, without control characters")
	}
	// Control can return an existing Pachca bot with an empty link. The next
	// save must supply it, even if the bot is disabled.
	if (m == "telegram" || m == "pachca" && writing) && in.EnrollmentURL == "" {
		return errors.New("enrollment_url is required for Telegram and Pachca, including disabled bots")
	}
	if !botLink(in.EnrollmentURL) {
		return errors.New("enrollment_url must be an HTTPS URL without credentials, control characters or surrounding whitespace, at most 4096 UTF-8 bytes")
	}
	if in.Visibility != "global" && in.Visibility != "team" {
		return errors.New("visibility must be global or team")
	}
	if len(in.TeamIDs) > 1000 {
		return errors.New("team_ids must contain at most 1000 team UUIDs")
	}
	if in.Visibility == "global" && len(in.TeamIDs) > 0 {
		return errors.New("team_ids must be empty when visibility is global")
	}
	for _, id := range in.TeamIDs {
		if !validResponseID(id) {
			return errors.New("team_ids must contain canonical nonzero lowercase team UUIDs")
		}
	}
	p := in.Pacing
	if p.Requests < 1 || p.Requests > 2147483647 {
		return errors.New("pacing.requests must be between 1 and 2147483647")
	}
	if p.WindowMS < 1 || p.WindowMS > 86400000 {
		return errors.New("pacing.window_ms must be between 1 and 86400000 milliseconds")
	}
	if p.SearchRequests < 0 || p.SearchRequests > p.Requests/10 {
		return errors.New("pacing.search_requests must be between 0 and floor(pacing.requests/10)")
	}
	cfg := in.Configuration
	if m == "telegram" || m == "pachca" {
		if cfg.Mode != "" && cfg.Mode != "polling" && cfg.Mode != "webhook" {
			return errors.New("configuration.mode must be polling or webhook for Telegram/Pachca")
		}
	} else if cfg.Mode != "" {
		return errors.New("configuration.mode must be omitted or empty for Slack, Mattermost and Discord")
	}
	if _, err := NormalizeProviderBaseURL(cfg.BaseURL); err != nil {
		return err
	}
	if m == "mattermost" && cfg.BaseURL == "" {
		return errors.New("configuration.base_url is required for Mattermost")
	}
	if m != "telegram" && len(cfg.AllowedUpdates) > 0 {
		return errors.New("configuration.allowed_updates must be omitted or empty except for Telegram")
	}
	if len(cfg.AllowedUpdates) > 32 {
		return errors.New("configuration.allowed_updates must contain at most 32 update names")
	}
	for _, update := range cfg.AllowedUpdates {
		if !botUpdatePattern.MatchString(update) {
			return errors.New("configuration.allowed_updates names must match ^[a-z][a-z0-9_]{0,63}$")
		}
	}
	for key, change := range in.Secrets {
		if !botSecretAllowed(m, key) {
			return errors.New("secrets contains a key unsupported by the configured messenger; use token/proxy_url, Slack app_token, Mattermost action_secret or Telegram/Pachca webhook_secret")
		}
		// Only supported, fixed key names enter diagnostics; never include values
		// or an arbitrary user-supplied map key, which may itself contain a secret.
		field := "secrets." + key
		switch change.Action {
		case "keep", "delete":
			if change.Action == "delete" && (key == "token" || key == "app_token" || key == "action_secret" || key == "webhook_secret" && cfg.Mode == "webhook") {
				return errors.New(field + ".action cannot be delete while this secret is required by the configured messenger and transport")
			}
			if change.Value != "" {
				return errors.New(field + ".value must be omitted for keep/delete actions")
			}
		case "replace":
			if change.Value == "" || strings.TrimSpace(change.Value) == "" || !botText(change.Value, 16384) {
				return errors.New(field + ".value must be nonempty valid UTF-8, at most 16384 bytes, without control characters or whitespace-only text when action is replace")
			}
			if key == "proxy_url" && !validBotProxyURL(change.Value) {
				return errors.New(field + ".value must be an HTTP(S) proxy URL without query or fragment")
			}
		default:
			return errors.New(field + ".action must be keep, replace or delete")
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
