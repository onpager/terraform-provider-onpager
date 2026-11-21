package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

var safeCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

// APIError deliberately excludes raw response bodies, URLs and server messages.
type APIError struct {
	StatusCode int
	Code       string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("OnPager API returned HTTP %d (%s)", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("OnPager API returned HTTP %d", e.StatusCode)
}

func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Code == "not_found"
}

func (c *Client) json(ctx context.Context, method, route string, input, output any, revision int64) error {
	tag := ""
	if revision > 0 {
		tag = fmt.Sprintf(`"%d"`, revision)
	}
	return c.jsonWithETag(ctx, method, route, input, output, tag)
}

func (c *Client) jsonWithETag(ctx context.Context, method, route string, input, output any, tag string) error {
	data, _, err := c.rawJSONRequest(ctx, method, route, input, tag)
	if err != nil {
		return err
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return errors.New("OnPager returned an invalid JSON response")
		}
	}
	return nil
}

func allowAbsent(err error) error {
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) unversionedJSONRequest(ctx context.Context, method, route string, input any) ([]byte, int, error) {
	return c.rawJSONRequest(ctx, method, route, input, "")
}

// rawJSONRequest retains the HTTP status even if the response body cannot be
// read. Each endpoint validates its own success statuses and receipt.
func (c *Client) rawJSONRequest(ctx context.Context, method, route string, input any, tag string) ([]byte, int, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, 0, errors.New("could not encode OnPager request")
		}
		body = bytes.NewReader(data)
	}
	var headers http.Header
	if tag != "" {
		headers = http.Header{"If-Match": []string{tag}}
	}
	res, err := c.do(ctx, method, route, body, headers)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	const maxResponseBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, res.StatusCode, errors.New("could not read OnPager response")
	}
	if len(data) > maxResponseBytes {
		return nil, res.StatusCode, errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if !success {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		code := envelope.Error.Code
		if !safeCode.MatchString(code) {
			code = ""
		}
		return nil, res.StatusCode, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	return data, res.StatusCode, nil
}
