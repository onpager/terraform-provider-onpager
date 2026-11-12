package provider_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

// Global slots are shared tenant settings: fixture HTTP tests cover their CRUD.
// Live tests must never replace existing tenant-wide notification templates.
func TestAccNotificationTemplateGlobal(t *testing.T) {
	t.Skip("global notification templates have no isolated owner; CRUD is covered by local HTTP fixtures")
}

func TestAccTeamNotificationTemplate(t *testing.T) {
	for _, deliveryProvider := range []string{"telegram", "zvonok"} {
		for _, language := range []string{"ru", "en"} {
			t.Run(deliveryProvider+"/"+language, func(t *testing.T) {
				testAccTeamNotificationTemplate(t, deliveryProvider, language)
			})
		}
	}
}

func testAccTeamNotificationTemplate(t *testing.T, deliveryProvider, language string) {
	t.Helper()
	siblingLanguage := "ru"
	if language == "ru" {
		siblingLanguage = "en"
	}
	format := "telegram_html"
	if deliveryProvider == "zvonok" {
		format = "plain"
	}
	c, prefix := routingAccSetup(t)
	var team string
	siblingBody := prefix + " sibling retained"
	config := func(body string, active bool) string {
		return routingAccHeader + fmt.Sprintf(`
resource "onpager_team" "templates" {
 name = %q
 active = %t
}
resource "onpager_team_notification_template" "main" {
 team_id = onpager_team.templates.id
 type = "alert_direct"
 delivery_provider = %q
 language = %q
 format = %q
 body = %q
}
`, prefix+"-templates", active, deliveryProvider, language, format, body)
	}
	readSibling := func() error {
		v, err := c.GetNotificationTemplate(context.Background(), team, "alert_direct", deliveryProvider, siblingLanguage)
		if err != nil {
			return err
		}
		if v.Override == nil || v.Override.Body != siblingBody || v.Override.Format != format {
			return fmt.Errorf("unmanaged other-language template changed")
		}
		return nil
	}
	t.Cleanup(func() {
		if team == "" {
			return
		}
		v, err := c.GetNotificationTemplate(context.Background(), team, "alert_direct", deliveryProvider, siblingLanguage)
		if err != nil {
			t.Error(err)
			return
		}
		if v.Override != nil {
			if _, err = c.DeleteNotificationTemplate(context.Background(), team, "alert_direct", deliveryProvider, siblingLanguage, v.ETag); err != nil {
				t.Error(err)
			}
		}
	})
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), CheckDestroy: func(*terraform.State) error {
		v, err := c.GetNotificationTemplate(context.Background(), team, "alert_direct", deliveryProvider, language)
		if err != nil {
			return err
		}
		if v.Override != nil {
			return fmt.Errorf("managed override survived reset")
		}
		return readSibling()
	}, Steps: []resource.TestStep{
		{Config: config(prefix+" original", true), Check: func(s *terraform.State) error {
			team = s.RootModule().Resources["onpager_team.templates"].Primary.ID
			v, err := c.GetNotificationTemplate(context.Background(), team, "alert_direct", deliveryProvider, siblingLanguage)
			if err != nil {
				return err
			}
			_, err = c.PutNotificationTemplate(context.Background(), team, "alert_direct", deliveryProvider, siblingLanguage, v.ETag, client.NotificationTemplateDraft{Format: format, Body: siblingBody})
			return err
		}},
		{ResourceName: "onpager_team_notification_template.main", ImportState: true, ImportStateVerify: true},
		{Config: config(prefix+" updated\n", true), Check: func(*terraform.State) error { return readSibling() }},
		{Config: config(prefix+" updated\n", true), PlanOnly: true},
		{PreConfig: func() {
			v, err := c.GetNotificationTemplate(t.Context(), team, "alert_direct", deliveryProvider, language)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.PutNotificationTemplate(t.Context(), team, "alert_direct", deliveryProvider, language, v.ETag, client.NotificationTemplateDraft{Format: format, Body: prefix + " external drift"}); err != nil {
				t.Fatal(err)
			}
		}, Config: config(prefix+" updated\n", true), Check: resource.TestCheckResourceAttr("onpager_team_notification_template.main", "body", prefix+" updated\n")},
		{PreConfig: func() {
			v, err := c.GetNotificationTemplate(t.Context(), team, "alert_direct", deliveryProvider, language)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.DeleteNotificationTemplate(t.Context(), team, "alert_direct", deliveryProvider, language, v.ETag); err != nil {
				t.Fatal(err)
			}
		}, Config: config(prefix+" updated\n", true), Check: func(*terraform.State) error { return readSibling() }},
		{Config: config(prefix+" updated\n", false), Check: func(*terraform.State) error { return readSibling() }},
		{ResourceName: "onpager_team_notification_template.main", ImportState: true, ImportStateVerify: true},
		{Config: config(prefix+" updated\n", false), PlanOnly: true},
	}})
}
