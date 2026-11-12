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
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return errors.New("could not encode OnPager request")
		}
		body = bytes.NewReader(encoded)
	}
	headers := make(http.Header)
	if tag != "" {
		headers.Set("If-Match", tag)
	}
	resp, err := c.do(ctx, method, route, body, headers)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer resp.Body.Close()
	const maxResponseBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("could not read OnPager response")
	}
	if len(data) > maxResponseBytes {
		return errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
		return &APIError{StatusCode: resp.StatusCode, Code: code}
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
