package provider

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

var outgoingPairTypes = map[string]attr.Type{"key": types.StringType, "value": types.StringType}
var outgoingPairType = types.ObjectType{AttrTypes: outgoingPairTypes}
var outgoingAuthTypes = map[string]attr.Type{"kind": types.StringType, "username": types.StringType, "password_secret": types.StringType, "token_secret": types.StringType}
var outgoingRequestTypes = map[string]attr.Type{"method": types.StringType, "endpoint": types.StringType, "body_format": types.StringType, "body_template": types.StringType, "content_type": types.StringType, "verify_ssl": types.BoolType, "timeout_seconds": types.Int64Type, "query": types.ListType{ElemType: outgoingPairType}, "headers": types.ListType{ElemType: outgoingPairType}, "form": types.ListType{ElemType: outgoingPairType}, "auth": types.ObjectType{AttrTypes: outgoingAuthTypes}}
var outgoingRecipientTypes = map[string]attr.Type{"key": types.StringType, "label": types.StringType, "kind": types.StringType, "required": types.BoolType, "placeholder": types.StringType}
var outgoingRecipientType = types.ObjectType{AttrTypes: outgoingRecipientTypes}
var outgoingSecretTypes = map[string]attr.Type{"action": types.StringType, "value": types.StringType}
var outgoingSecretType = types.ObjectType{AttrTypes: outgoingSecretTypes}
var outgoingKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

type outgoingWebhookModel struct {
	id, team, purpose, name, etag, runtime, requestID types.String
	enabled                                           types.Bool
	request                                           types.Object
	recipients, keys                                  types.List
	secrets                                           types.Map
}

func (r *outgoingWebhookResource) readModel(ctx context.Context, from routingAttributeReader) (outgoingWebhookModel, diag.Diagnostics) {
	var m outgoingWebhookModel
	var d diag.Diagnostics
	for _, v := range []struct {
		name   string
		target any
	}{{"id", &m.id}, {"purpose", &m.purpose}, {"display_name", &m.name}, {"enabled", &m.enabled}, {"etag", &m.etag}, {"runtime_status", &m.runtime}, {"request_id", &m.requestID}, {"request", &m.request}, {"recipient_fields", &m.recipients}, {"secrets", &m.secrets}, {"secret_keys", &m.keys}} {
		d.Append(from.GetAttribute(ctx, path.Root(v.name), v.target)...)
	}
	if r.team {
		d.Append(from.GetAttribute(ctx, path.Root("team_id"), &m.team)...)
	}
	return m, d
}
func (r *outgoingWebhookResource) setModel(ctx context.Context, state *tfsdk.State, m outgoingWebhookModel) diag.Diagnostics {
	values := map[string]attr.Value{"id": m.id, "purpose": m.purpose, "display_name": m.name, "enabled": m.enabled, "etag": m.etag, "runtime_status": m.runtime, "request_id": m.requestID, "request": m.request, "recipient_fields": m.recipients, "secrets": m.secrets, "secret_keys": m.keys}
	if r.team {
		values["team_id"] = m.team
	}
	ts := make(map[string]attr.Type, len(values))
	for k, v := range values {
		ts[k] = v.Type(ctx)
	}
	obj, d := types.ObjectValue(ts, values)
	if !d.HasError() {
		d.Append(state.Set(ctx, obj)...)
	}
	return d
}
func outgoingHasUnknown(v attr.Value) bool {
	if v.IsUnknown() {
		return true
	}
	switch v := v.(type) {
	case types.Object:
		for _, x := range v.Attributes() {
			if outgoingHasUnknown(x) {
				return true
			}
		}
	case types.List:
		for _, x := range v.Elements() {
			if outgoingHasUnknown(x) {
				return true
			}
		}
	case types.Map:
		for _, x := range v.Elements() {
			if outgoingHasUnknown(x) {
				return true
			}
		}
	}
	return false
}
func outgoingString(v map[string]attr.Value, k string) string {
	x, _ := v[k].(types.String)
	return x.ValueString()
}
func outgoingPairs(v attr.Value) []client.OutgoingWebhookPair {
	list, _ := v.(types.List)
	out := make([]client.OutgoingWebhookPair, 0, len(list.Elements()))
	for _, x := range list.Elements() {
		obj, _ := x.(types.Object)
		a := obj.Attributes()
		out = append(out, client.OutgoingWebhookPair{Key: outgoingString(a, "key"), Value: outgoingString(a, "value")})
	}
	return out
}
func outgoingInput(m outgoingWebhookModel) (client.OutgoingWebhookInput, diag.Diagnostics) {
	var d diag.Diagnostics
	for _, v := range []attr.Value{m.purpose, m.name, m.enabled, m.request, m.recipients, m.secrets} {
		if outgoingHasUnknown(v) {
			d.AddError("Unknown outgoing-webhook configuration", "All request, recipient and secret fields must be known before applying.")
			return client.OutgoingWebhookInput{}, d
		}
	}
	if m.purpose.IsNull() || m.name.IsNull() || m.enabled.IsNull() || m.request.IsNull() {
		d.AddError("Missing outgoing-webhook configuration", "Purpose, display_name, enabled and request must be known and non-null.")
		return client.OutgoingWebhookInput{}, d
	}
	a := m.request.Attributes()
	b, _ := a["verify_ssl"].(types.Bool)
	timeout, _ := a["timeout_seconds"].(types.Int64)
	auth, _ := a["auth"].(types.Object)
	aa := auth.Attributes()
	verify := b.ValueBool()
	out := client.OutgoingWebhookInput{Purpose: m.purpose.ValueString(), DisplayName: m.name.ValueString(), Enabled: m.enabled.ValueBool(), Request: client.OutgoingWebhookRequest{Method: outgoingString(a, "method"), Endpoint: outgoingString(a, "endpoint"), BodyFormat: outgoingString(a, "body_format"), BodyTemplate: outgoingString(a, "body_template"), ContentType: outgoingString(a, "content_type"), VerifySSL: &verify, TimeoutSeconds: timeout.ValueInt64(), Query: outgoingPairs(a["query"]), Headers: outgoingPairs(a["headers"]), Form: outgoingPairs(a["form"]), Auth: client.OutgoingWebhookAuth{Kind: outgoingString(aa, "kind"), Username: outgoingString(aa, "username"), PasswordSecret: outgoingString(aa, "password_secret"), TokenSecret: outgoingString(aa, "token_secret")}}, RecipientFields: []client.OutgoingWebhookRecipientField{}, Secrets: map[string]client.OutgoingWebhookSecretChange{}}
	for _, x := range m.recipients.Elements() {
		obj, _ := x.(types.Object)
		v := obj.Attributes()
		required, _ := v["required"].(types.Bool)
		out.RecipientFields = append(out.RecipientFields, client.OutgoingWebhookRecipientField{Key: outgoingString(v, "key"), Label: outgoingString(v, "label"), Kind: outgoingString(v, "kind"), Required: required.ValueBool(), Placeholder: outgoingString(v, "placeholder")})
	}
	for k, x := range m.secrets.Elements() {
		obj, _ := x.(types.Object)
		v := obj.Attributes()
		action := outgoingString(v, "action")
		value, _ := v["value"].(types.String)
		if action == "replace" && value.IsNull() {
			d.AddError("Missing outgoing-webhook secret value", "A replace action requires a known non-null value; an explicit empty string is supported.")
		}
		out.Secrets[k] = client.OutgoingWebhookSecretChange{Action: action, Value: value.ValueString()}
	}
	invalid := func() {
		d.AddError("Invalid outgoing-webhook configuration", "Use the documented purpose, request settings, recipient fields and secret actions. Sensitive values are never included in diagnostics.")
	}
	c := out.Request
	if !notificationContains([]string{"escalation", "notification"}, out.Purpose) || out.DisplayName == "" || strings.TrimSpace(out.DisplayName) != out.DisplayName || !utf8.ValidString(out.DisplayName) || utf8.RuneCountInString(out.DisplayName) > 200 {
		invalid()
	}
	if b.IsNull() || timeout.IsNull() || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60 || !notificationContains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}, c.Method) || !notificationContains([]string{"json", "form", "text"}, c.BodyFormat) || len(c.Endpoint) > 8192 {
		invalid()
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(c.Endpoint, "#") || strings.ContainsAny(u.Host, "{}\\") {
		invalid()
	}
	if (c.Method == "GET" || c.Method == "HEAD") && (c.BodyTemplate != "" || len(c.Form) > 0) || c.BodyFormat != "form" && len(c.Form) > 0 || c.BodyFormat == "form" && c.BodyTemplate != "" || c.BodyFormat != "text" && c.ContentType != "" {
		invalid()
	}
	if len(c.BodyTemplate) > 65536 || len(c.Query) > 50 || len(c.Headers) > 50 || len(c.Form) > 50 || len(out.RecipientFields) > 20 || out.Purpose == "escalation" && len(out.RecipientFields) > 0 || len(out.Secrets) > 50 {
		invalid()
	}
	for _, pairs := range [][]client.OutgoingWebhookPair{c.Query, c.Headers, c.Form} {
		for _, p := range pairs {
			if p.Key == "" || len(p.Key) > 1024 || len(p.Value) > 65536 || strings.Contains(p.Key, "{{") {
				invalid()
			}
		}
	}
	switch c.Auth.Kind {
	case "none":
		if c.Auth.Username != "" || c.Auth.PasswordSecret != "" || c.Auth.TokenSecret != "" {
			invalid()
		}
	case "basic":
		if c.Auth.Username == "" || strings.Contains(c.Auth.Username, ":") || c.Auth.TokenSecret != "" || c.Auth.PasswordSecret != "" && !outgoingKeyPattern.MatchString(c.Auth.PasswordSecret) {
			invalid()
		}
	case "bearer":
		if !outgoingKeyPattern.MatchString(c.Auth.TokenSecret) || c.Auth.Username != "" || c.Auth.PasswordSecret != "" {
			invalid()
		}
	default:
		invalid()
	}
	seen := map[string]bool{}
	for _, f := range out.RecipientFields {
		if !outgoingKeyPattern.MatchString(f.Key) || seen[f.Key] || strings.TrimSpace(f.Label) == "" || utf8.RuneCountInString(f.Label) > 200 || len(f.Placeholder) > 1024 || !notificationContains([]string{"string", "email", "phone"}, f.Kind) {
			invalid()
		}
		seen[f.Key] = true
	}
	total := 0
	for k, v := range out.Secrets {
		total += len(v.Value)
		if !outgoingKeyPattern.MatchString(k) || !notificationContains([]string{"keep", "replace", "delete"}, v.Action) || len(v.Value) > 65536 || !utf8.ValidString(v.Value) || (v.Action == "keep" || v.Action == "delete") && v.Value != "" {
			invalid()
		}
	}
	if total > 65536 {
		invalid()
	}
	return out, d
}

func outgoingPairsState(pairs []client.OutgoingWebhookPair, old types.List) types.List {
	if len(pairs) == 0 && old.IsNull() {
		return types.ListNull(outgoingPairType)
	}
	values := make([]attr.Value, 0, len(pairs))
	for _, p := range pairs {
		values = append(values, types.ObjectValueMust(outgoingPairTypes, map[string]attr.Value{"key": types.StringValue(p.Key), "value": types.StringValue(p.Value)}))
	}
	return types.ListValueMust(outgoingPairType, values)
}
func outgoingState(ctx context.Context, v client.OutgoingWebhook, m outgoingWebhookModel) (outgoingWebhookModel, diag.Diagnostics) {
	var d diag.Diagnostics
	m.id = types.StringValue(v.ID)
	m.purpose = types.StringValue(v.Purpose)
	m.name = types.StringValue(v.DisplayName)
	m.enabled = types.BoolValue(v.Enabled)
	m.etag = types.StringValue(v.ETag)
	m.runtime = types.StringValue(v.RuntimeStatus)
	if m.requestID.IsUnknown() {
		m.requestID = types.StringNull()
	}
	prior, ds := outgoingInput(m)
	if !ds.HasError() && client.EqualOutgoingWebhookRequests(prior.Request, v.Request) { /* Preserve equivalent declared query/default/null representation. */
	} else {
		old := m.request.Attributes()
		list := func(k string) types.List { v, _ := old[k].(types.List); return v }
		c := v.Request
		verify := true
		if c.VerifySSL != nil {
			verify = *c.VerifySSL
		}
		auth := c.Auth
		if auth.Kind == "" {
			auth.Kind = "none"
		}
		m.request = types.ObjectValueMust(outgoingRequestTypes, map[string]attr.Value{"method": types.StringValue(c.Method), "endpoint": types.StringValue(c.Endpoint), "body_format": types.StringValue(c.BodyFormat), "body_template": types.StringValue(c.BodyTemplate), "content_type": types.StringValue(c.ContentType), "verify_ssl": types.BoolValue(verify), "timeout_seconds": types.Int64Value(c.TimeoutSeconds), "query": outgoingPairsState(c.Query, list("query")), "headers": outgoingPairsState(c.Headers, list("headers")), "form": outgoingPairsState(c.Form, list("form")), "auth": types.ObjectValueMust(outgoingAuthTypes, map[string]attr.Value{"kind": types.StringValue(auth.Kind), "username": types.StringValue(auth.Username), "password_secret": types.StringValue(auth.PasswordSecret), "token_secret": types.StringValue(auth.TokenSecret)})})
	}
	if len(v.RecipientFields) > 0 || !m.recipients.IsNull() && !m.recipients.IsUnknown() {
		values := make([]attr.Value, 0, len(v.RecipientFields))
		for _, f := range v.RecipientFields {
			values = append(values, types.ObjectValueMust(outgoingRecipientTypes, map[string]attr.Value{"key": types.StringValue(f.Key), "label": types.StringValue(f.Label), "kind": types.StringValue(f.Kind), "required": types.BoolValue(f.Required), "placeholder": types.StringValue(f.Placeholder)}))
		}
		m.recipients = types.ListValueMust(outgoingRecipientType, values)
	} else if m.recipients.IsUnknown() {
		m.recipients = types.ListNull(outgoingRecipientType)
	}
	keys := make([]attr.Value, 0, len(v.Secrets))
	exists := map[string]bool{}
	for _, s := range v.Secrets {
		if s.HasValue {
			keys = append(keys, types.StringValue(s.Key))
			exists[s.Key] = true
		}
	}
	m.keys = types.ListValueMust(types.StringType, keys)
	if m.secrets.IsUnknown() {
		m.secrets = types.MapNull(outgoingSecretType)
	} else if !m.secrets.IsNull() {
		values := m.secrets.Elements()
		for k, x := range values {
			obj, _ := x.(types.Object)
			a := obj.Attributes()
			if !exists[k] && outgoingString(a, "action") != "delete" {
				a["value"] = types.StringNull()
				values[k] = types.ObjectValueMust(outgoingSecretTypes, a)
			}
		}
		m.secrets = types.MapValueMust(outgoingSecretType, values)
	}
	return m, d
}
