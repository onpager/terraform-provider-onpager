package provider

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

var voiceConfigurationTypes = map[string]attr.Type{"call_campaign_id": types.StringType, "verification_campaign_id": types.StringType, "max_text_runes": types.Int64Type}
var voiceSecretTypes = map[string]attr.Type{"action": types.StringType, "value": types.StringType}
var voiceSecretType = types.ObjectType{AttrTypes: voiceSecretTypes}

type voiceProviderModel struct {
	ID                types.String `tfsdk:"id"`
	Kind              types.String `tfsdk:"kind"`
	DisplayName       types.String `tfsdk:"display_name"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Visibility        types.String `tfsdk:"visibility"`
	TeamIDs           types.Set    `tfsdk:"team_ids"`
	Configuration     types.Object `tfsdk:"configuration"`
	Secrets           types.Map    `tfsdk:"secrets"`
	SecretKeys        types.Set    `tfsdk:"secret_keys"`
	CallbackURL       types.String `tfsdk:"callback_url"`
	RuntimeStatus     types.String `tfsdk:"runtime_status"`
	RuntimeErrorCode  types.String `tfsdk:"runtime_error_code"`
	RuntimeObservedAt types.String `tfsdk:"runtime_observed_at"`
}

func voiceUnknown(v attr.Value) bool {
	if v.IsUnknown() {
		return true
	}
	switch v := v.(type) {
	case types.Object:
		for _, x := range v.Attributes() {
			if voiceUnknown(x) {
				return true
			}
		}
	case types.Map:
		for _, x := range v.Elements() {
			if voiceUnknown(x) {
				return true
			}
		}
	case types.Set:
		for _, x := range v.Elements() {
			if voiceUnknown(x) {
				return true
			}
		}
	}
	return false
}
func voiceText(s string, max int) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func voiceSecret(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func voiceDefaults(m voiceProviderModel) voiceProviderModel {
	if m.Enabled.IsNull() {
		m.Enabled = types.BoolValue(true)
	}
	if m.Visibility.IsNull() {
		m.Visibility = types.StringValue("global")
	}
	if !m.Configuration.IsNull() && !m.Configuration.IsUnknown() {
		a := m.Configuration.Attributes()
		if a["max_text_runes"].IsNull() {
			a["max_text_runes"] = types.Int64Value(500)
		}
		m.Configuration = types.ObjectValueMust(voiceConfigurationTypes, a)
	}
	return m
}

// voiceInput validates known values during configuration and requires all
// desired values to be known during apply. Diagnostics never include inputs.
func voiceInput(m voiceProviderModel, apply bool) (client.VoiceProviderInput, diag.Diagnostics) {
	var d diag.Diagnostics
	m = voiceDefaults(m)
	invalid := func() {
		d.AddError("Invalid voice-provider configuration", "Use kind zvonok, a nonblank display name of at most 200 UTF-8 bytes, valid campaign identifiers and max_text_runes from 1 through 500. Visibility must be global or team; global visibility requires no team IDs. Only public_key keep or nonempty replace changes are supported. Sensitive input values are omitted from diagnostics.")
	}
	unknown := false
	for _, v := range []attr.Value{m.Kind, m.DisplayName, m.Enabled, m.Visibility, m.TeamIDs, m.Configuration, m.Secrets} {
		unknown = unknown || voiceUnknown(v)
	}
	if apply && unknown {
		d.AddError("Unknown voice-provider configuration", "All public settings, team IDs and secret changes must be known before applying.")
		return client.VoiceProviderInput{}, d
	}
	if !m.Kind.IsUnknown() && (m.Kind.IsNull() || m.Kind.ValueString() != "zvonok") {
		invalid()
	}
	if !m.DisplayName.IsUnknown() && (m.DisplayName.IsNull() || !voiceText(m.DisplayName.ValueString(), 200)) {
		invalid()
	}
	if !m.Visibility.IsUnknown() && m.Visibility.ValueString() != "global" && m.Visibility.ValueString() != "team" {
		invalid()
	}
	in := client.VoiceProviderInput{DisplayName: m.DisplayName.ValueString(), Enabled: m.Enabled.ValueBool(), Visibility: m.Visibility.ValueString(), TeamIDs: []string{}}
	if m.Configuration.IsNull() {
		invalid()
	} else if !m.Configuration.IsUnknown() {
		a := m.Configuration.Attributes()
		call, _ := a["call_campaign_id"].(types.String)
		verify, _ := a["verification_campaign_id"].(types.String)
		limit, _ := a["max_text_runes"].(types.Int64)
		if !call.IsUnknown() && (call.IsNull() || !voiceText(call.ValueString(), 128)) {
			invalid()
		}
		if !verify.IsUnknown() && (verify.IsNull() || !voiceText(verify.ValueString(), 128)) {
			invalid()
		}
		if !limit.IsUnknown() && (limit.IsNull() || limit.ValueInt64() < 1 || limit.ValueInt64() > 500) {
			invalid()
		}
		in.Configuration = client.VoiceProviderConfiguration{CallCampaignID: call.ValueString(), VerificationCampaignID: verify.ValueString(), MaxTextRunes: limit.ValueInt64()}
	}
	if !m.TeamIDs.IsUnknown() {
		if len(m.TeamIDs.Elements()) > 1000 {
			invalid()
		}
		for _, v := range m.TeamIDs.Elements() {
			id, _ := v.(types.String)
			if !id.IsUnknown() {
				if id.IsNull() || !validID(id.ValueString()) {
					invalid()
				} else {
					in.TeamIDs = append(in.TeamIDs, id.ValueString())
				}
			}
		}
		if !m.Visibility.IsUnknown() && m.Visibility.ValueString() == "global" && len(m.TeamIDs.Elements()) > 0 {
			invalid()
		}
	}
	if !m.Secrets.IsNull() && !m.Secrets.IsUnknown() {
		in.Secrets = map[string]client.VoiceProviderSecretChange{}
		for k, v := range m.Secrets.Elements() {
			if k != "public_key" {
				invalid()
			}
			obj, _ := v.(types.Object)
			if obj.IsNull() {
				invalid()
				continue
			}
			if obj.IsUnknown() {
				continue
			}
			a := obj.Attributes()
			action, _ := a["action"].(types.String)
			value, _ := a["value"].(types.String)
			if !action.IsUnknown() {
				switch action.ValueString() {
				case "keep":
					if !value.IsUnknown() && !value.IsNull() && value.ValueString() != "" {
						invalid()
					}
				case "replace":
					if !value.IsUnknown() && (value.IsNull() || !voiceSecret(value.ValueString())) {
						invalid()
					}
				default:
					invalid()
				}
			}
			in.Secrets[k] = client.VoiceProviderSecretChange{Action: action.ValueString(), Value: value.ValueString()}
		}
	}
	return in, d
}

func voiceState(v client.VoiceProvider, m voiceProviderModel) voiceProviderModel {
	m.ID = types.StringValue(v.ID)
	m.Kind = types.StringValue(v.Kind)
	m.DisplayName = types.StringValue(v.DisplayName)
	m.Enabled = types.BoolValue(v.Enabled)
	m.Visibility = types.StringValue(v.Visibility)
	m.Configuration = types.ObjectValueMust(voiceConfigurationTypes, map[string]attr.Value{"call_campaign_id": types.StringValue(v.Configuration.CallCampaignID), "verification_campaign_id": types.StringValue(v.Configuration.VerificationCampaignID), "max_text_runes": types.Int64Value(v.Configuration.MaxTextRunes)})
	if len(v.TeamIDs) > 0 || !m.TeamIDs.IsNull() && !m.TeamIDs.IsUnknown() {
		teams := make([]attr.Value, 0, len(v.TeamIDs))
		for _, id := range v.TeamIDs {
			teams = append(teams, types.StringValue(id))
		}
		m.TeamIDs = types.SetValueMust(types.StringType, teams)
	} else {
		m.TeamIDs = types.SetNull(types.StringType)
	}
	keys := make([]attr.Value, 0, len(v.SecretKeys))
	present := map[string]bool{}
	for _, k := range v.SecretKeys {
		keys = append(keys, types.StringValue(k))
		present[k] = true
	}
	m.SecretKeys = types.SetValueMust(types.StringType, keys)
	if m.Secrets.IsUnknown() {
		m.Secrets = types.MapNull(voiceSecretType)
	} else if !m.Secrets.IsNull() {
		next := m.Secrets.Elements()
		for k, v := range next {
			if !present[k] {
				obj, _ := v.(types.Object)
				if !obj.IsNull() && !obj.IsUnknown() {
					a := obj.Attributes()
					a["value"] = types.StringNull()
					next[k] = types.ObjectValueMust(voiceSecretTypes, a)
				}
			}
		}
		m.Secrets = types.MapValueMust(voiceSecretType, next)
	}
	m.CallbackURL = voiceOptional(v.CallbackURL)
	m.RuntimeStatus = types.StringValue(v.Runtime.State)
	m.RuntimeErrorCode = voiceOptional(v.Runtime.ErrorCode)
	m.RuntimeObservedAt = voiceOptional(v.Runtime.ObservedAt)
	return m
}
func voiceOptional(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// Once a write commits, preserve the known desired values before any follow-up
// read. A read error must not restore old settings or expose unknown state.
func voiceCommitted(m voiceProviderModel, id string, old *voiceProviderModel) voiceProviderModel {
	m = voiceDefaults(m)
	m.ID = types.StringValue(id)
	m.RuntimeStatus = types.StringNull()
	m.RuntimeErrorCode = types.StringNull()
	m.RuntimeObservedAt = types.StringNull()
	m.CallbackURL = types.StringNull()
	if old != nil && !old.CallbackURL.IsUnknown() {
		m.CallbackURL = old.CallbackURL
	}
	keys := []attr.Value{types.StringValue("public_key")}
	m.SecretKeys = types.SetValueMust(types.StringType, keys)
	if m.TeamIDs.IsUnknown() {
		m.TeamIDs = types.SetNull(types.StringType)
	}
	if m.Secrets.IsUnknown() {
		m.Secrets = types.MapNull(voiceSecretType)
	}
	return m
}

// Metadata/settings edits must not replay a previously declared replacement:
// the separate credential grant is needed only for an actual requested change.
func voiceChangedSecrets(in client.VoiceProviderInput, plan, state voiceProviderModel) client.VoiceProviderInput {
	for k, v := range in.Secrets {
		if v.Action != "replace" {
			continue
		}
		present := false
		for _, x := range state.SecretKeys.Elements() {
			s, _ := x.(types.String)
			present = present || s.ValueString() == k
		}
		prior, ok := state.Secrets.Elements()[k]
		desired := plan.Secrets.Elements()[k]
		if present && ok && desired.Equal(prior) {
			delete(in.Secrets, k)
		}
	}
	return in
}

// Unversioned writes have no receipt revision to bind the follow-up read. Only
// adopt its observed fields when all settings still match the committed intent.
func voiceIntentMatches(v client.VoiceProvider, committed voiceProviderModel, in client.VoiceProviderInput) bool {
	if v.ID != committed.ID.ValueString() || v.Kind != committed.Kind.ValueString() || v.DisplayName != in.DisplayName || v.Enabled != in.Enabled || v.Visibility != in.Visibility || v.Configuration != in.Configuration {
		return false
	}
	actualTeams, expectedTeams := slices.Clone(v.TeamIDs), slices.Clone(in.TeamIDs)
	slices.Sort(actualTeams)
	slices.Sort(expectedTeams)
	if !slices.Equal(actualTeams, expectedTeams) {
		return false
	}
	keys := make([]attr.Value, 0, len(v.SecretKeys))
	for _, key := range v.SecretKeys {
		keys = append(keys, types.StringValue(key))
	}
	return committed.SecretKeys.Equal(types.SetValueMust(types.StringType, keys))
}
