package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type enumRule []string

func (v enumRule) Description(context.Context) string             { return "One of: " + strings.Join(v, ", ") }
func (v enumRule) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v enumRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, value := range v {
		if req.ConfigValue.ValueString() == value {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid attribute value", v.Description(ctx))
}
func splitImportID(id string) (string, string, bool) {
	p := strings.Split(id, "/")
	if len(p) != 2 || !validID(p[0]) || !validID(p[1]) {
		return "", "", false
	}
	return p[0], p[1], true
}
func (*escalationChainResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m chainModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.CalendarReference.ValueString() == "step_time" && m.CalendarID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("work_calendar_id"), "Missing work calendar", "step_time requires work_calendar_id.")
	}
	if m.Steps.IsNull() || m.Steps.IsUnknown() {
		return
	}
	for _, v := range m.Steps.Elements() {
		if v.IsUnknown() {
			return
		}
	}
	var steps []chainStepModel
	resp.Diagnostics.Append(m.Steps.ElementsAs(ctx, &steps, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for i, s := range steps {
		if s.Kind.IsUnknown() || s.Kind.IsNull() {
			continue
		}
		kind := s.Kind.ValueString()
		base := path.Root("steps").AtListIndex(i)
		required := ""
		switch kind {
		case "wait":
			required = "wait_seconds"
		case "notify_user":
			required = "target_user_id"
		case "notify_oncall":
			required = "schedule_id"
		case "notify_webhook":
			required = "webhook_id"
		case "notify_channel":
			required = "channel_sources"
		case "repeat":
		default:
			resp.Diagnostics.AddAttributeError(base.AtName("kind"), "Invalid step kind", "Use a supported escalation step kind.")
			continue
		}
		fields := map[string]struct{ null, unknown bool }{"wait_seconds": {s.WaitSeconds.IsNull(), s.WaitSeconds.IsUnknown()}, "target_user_id": {s.TargetUserID.IsNull(), s.TargetUserID.IsUnknown()}, "schedule_id": {s.ScheduleID.IsNull(), s.ScheduleID.IsUnknown()}, "webhook_id": {s.WebhookID.IsNull(), s.WebhookID.IsUnknown()}, "channel_sources": {s.ChannelSources.IsNull(), s.ChannelSources.IsUnknown()}, "importance": {s.Importance.IsNull(), s.Importance.IsUnknown()}}
		for name, value := range fields {
			if value.unknown {
				continue
			}
			allowed := name == required || (name == "importance" && (kind == "notify_user" || kind == "notify_oncall"))
			if name == required && value.null {
				resp.Diagnostics.AddAttributeError(base.AtName(name), "Missing step setting", "This setting is required for "+kind+".")
			}
			if !allowed && !value.null {
				resp.Diagnostics.AddAttributeError(base.AtName(name), "Inapplicable step setting", "This setting is not supported for "+kind+".")
			}
		}
		if kind == "wait" && !s.WaitSeconds.IsNull() && !s.WaitSeconds.IsUnknown() && (s.WaitSeconds.ValueInt64() < 1 || s.WaitSeconds.ValueInt64() > 9223372036) {
			resp.Diagnostics.AddAttributeError(base.AtName("wait_seconds"), "Invalid wait duration", "Use 1 through 9223372036 seconds.")
		}
		if kind == "notify_channel" && !s.ChannelSources.IsNull() && !s.ChannelSources.IsUnknown() {
			seen := map[string]bool{}
			if len(s.ChannelSources.Elements()) == 0 {
				resp.Diagnostics.AddAttributeError(base.AtName("channel_sources"), "Empty channel sources", "Supply at least one team/service source.")
			}
			for _, v := range s.ChannelSources.Elements() {
				if v.IsUnknown() {
					continue
				}
				str, ok := v.(types.String)
				if !ok || str.IsNull() || (str.ValueString() != "team" && str.ValueString() != "service") || seen[str.ValueString()] {
					resp.Diagnostics.AddAttributeError(base.AtName("channel_sources"), "Invalid channel sources", "Use unique team/service sources in priority order.")
					break
				}
				seen[str.ValueString()] = true
			}
		}
	}
}
