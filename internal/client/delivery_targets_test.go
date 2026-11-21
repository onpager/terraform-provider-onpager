package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/onpager/terraform-provider-onpager/internal/client"
)

func TestDeliveryTargetExplicitLifecycle(t *testing.T) {
	var methods []string
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		wantPath := "/v1/delivery/targets/" + iamID
		if r.Method == "POST" {
			wantPath = "/v1/delivery/targets"
		}
		if r.URL.Path != wantPath || r.URL.RawQuery != "" || r.Header.Get("If-Match") != "" {
			t.Error("unexpected delivery target route, query or precondition")
		}
		body, _ := io.ReadAll(r.Body)
		switch r.Method {
		case "POST":
			want := `{"channel_id":"` + iamRecipient + `","snapshot":{"address":"fixture"}}`
			if string(body) != want {
				t.Error("create must send only the channel and embedded snapshot object")
			}
			w.WriteHeader(201)
		case "GET":
			if len(body) != 0 {
				t.Error("unexpected GET body")
			}
		case "PUT":
			if string(body) != `{"enabled":false}` {
				t.Error("update must only submit enabled")
			}
			w.WriteHeader(204)
			return
		case "DELETE":
			if len(body) != 0 {
				t.Error("unexpected DELETE body")
			}
			w.WriteHeader(204)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"channel_id":%q,"kind":"bot","contract_version":"v1","enabled":%t}`, iamID, iamRecipient, r.Method == "POST")
	})
	in := client.DeliveryTargetInput{ChannelID: iamRecipient, Snapshot: json.RawMessage(`{ "address" : "fixture" }`)}
	created, err := c.CreateDeliveryTarget(t.Context(), in)
	if err != nil || created.ID != iamID || !created.Enabled || created.Kind != "bot" || created.ContractVersion != "v1" {
		t.Fatalf("invalid target receipt: %v", err)
	}
	committed, err := c.UpdateDeliveryTarget(t.Context(), iamID, false)
	if err != nil || !committed {
		t.Fatalf("unable to disable: %v", err)
	}
	read, err := c.GetDeliveryTarget(t.Context(), iamID)
	if err != nil || read.Enabled || read.ChannelID != iamRecipient {
		t.Fatalf("invalid disabled target: %v", err)
	}
	if err = c.DisableDeliveryTarget(t.Context(), iamID); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, " ") != "POST PUT GET DELETE" {
		t.Fatalf("unexpected requests: %v", methods)
	}
}

func TestDeliveryTargetInvalidInputsAndRedaction(t *testing.T) {
	calls := 0
	c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(422)
		fmt.Fprint(w, `{"error":{"code":"validation_failed","message":"private-marker"}}`)
	})
	for _, snap := range []string{"", "null", "[]", `{"private-marker":}`, `{"a":{"a":1,"a":2}}`, `{"a":2} {}`, `{"large":"` + strings.Repeat("z", 8190) + `"}`} {
		in := client.DeliveryTargetInput{ChannelID: iamID, Snapshot: json.RawMessage(snap)}
		_, err := c.CreateDeliveryTarget(t.Context(), in)
		if err == nil || strings.Contains(err.Error(), "private-marker") || calls != 0 {
			t.Fatal("invalid snapshot accepted, submitted, or exposed")
		}
	}
	in := client.DeliveryTargetInput{ChannelID: iamID, Snapshot: json.RawMessage(`{"id":"private-marker"}`)}
	_, err := c.CreateDeliveryTarget(t.Context(), in)
	var api *client.APIError
	if !errors.As(err, &api) || api.StatusCode != 422 || calls != 1 || strings.Contains(err.Error(), "private-marker") || strings.Contains(fmt.Sprintf("%v %#v", in, in), "private-marker") {
		t.Fatal("failure not preserved or snapshot exposed")
	}
}

func TestDeliveryTargetInvalidReceiptsRetainOnlyConfirmedIdentity(t *testing.T) {
	rsp := `{"id":"` + iamID + `","channel_id":"` + iamRecipient + `","kind":"bot","contract_version":"v1","enabled":true}`
	for _, tc := range []struct {
		name, body string
		post       bool
		wantID     string
	}{
		{"missingEnabled", strings.Replace(rsp, `,"enabled":true`, "", 1), true, iamID},
		{"foreignCreateChannel", strings.Replace(rsp, iamRecipient, iamID, 1), true, ""},
		{"disabledCreate", strings.Replace(rsp, "true", "false", 1), true, iamID},
		{"emptyID", strings.Replace(rsp, iamID, "", 1), true, ""},
		{"foreignRead", strings.Replace(rsp, iamID, iamRecipient, 1), false, ""},
		{"nullEnabled", strings.Replace(rsp, "true", "null", 1), false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := iamClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.post {
					w.WriteHeader(201)
				}
				fmt.Fprint(w, tc.body)
			})
			if tc.post {
				rec, err := c.CreateDeliveryTarget(t.Context(), client.DeliveryTargetInput{ChannelID: iamRecipient, Snapshot: json.RawMessage(`{}`)})
				if err == nil || rec.ID != tc.wantID {
					t.Fatal("invalid create receipt accepted or allocated identity lost")
				}
			} else {
				_, err := c.GetDeliveryTarget(t.Context(), iamID)
				if err == nil {
					t.Fatal("invalid read accepted")
				}
			}
		})
	}
}
