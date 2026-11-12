package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/onpager/terraform-provider-onpager/internal/client"
)

const chainTeam = "11111111-1111-4111-8111-111111111111"
const chainID = "22222222-2222-4222-8222-222222222222"

func testChain(t *testing.T, steps []client.ChainStep) chainModel {
	t.Helper()
	v, d := chainModelFromAPI(t.Context(), client.Chain{ID: chainID, TeamID: chainTeam, ETag: `"` + strings.Repeat("a", 64) + `"`, ChainInput: client.ChainInput{Name: "Primary", Steps: steps, WorkCalendarReference: "start_time"}})
	if d.HasError() {
		t.Fatal(d)
	}
	return v
}

func TestChainStepMappingPreservesOrderAndOmitsInapplicableFields(t *testing.T) {
	user, schedule, webhook := chainTeam, chainID, "33333333-3333-4333-8333-333333333333"
	steps := []client.ChainStep{{Kind: "wait", WaitSeconds: 60}, {Kind: "notify_user", TargetUserID: &user, Importance: "default"}, {Kind: "notify_oncall", ScheduleID: &schedule, Importance: "important"}, {Kind: "notify_channel", ChannelSources: []string{"service", "team"}}, {Kind: "notify_webhook", WebhookID: &webhook}, {Kind: "repeat"}}
	v := testChain(t, steps)
	payload, d := chainPayload(t.Context(), v)
	if d.HasError() {
		t.Fatal(d)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Steps []map[string]any `json:"steps"`
	}
	json.Unmarshal(b, &got)
	if len(got.Steps) != 6 || got.Steps[0]["wait_seconds"] != float64(60) || got.Steps[5]["kind"] != "repeat" {
		t.Fatal("step order/values lost")
	}
	if _, ok := got.Steps[0]["importance"]; ok {
		t.Fatal("wait importance was invented")
	}
	if len(got.Steps[5]) != 1 {
		t.Fatal("repeat must contain only kind")
	}
	if got.Steps[1]["importance"] != "default" {
		t.Fatal("normalized importance lost")
	}
	sources := got.Steps[3]["channel_sources"].([]any)
	if sources[0] != "service" || sources[1] != "team" {
		t.Fatal("channel priority changed")
	}
}

func TestChainArchiveConflictProducesErrorAndKeepsIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.Header.Get("If-Match") != `"`+strings.Repeat("a", 64)+`"` {
			t.Error("archive must send the observed ETag")
		}
		w.WriteHeader(409)
		io.WriteString(w, `{"error":{"code":"chain_in_use"}}`)
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL, "fixture-token")
	r := &escalationChainResource{configuredResource{client: c}}
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	state := tfsdk.State{Schema: sr.Schema}
	d := state.Set(t.Context(), testChain(t, []client.ChainStep{}))
	if d.HasError() {
		t.Fatal(d)
	}
	var resp resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("dependency conflict was converted into successful deletion")
	}
}

func TestChainCreateKeepsUUIDIfDetailReadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"`+chainID+`"}`)
		} else {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":"forbidden"}}`)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL, "fixture-token")
	r := &escalationChainResource{configuredResource{client: c}}
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	m := testChain(t, []client.ChainStep{})
	m.ID = types.StringUnknown()
	m.ETag = types.StringUnknown()
	state := tfsdk.State{Schema: sr.Schema}
	d := state.Set(t.Context(), m)
	if d.HasError() {
		t.Fatal(d)
	}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("read denial was not reported")
	}
	var id types.String
	resp.State.GetAttribute(t.Context(), path.Root("id"), &id)
	if id.ValueString() != chainID {
		t.Fatal("allocated chain identity was lost")
	}
}

func TestChainValidationRejectsFieldsFromAnotherStepKind(t *testing.T) {
	r := &escalationChainResource{}
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	state := tfsdk.State{Schema: sr.Schema}
	d := state.Set(t.Context(), testChain(t, []client.ChainStep{{Kind: "repeat", WaitSeconds: 60}}))
	if d.HasError() {
		t.Fatal(d)
	}
	var resp resource.ValidateConfigResponse
	r.ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("forbidden repeat settings were not diagnosed")
	}
}

func TestChainPlanRestoresDefaultWhenImportanceIsRemoved(t *testing.T) {
	r := &escalationChainResource{}
	var sr resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &sr)
	user := chainTeam
	m := testChain(t, []client.ChainStep{{Kind: "notify_user", TargetUserID: &user, Importance: "important"}})
	state := tfsdk.State{Schema: sr.Schema}
	state.Set(t.Context(), m)
	var rows []chainStepModel
	m.Steps.ElementsAs(t.Context(), &rows, false)
	rows[0].Importance = types.StringNull()
	m.Steps, _ = types.ListValueFrom(t.Context(), chainStepType, rows)
	config := tfsdk.State{Schema: sr.Schema}
	config.Set(t.Context(), m)
	plan := tfsdk.Plan{Schema: sr.Schema, Raw: state.Raw}
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(t.Context(), resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: config.Raw}, State: state, Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got chainModel
	resp.Plan.Get(t.Context(), &got)
	got.Steps.ElementsAs(t.Context(), &rows, false)
	if rows[0].Importance.ValueString() != "default" {
		t.Fatal("removing importance retained the previous important value")
	}
	if !got.ETag.IsUnknown() {
		t.Fatal("changing the planned default must allow the ETag to change")
	}
}
