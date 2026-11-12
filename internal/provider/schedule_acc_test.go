package provider_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/onpager/terraform-provider-onpager/internal/client"
	onpager "github.com/onpager/terraform-provider-onpager/internal/provider"
)

// Raw HTTP models separately owned UI objects and inspects retained history.
// Managed rotation resources use the typed declaration client instead.
func scheduleAccRequest(ctx context.Context, endpoint, token, method, route string, body any, revision int64, output any) error {
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+route, input)
	if err != nil {
		return fmt.Errorf("construct schedule fixture request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if revision > 0 {
		req.Header.Set("If-Match", fmt.Sprintf(`"%d"`, revision))
	}
	transport := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := transport.Do(req)
	if err != nil {
		return fmt.Errorf("schedule fixture request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("schedule fixture %s returned HTTP %d", method, response.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
	}
	return nil
}

func TestAccScheduleSettings(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("live API acceptance is opt-in")
	}
	endpoint, token, user := os.Getenv("ONPAGER_ACC_ENDPOINT"), os.Getenv("ONPAGER_ACC_TOKEN"), os.Getenv("ONPAGER_ACC_USER_ID")
	if endpoint == "" || token == "" || user == "" {
		t.Fatal("ONPAGER_ACC_ENDPOINT, ONPAGER_ACC_TOKEN and ONPAGER_ACC_USER_ID are required")
	}
	t.Setenv("ONPAGER_ENDPOINT", endpoint)
	t.Setenv("ONPAGER_TOKEN", token)
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "onpager")
	c, err := client.New(endpoint, token)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("tfacc-schedule-%x", suffix)
	var team, id, sibling, overrideID string
	var memberAdded bool
	overrideRoute := func() string { return "/v1/schedules/" + id + "/override-rotations/" + overrideID }
	overrideRead := func() error {
		var v struct {
			ID            string  `json:"id"`
			ScheduleID    string  `json:"schedule_id"`
			DeletedAt     *string `json:"deleted_at"`
			Revision      int64   `json:"revision"`
			Configuration *struct {
				Configuration struct {
					UserID   string `json:"user_id"`
					StartsAt string `json:"starts_at"`
					EndsAt   string `json:"ends_at"`
				} `json:"configuration"`
			} `json:"configuration"`
		}
		if err := scheduleAccRequest(context.Background(), endpoint, token, "GET", overrideRoute(), nil, 0, &v); err != nil {
			return err
		}
		if v.ID != overrideID || v.ScheduleID != id || v.DeletedAt != nil || v.Revision != 1 || v.Configuration == nil || v.Configuration.Configuration.UserID != user || v.Configuration.Configuration.StartsAt != "2026-01-05T09:00:07Z" || v.Configuration.Configuration.EndsAt != "2026-01-05T10:00:07Z" {
			return fmt.Errorf("unmanaged override/history changed")
		}
		return nil
	}
	t.Cleanup(func() {
		if overrideID != "" {
			var v struct {
				Revision int64 `json:"revision"`
			}
			if err := scheduleAccRequest(context.Background(), endpoint, token, "GET", overrideRoute(), nil, 0, &v); err != nil {
				t.Error(err)
				return
			}
			if err := scheduleAccRequest(context.Background(), endpoint, token, "DELETE", overrideRoute()+"?mode=all", nil, v.Revision, nil); err != nil {
				t.Error(err)
				return
			}
		}
		if memberAdded {
			if err := c.RemoveMembership(context.Background(), team, user); err != nil {
				t.Error(err)
			}
		}
	})
	config := func(name string, enabled, active bool) string {
		return fmt.Sprintf(`
terraform {
  required_providers {
    onpager = { source = "onpager/onpager" }
  }
}
provider "onpager" {}
resource "onpager_team" "main" {
  name   = %q
  active = %t
}
resource "onpager_schedule" "main" {
  team_id = onpager_team.main.id
  name    = %q
  enabled = %t
}
resource "onpager_schedule" "sibling" {
  team_id = onpager_team.main.id
  name    = %q
  enabled = false
}
`, prefix, active, name, enabled, prefix+"-sibling")
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"onpager": providerserver.NewProtocol6WithError(onpager.New("test")())},
		CheckDestroy: func(*terraform.State) error {
			for _, sid := range []string{id, sibling} {
				v, err := c.GetSchedule(context.Background(), sid)
				if err != nil {
					return err
				}
				if v.Enabled {
					return fmt.Errorf("schedule destroy did not disable retained history")
				}
			}
			return overrideRead()
		},
		Steps: []resource.TestStep{
			{Config: config(prefix, true, true), Check: func(s *terraform.State) error {
				team = s.RootModule().Resources["onpager_team.main"].Primary.ID
				id = s.RootModule().Resources["onpager_schedule.main"].Primary.ID
				sibling = s.RootModule().Resources["onpager_schedule.sibling"].Primary.ID
				return nil
			}},
			{ResourceName: "onpager_schedule.main", ImportState: true, ImportStateVerify: true},
			{PreConfig: func() {
				if _, err := c.AddMembership(t.Context(), team, user, false); err != nil {
					t.Fatal(err)
				}
				memberAdded = true
				before, err := c.GetSchedule(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				var receipt struct {
					ID string `json:"rotation_id"`
				}
				if err := scheduleAccRequest(t.Context(), endpoint, token, "POST", "/v1/schedules/"+id+"/override-rotations", map[string]any{"user_id": user, "starts_at": "2026-01-05T09:00:07Z", "ends_at": "2026-01-05T10:00:07Z"}, 0, &receipt); err != nil {
					t.Fatal(err)
				}
				overrideID = receipt.ID
				after, err := c.GetSchedule(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				if after.Revision != before.Revision {
					t.Fatal("child creation invalidated schedule settings revision")
				}
			}, Config: config(prefix+"-renamed", true, true), Check: func(*terraform.State) error { return overrideRead() }},
			{Config: config(prefix+"-renamed", true, true), PlanOnly: true},
			{PreConfig: func() {
				v, err := c.GetSchedule(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				name := prefix + "-drift"
				if _, err := c.UpdateSchedule(t.Context(), id, v.Revision, client.ScheduleSettings{Name: &name}); err != nil {
					t.Fatal(err)
				}
			}, Config: config(prefix+"-renamed", true, true), Check: resource.TestCheckResourceAttr("onpager_schedule.main", "name", prefix+"-renamed")},
			{Config: config(prefix+"-renamed", false, true), Check: resource.TestCheckResourceAttr("onpager_schedule.main", "enabled", "false")},
			{ResourceName: "onpager_schedule.main", ImportState: true, ImportStateVerify: true},
			{Config: config(prefix+"-renamed", true, true), Check: resource.TestCheckResourceAttr("onpager_schedule.main", "enabled", "true")},
			{Config: config(prefix+"-renamed", false, false), Check: func(*terraform.State) error { return overrideRead() }},
			{Config: config(prefix+"-renamed", false, false), PlanOnly: true},
		},
	})
}
