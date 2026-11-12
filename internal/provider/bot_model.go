package provider

import (
	"errors"
	"reflect"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

type botModel struct {
	ID            types.String `tfsdk:"id"`
	Messenger     types.String `tfsdk:"messenger"`
	DisplayName   types.String `tfsdk:"display_name"`
	PublicHandle  types.String `tfsdk:"public_handle"`
	EnrollmentURL types.String `tfsdk:"enrollment_url"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Visibility    types.String `tfsdk:"visibility"`
	TeamIDs       types.Set    `tfsdk:"team_ids"`
	Configuration types.Object `tfsdk:"configuration"`
	Pacing        types.Object `tfsdk:"pacing"`
	ScopeID       types.String `tfsdk:"addressing_scope_id"`
	ScopeKind     types.String `tfsdk:"addressing_scope_kind"`
	Secrets       types.Map    `tfsdk:"secrets"`
	SecretKeys    types.Set    `tfsdk:"secret_keys"`
	CallbackURL   types.String `tfsdk:"callback_url"`
	Runtime       types.Object `tfsdk:"runtime"`
}

var botConfigurationTypes = map[string]attr.Type{"mode": types.StringType, "base_url": types.StringType, "allowed_updates": types.SetType{ElemType: types.StringType}}
var botPacingTypes = map[string]attr.Type{"requests": types.Int64Type, "window_ms": types.Int64Type, "search_requests": types.Int64Type}
var botSecretTypes = map[string]attr.Type{"action": types.StringType, "value": types.StringType}
var botSecretType = types.ObjectType{AttrTypes: botSecretTypes}
var botRuntimeTypes = map[string]attr.Type{"state": types.StringType, "error_code": types.StringType, "last_probe_at": types.StringType, "observed_at": types.StringType}

func botStrings(ss []string) types.Set {
	v := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		v = append(v, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, v)
}
func botString(a map[string]attr.Value, k string) string {
	v, _ := a[k].(types.String)
	return v.ValueString()
}
func botInt(a map[string]attr.Value, k string) int64 {
	v, _ := a[k].(types.Int64)
	return v.ValueInt64()
}
func botUnknown(v attr.Value) bool {
	if v == nil || v.IsUnknown() {
		return true
	}
	switch v := v.(type) {
	case types.Object:
		for _, x := range v.Attributes() {
			if botUnknown(x) {
				return true
			}
		}
	case types.Set:
		for _, x := range v.Elements() {
			if botUnknown(x) {
				return true
			}
		}
	case types.Map:
		for _, x := range v.Elements() {
			if botUnknown(x) {
				return true
			}
		}
	}
	return false
}
func botDefaultConfiguration(m string) types.Object {
	mode := ""
	updates := []string{}
	if m == "telegram" || m == "pachca" {
		mode = "polling"
	}
	if m == "telegram" {
		updates = []string{"callback_query", "message", "my_chat_member"}
	}
	return types.ObjectValueMust(botConfigurationTypes, map[string]attr.Value{"mode": types.StringValue(mode), "base_url": types.StringValue(""), "allowed_updates": botStrings(updates)})
}
func botDefaultPacing() types.Object {
	return types.ObjectValueMust(botPacingTypes, map[string]attr.Value{"requests": types.Int64Value(10), "window_ms": types.Int64Value(1000), "search_requests": types.Int64Value(0)})
}

// Defaults are applied from configuration presence, never over explicit unknowns.
func botDefaults(m botModel) botModel {
	if m.PublicHandle.IsNull() {
		m.PublicHandle = types.StringValue("")
	}
	if m.EnrollmentURL.IsNull() {
		m.EnrollmentURL = types.StringValue("")
	}
	if m.Enabled.IsNull() {
		m.Enabled = types.BoolValue(true)
	}
	if m.Visibility.IsNull() {
		m.Visibility = types.StringValue("global")
	}
	if m.TeamIDs.IsNull() {
		m.TeamIDs = botStrings(nil)
	}
	if m.Pacing.IsNull() {
		m.Pacing = botDefaultPacing()
	} else if !m.Pacing.IsUnknown() {
		a := m.Pacing.Attributes()
		def := botDefaultPacing().Attributes()
		for k, v := range a {
			if v.IsNull() {
				a[k] = def[k]
			}
		}
		m.Pacing = types.ObjectValueMust(botPacingTypes, a)
	}
	if !m.Messenger.IsNull() && !m.Messenger.IsUnknown() {
		def := botDefaultConfiguration(m.Messenger.ValueString()).Attributes()
		if m.Configuration.IsNull() {
			m.Configuration = botDefaultConfiguration(m.Messenger.ValueString())
		} else if !m.Configuration.IsUnknown() {
			a := m.Configuration.Attributes()
			for k, v := range a {
				if v.IsNull() {
					a[k] = def[k]
				}
			}
			m.Configuration = types.ObjectValueMust(botConfigurationTypes, a)
		}
	}
	return m
}
func botInput(m botModel) (client.BotInput, error) {
	var out client.BotInput
	for _, v := range []attr.Value{m.Messenger, m.DisplayName, m.PublicHandle, m.EnrollmentURL, m.Enabled, m.Visibility, m.TeamIDs, m.Configuration, m.Pacing, m.Secrets} {
		if botUnknown(v) {
			return out, errors.New("bot configuration and secret actions must be known before apply")
		}
	}
	if m.Messenger.IsNull() || m.DisplayName.IsNull() || m.Enabled.IsNull() || m.Visibility.IsNull() || m.Configuration.IsNull() || m.Pacing.IsNull() {
		return out, errors.New("bot required configuration cannot be null")
	}
	cfg := m.Configuration.Attributes()
	p := m.Pacing.Attributes()
	updates, ok := cfg["allowed_updates"].(types.Set)
	if !ok || updates.IsNull() {
		return out, errors.New("bot allowed_updates must be a known set")
	}
	out = client.BotInput{BotSettings: client.BotSettings{DisplayName: m.DisplayName.ValueString(), PublicHandle: m.PublicHandle.ValueString(), EnrollmentURL: m.EnrollmentURL.ValueString(), Enabled: m.Enabled.ValueBool(), Visibility: m.Visibility.ValueString(), Configuration: client.BotConfiguration{Mode: botString(cfg, "mode"), BaseURL: botString(cfg, "base_url")}, Pacing: client.BotPacing{Requests: botInt(p, "requests"), WindowMS: botInt(p, "window_ms"), SearchRequests: botInt(p, "search_requests")}, TeamIDs: []string{}}, Secrets: map[string]client.BotSecretChange{}}
	for _, v := range updates.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsNull() {
			return out, errors.New("bot allowed_updates cannot contain null")
		}
		out.Configuration.AllowedUpdates = append(out.Configuration.AllowedUpdates, s.ValueString())
	}
	for _, v := range m.TeamIDs.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsNull() {
			return out, errors.New("bot team_ids cannot contain null")
		}
		out.TeamIDs = append(out.TeamIDs, s.ValueString())
	}
	if (m.Messenger.ValueString() == "telegram" || m.Messenger.ValueString() == "pachca") && out.Configuration.Mode == "" {
		return out, errors.New("transport mode must not be explicitly empty; omit it to use polling")
	}
	if m.Messenger.ValueString() == "telegram" && (out.Configuration.Mode == "" || len(out.Configuration.AllowedUpdates) == 0) {
		return out, errors.New("Telegram mode and allowed_updates must not be explicitly empty; omit them to use defaults")
	}
	sort.Strings(out.TeamIDs)
	sort.Strings(out.Configuration.AllowedUpdates)
	if !m.Secrets.IsNull() {
		for key, value := range m.Secrets.Elements() {
			obj, ok := value.(types.Object)
			if !ok || obj.IsNull() {
				return out, errors.New("bot secret actions cannot be null")
			}
			a := obj.Attributes()
			action, ok := a["action"].(types.String)
			if !ok || action.IsNull() {
				return out, errors.New("bot secret action is required")
			}
			out.Secrets[key] = client.BotSecretChange{Action: action.ValueString(), Value: botString(a, "value")}
		}
	}
	return out, client.ValidateBotInput(m.Messenger.ValueString(), out)
}
func botChangedSecrets(in client.BotInput, plan, prior botModel) client.BotInput {
	present := map[string]bool{}
	for _, v := range prior.SecretKeys.Elements() {
		s, _ := v.(types.String)
		present[s.ValueString()] = true
	}
	old := prior.Secrets.Elements()
	for key, change := range in.Secrets {
		if change.Action == "keep" {
			delete(in.Secrets, key)
			continue
		}
		previous, exists := old[key]
		planned := plan.Secrets.Elements()[key]
		if exists && planned.Equal(previous) && (change.Action == "replace" && present[key] || change.Action == "delete" && !present[key]) {
			delete(in.Secrets, key)
		}
	}
	return in
}
func botCommittedState(plan, prior botModel, id string, in client.BotInput, create bool) botModel {
	m := plan
	m.ID = types.StringValue(id)
	if create {
		scope := ""
		if m.Messenger.ValueString() != "telegram" {
			scope = m.ScopeID.ValueString()
		}
		m.ScopeID = types.StringValue(scope)
		k := map[string]string{"telegram": "bot_account", "pachca": "bot_account", "slack": "workspace", "mattermost": "team", "discord": "guild"}[m.Messenger.ValueString()]
		m.ScopeKind = types.StringValue(k)
		m.CallbackURL = types.StringValue("")
	} else {
		m.ScopeID = prior.ScopeID
		m.ScopeKind = prior.ScopeKind
		m.CallbackURL = prior.CallbackURL
	}
	present := map[string]bool{}
	if !create {
		for _, v := range prior.SecretKeys.Elements() {
			s, _ := v.(types.String)
			present[s.ValueString()] = true
		}
	}
	for key, ch := range in.Secrets {
		if ch.Action == "replace" {
			present[key] = true
		} else if ch.Action == "delete" {
			delete(present, key)
		}
	}
	keys := []string{}
	for key := range present {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	m.SecretKeys = botStrings(keys)
	state := "starting"
	if !in.Enabled {
		state = "disabled"
	}
	m.Runtime = types.ObjectValueMust(botRuntimeTypes, map[string]attr.Value{"state": types.StringValue(state), "error_code": types.StringValue(""), "last_probe_at": types.StringValue(""), "observed_at": types.StringValue("")})
	return m
}
func botState(v client.Bot, prior botModel) (botModel, diag.Diagnostics) {
	m := botModel{ID: types.StringValue(v.ID), Messenger: types.StringValue(v.Messenger), DisplayName: types.StringValue(v.DisplayName), PublicHandle: types.StringValue(v.PublicHandle), EnrollmentURL: types.StringValue(v.EnrollmentURL), Enabled: types.BoolValue(v.Enabled), Visibility: types.StringValue(v.Visibility), TeamIDs: botStrings(v.TeamIDs), Configuration: types.ObjectValueMust(botConfigurationTypes, map[string]attr.Value{"mode": types.StringValue(v.Configuration.Mode), "base_url": types.StringValue(v.Configuration.BaseURL), "allowed_updates": botStrings(v.Configuration.AllowedUpdates)}), Pacing: types.ObjectValueMust(botPacingTypes, map[string]attr.Value{"requests": types.Int64Value(v.Pacing.Requests), "window_ms": types.Int64Value(v.Pacing.WindowMS), "search_requests": types.Int64Value(v.Pacing.SearchRequests)}), ScopeID: types.StringValue(v.AddressingScope.ID), ScopeKind: types.StringValue(v.AddressingScope.Kind), Secrets: prior.Secrets, SecretKeys: botStrings(v.SecretKeys), CallbackURL: types.StringValue(v.CallbackURL), Runtime: types.ObjectValueMust(botRuntimeTypes, map[string]attr.Value{"state": types.StringValue(v.Runtime.State), "error_code": types.StringValue(v.Runtime.ErrorCode), "last_probe_at": types.StringValue(v.Runtime.LastProbeAt), "observed_at": types.StringValue(v.Runtime.ObservedAt)})}
	if m.Secrets.IsUnknown() {
		m.Secrets = types.MapNull(botSecretType)
	} else if !m.Secrets.IsNull() {
		present := map[string]bool{}
		for _, key := range v.SecretKeys {
			present[key] = true
		}
		values := m.Secrets.Elements()
		for key, value := range values {
			obj, ok := value.(types.Object)
			if !ok || obj.IsUnknown() || obj.IsNull() {
				continue
			}
			a := obj.Attributes()
			if !present[key] && botString(a, "action") == "replace" {
				a["value"] = types.StringNull()
				values[key] = types.ObjectValueMust(botSecretTypes, a)
			}
		}
		m.Secrets = types.MapValueMust(botSecretType, values)
	}
	return m, nil
}
func botSettingsEqual(a, b client.BotSettings) bool {
	a.TeamIDs = append([]string{}, a.TeamIDs...)
	b.TeamIDs = append([]string{}, b.TeamIDs...)
	sort.Strings(a.TeamIDs)
	sort.Strings(b.TeamIDs)
	a.Configuration.AllowedUpdates = append([]string{}, a.Configuration.AllowedUpdates...)
	b.Configuration.AllowedUpdates = append([]string{}, b.Configuration.AllowedUpdates...)
	sort.Strings(a.Configuration.AllowedUpdates)
	sort.Strings(b.Configuration.AllowedUpdates)
	return reflect.DeepEqual(a, b)
}
func botIdentityMatches(v client.Bot, prior botModel) bool {
	if !prior.Messenger.IsNull() && !prior.Messenger.IsUnknown() && prior.Messenger.ValueString() != v.Messenger {
		return false
	}
	if !prior.ScopeID.IsNull() && !prior.ScopeID.IsUnknown() && prior.ScopeID.ValueString() != "" && prior.ScopeID.ValueString() != v.AddressingScope.ID {
		return false
	}
	return true
}
