package provider_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestAccConfigurationDataSources(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 for explicitly configured local API acceptance")
	}
	c, prefix := routingAccSetup(t)
	team, err := c.CreateTeam(t.Context(), prefix+"-team")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.DisableTeam(ctx, team.ID); err != nil {
			t.Error(err)
		}
	})
	service, err := c.CreateService(t.Context(), team.ID, prefix+"-service")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.DisableService(ctx, service.ID); err != nil {
			t.Error(err)
		}
	})
	role, err := c.CreateRBACRole(t.Context(), client.RBACRoleInput{Key: prefix + "-role", Name: prefix + "-role", Description: "Data source fixture", ScopeType: "tenant", PermissionKeys: []string{"teams.read"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.DeleteRBACRole(ctx, role.ID, role.Revision); err != nil {
			t.Error(err)
		}
	})
	group, err := c.CreateGroup(t.Context(), client.GroupInput{Key: prefix + "-group", Name: prefix + "-group", Description: "Data source fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		current, err := c.GetGroup(ctx, group.ID)
		if err == nil {
			err = c.DeleteGroup(ctx, group.ID, current.Revision)
		}
		if err != nil {
			t.Error(err)
		}
	})
	userID := os.Getenv("ONPAGER_ACC_DS_USER_ID")
	if userID == "" {
		userID = os.Getenv("ONPAGER_ACC_USER_ID")
	}
	if userID == "" {
		t.Fatal("an explicitly configured existing user ID is required")
	}
	login, email := os.Getenv("ONPAGER_ACC_DS_LOCAL_LOGIN"), os.Getenv("ONPAGER_ACC_DS_EMAIL")
	channelID, builtin := os.Getenv("ONPAGER_ACC_DELIVERY_CHANNEL_ID"), os.Getenv("ONPAGER_ACC_RBAC_BUILTIN_KEY")
	config := func(byID bool, extra string) string {
		selector := func(field, value, id string) string {
			if byID {
				return fmt.Sprintf("id = %q", id)
			}
			return fmt.Sprintf("%s = %q", field, value)
		}
		out := fmt.Sprintf(`
terraform {
 required_providers {
  onpager = { source = "onpager/onpager" }
 }
}
provider "onpager" { timezone = "Europe/Moscow" }
data "onpager_user" "selected" { id = %q }
data "onpager_team" "selected" { %s }
data "onpager_service" "selected" {
 %s
 team_id = %q
}
data "onpager_rbac_role" "selected" { %s }
data "onpager_group" "selected" { %s }
data "onpager_group" "by_key" { key = %q }
`, userID, selector("name", team.Name, team.ID), selector("name", service.Name, service.ID), team.ID, selector("key", role.Key, role.ID), selector("name", group.Name, group.ID), group.Key)
		if login != "" {
			out += fmt.Sprintf("data \"onpager_user\" \"local\" { local_login = %q }\n", " "+strings.ToUpper(login)+" ")
		}
		if email != "" {
			out += fmt.Sprintf("data \"onpager_user\" \"email\" { email = %q }\n", " "+strings.ToUpper(email)+" ")
		}
		if channelID != "" {
			out += fmt.Sprintf("data \"onpager_delivery_channel\" \"selected\" { id = %q }\n", channelID)
		}
		if builtin != "" {
			out += fmt.Sprintf("data \"onpager_rbac_role\" \"builtin\" { key = %q }\n", builtin)
		}
		return out + extra
	}
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr("data.onpager_user.selected", "id", userID),
		resource.TestCheckResourceAttr("data.onpager_team.selected", "id", team.ID),
		resource.TestCheckResourceAttr("data.onpager_service.selected", "id", service.ID),
		resource.TestCheckResourceAttr("data.onpager_service.selected", "team_id", team.ID),
		resource.TestCheckResourceAttr("data.onpager_rbac_role.selected", "id", role.ID),
		resource.TestCheckResourceAttr("data.onpager_group.selected", "id", group.ID),
		resource.TestCheckResourceAttr("data.onpager_group.by_key", "id", group.ID),
	}
	if login != "" {
		checks = append(checks, resource.TestCheckResourceAttr("data.onpager_user.local", "id", userID), resource.TestCheckResourceAttr("data.onpager_user.local", "local_login", " "+strings.ToUpper(login)+" "))
	}
	if email != "" {
		checks = append(checks, resource.TestCheckResourceAttr("data.onpager_user.email", "id", userID), resource.TestCheckResourceAttr("data.onpager_user.email", "email", " "+strings.ToUpper(email)+" "), resource.TestCheckResourceAttr("data.onpager_user.email", "email_normalized", email))
	}
	if channelID != "" {
		checks = append(checks, resource.TestCheckResourceAttr("data.onpager_delivery_channel.selected", "id", channelID))
	} else {
		t.Log("channel data-source live read needs explicit ONPAGER_ACC_DELIVERY_CHANNEL_ID; other sources remain covered")
	}
	if builtin != "" {
		checks = append(checks, resource.TestCheckResourceAttr("data.onpager_rbac_role.builtin", "builtin", "true"))
	}
	var duplicate string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if duplicate != "" {
			current, err := c.GetGroup(ctx, duplicate)
			if err == nil {
				err = c.DeleteGroup(ctx, duplicate, current.Revision)
			}
			if err != nil {
				t.Error(err)
			}
		}
	})
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: routingAccFactories(), Steps: []resource.TestStep{
		{Config: config(false, ""), Check: resource.ComposeTestCheckFunc(checks...)},
		{Config: config(false, ""), PlanOnly: true},
		{Config: config(true, ""), Check: resource.ComposeTestCheckFunc(checks...)},
		{Config: config(true, ""), PlanOnly: true},
		{PreConfig: func() {
			if err := c.DisableService(t.Context(), service.ID); err != nil {
				t.Fatal(err)
			}
			if err := c.DisableTeam(t.Context(), team.ID); err != nil {
				t.Fatal(err)
			}
		}, Config: config(false, ""), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("data.onpager_team.selected", "active", "false"), resource.TestCheckResourceAttr("data.onpager_service.selected", "active", "false"))},
		{Config: config(false, ""), PlanOnly: true},
		{Config: config(true, fmt.Sprintf("data \"onpager_team\" \"wrong_case\" { name = %q }\n", strings.ToUpper(team.Name))), ExpectError: regexp.MustCompile("no visible match")},
		{PreConfig: func() {
			v, err := c.CreateGroup(t.Context(), client.GroupInput{Key: prefix + "-duplicate", Name: group.Name, Description: "ambiguity fixture"})
			if err != nil {
				t.Fatal(err)
			}
			duplicate = v.ID
		}, Config: config(true, fmt.Sprintf("data \"onpager_group\" \"ambiguous\" { name = %q }\n", group.Name)), ExpectError: regexp.MustCompile("ambiguous")},
		{Config: config(true, ""), Check: func(state *terraform.State) error {
			return resource.TestCheckResourceAttr("data.onpager_group.by_key", "id", group.ID)(state)
		}},
		{Config: config(true, ""), PlanOnly: true},
	}})
}
