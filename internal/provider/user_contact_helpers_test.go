package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

// Deleting a managed contact consumes its request key permanently for this
// employee and method kind. Reject a known conflict before Terraform destroys it.
func TestUserContactReplacementRequestScope(t *testing.T) {
	r := onpager.NewUserWebhookContactResource()
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	old := valueState(t, schema.Schema, userWebhookValues())
	for _, tc := range []struct {
		name       string
		user       string
		slot, key  types.String
		shouldFail bool
	}{
		{"same key on another webhook", fOtherTeam, types.StringNull(), types.StringValue(scheduleID), true},
		{"same key on another template slot", fOtherTeam, types.StringValue(fTeam), types.StringValue(scheduleID), true},
		{"missing key on replacement", fOtherTeam, types.StringNull(), types.StringNull(), true},
		{"key to be created by terraform_data", fOtherTeam, types.StringNull(), types.StringUnknown(), false},
		{"fresh key", fOtherTeam, types.StringNull(), types.StringValue(fTeam), false},
		{"same key for another user", fTeam, types.StringNull(), types.StringValue(scheduleID), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := userWebhookValues()
			next["request_id"], next["template_contact_id"] = tc.key, tc.slot
			next["user_id"] = types.StringValue(tc.user)
			if tc.slot.IsNull() {
				next["webhook_id"] = types.StringValue(fTeam)
			}
			state := valueState(t, schema.Schema, next)
			req := resource.ModifyPlanRequest{
				State: old, Config: tfsdk.Config{Raw: state.Raw, Schema: schema.Schema},
				Plan: tfsdk.Plan{Raw: state.Raw, Schema: schema.Schema},
			}
			resp := resource.ModifyPlanResponse{Plan: req.Plan}
			r.(resource.ResourceWithModifyPlan).ModifyPlan(t.Context(), req, &resp)
			if resp.Diagnostics.HasError() != tc.shouldFail {
				t.Fatalf("unexpected replacement decision: %v", resp.Diagnostics)
			}
		})
	}
}
