package client

import (
	"errors"
	"strings"
	"time"
	_ "time/tzdata" // Validate IANA names without host zoneinfo or runtime downloads.
)

// Option configures one independent client without network requests.
type Option func(*Client) error

// WithTimezone sends an explicit request-context override for every operation.
// No timestamp is interpreted or converted here; the backend owns conversions.
func WithTimezone(zone string) Option {
	return func(c *Client) error {
		if zone == "" || zone == "Local" || len(zone) > 255 || strings.TrimSpace(zone) != zone || strings.Contains(zone, "..") || strings.ContainsAny(zone, "\\\x00\r\n,") || strings.HasPrefix(zone, "/") {
			return errors.New("timezone must be one valid IANA time zone name")
		}
		if _, err := time.LoadLocation(zone); err != nil {
			return errors.New("timezone must be one valid IANA time zone name")
		}
		c.timezone = zone
		return nil
	}
}

// Timezone is the explicit override, or empty when server defaults are selected.
// It is not a resolved actor/profile timezone and must not be used for conversion.
func (c *Client) Timezone() string { return c.timezone }
