package client

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"strings"
)

type DeliveryTargetInput struct {
	ChannelID string          `json:"channel_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
}

func (DeliveryTargetInput) String() string   { return "[REDACTED]" }
func (DeliveryTargetInput) GoString() string { return "[REDACTED]" }

type DeliveryTarget struct {
	ID              string `json:"id"`
	ChannelID       string `json:"channel_id"`
	Kind            string `json:"kind"`
	ContractVersion string `json:"contract_version"`
	Enabled         bool   `json:"enabled"`
}

func (in DeliveryTargetInput) payload() ([]byte, error) {
	if !validResponseID(in.ChannelID) {
		return nil, errors.New("channel_id must be a nonzero canonical lowercase UUID")
	}
	raw := jsontext.Value(in.Snapshot)
	if raw.Kind() != '{' || !raw.IsValid() {
		return nil, errors.New("snapshot_json must encode one UTF-8 JSON object with unique member names")
	}
	body, err := json.Marshal(in)
	if err != nil || len(body) > 8192 {
		return nil, errors.New("the encoded delivery target request must fit within 8192 bytes, including channel_id and snapshot")
	}
	return body, nil
}

func (in DeliveryTargetInput) Validate() error {
	_, err := in.payload()
	return err
}

func (c *Client) deliveryTargetRequest(ctx context.Context, method, id string, input any, wantStatus int) ([]byte, bool, error) {
	route := "/v1/delivery/targets"
	if method != http.MethodPost {
		if !validResponseID(id) {
			return nil, false, errors.New("a nonzero canonical lowercase delivery target UUID is required")
		}
		route += "/" + id
	}
	raw, status, err := c.unversionedJSONRequest(ctx, method, route, input)
	committed := status >= 200 && status < 300 && method != http.MethodGet
	if err != nil {
		return nil, committed, err
	}
	if status != wantStatus || wantStatus == http.StatusNoContent && len(bytes.TrimSpace(raw)) != 0 {
		return raw, committed, errors.New("OnPager returned an unexpected delivery target response status or body")
	}
	return raw, committed, nil
}

func decodeDeliveryTarget(raw []byte, expectedID, createChannel string) (DeliveryTarget, error) {
	// Decode the allocated UUID independently so that a malformed create receipt
	// can retain ownership and be recovered through the direct detail endpoint.
	var receipt struct {
		ID        string `json:"id"`
		ChannelID string `json:"channel_id"`
	}
	_ = json.Unmarshal(raw, &receipt)
	if !validResponseID(receipt.ID) || expectedID != "" && receipt.ID != expectedID {
		return DeliveryTarget{}, errors.New("OnPager returned an invalid delivery target identity")
	}
	if createChannel != "" && validResponseID(receipt.ChannelID) && receipt.ChannelID != createChannel {
		return DeliveryTarget{}, errors.New("OnPager returned a create receipt for a different delivery channel")
	}
	var out struct {
		DeliveryTarget
		Enabled *bool `json:"enabled"`
	}
	if json.Unmarshal(raw, &out) != nil || !validResponseID(out.ChannelID) || strings.TrimSpace(out.Kind) == "" || strings.TrimSpace(out.ContractVersion) == "" || out.Enabled == nil ||
		createChannel != "" && !*out.Enabled {
		return DeliveryTarget{ID: receipt.ID}, errors.New("OnPager returned incomplete or inconsistent delivery target metadata")
	}
	out.DeliveryTarget.Enabled = *out.Enabled
	return out.DeliveryTarget, nil
}

func (c *Client) CreateDeliveryTarget(ctx context.Context, in DeliveryTargetInput) (DeliveryTarget, error) {
	body, err := in.payload()
	if err != nil {
		return DeliveryTarget{}, err
	}
	raw, committed, err := c.deliveryTargetRequest(ctx, http.MethodPost, "", json.RawMessage(body), http.StatusCreated)
	if !committed {
		return DeliveryTarget{}, err
	}
	out, decodeErr := decodeDeliveryTarget(raw, "", in.ChannelID)
	if err != nil {
		return out, err
	}
	return out, decodeErr
}

func (c *Client) GetDeliveryTarget(ctx context.Context, id string) (DeliveryTarget, error) {
	raw, _, err := c.deliveryTargetRequest(ctx, http.MethodGet, id, nil, http.StatusOK)
	if err != nil {
		return DeliveryTarget{}, err
	}
	return decodeDeliveryTarget(raw, id, "")
}

func (c *Client) UpdateDeliveryTarget(ctx context.Context, id string, enabled bool) (bool, error) {
	body := struct {
		Enabled bool `json:"enabled"`
	}{enabled}
	_, committed, err := c.deliveryTargetRequest(ctx, http.MethodPut, id, body, http.StatusNoContent)
	return committed, err
}

func (c *Client) DisableDeliveryTarget(ctx context.Context, id string) error {
	_, _, err := c.deliveryTargetRequest(ctx, http.MethodDelete, id, nil, http.StatusNoContent)
	return allowAbsent(err)
}
