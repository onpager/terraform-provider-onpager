package client

import (
	"errors"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errProviderBaseURL = errors.New("configuration.base_url must be empty or an absolute HTTP(S) URL of at most 16384 bytes with a valid host, port and clean path, without credentials, query or fragment")

var escapedByte = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

// RFC 3986 permits decoding unreserved characters. Keep escaped delimiters
// intact so a reverse proxy receives the configured path.
func canonicalEscape(escape string) string {
	n, _ := strconv.ParseUint(escape[1:], 16, 8)
	c := byte(n)
	if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", rune(c)) {
		return string(c)
	}
	return strings.ToUpper(escape)
}

// NormalizeProviderBaseURL follows the Control contract for bot and voice API
// destinations. An empty value selects the provider default where available.
// Errors omit the input because an invalid URL may contain credentials.
func NormalizeProviderBaseURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 16384 || !utf8.ValidString(raw) || strings.Contains(raw, "#") || strings.ContainsFunc(raw, unicode.IsSpace) {
		return "", errProviderBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" {
		return "", errProviderBaseURL
	}
	if strings.Contains(u.Path, "\\") || strings.ContainsFunc(u.Path, unicode.IsControl) || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return "", errProviderBaseURL
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = escapedByte.ReplaceAllStringFunc(strings.TrimRight(u.RawPath, "/"), canonicalEscape)
	if u.Path != "" && path.Clean(u.Path) != u.Path {
		return "", errProviderBaseURL
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return "", errProviderBaseURL
		}
		port = strconv.FormatUint(n, 10)
	} else if strings.HasSuffix(u.Host, ":") {
		return "", errProviderBaseURL
	}
	switch {
	case port != "" && !(u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443"):
		u.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		u.Host = "[" + host + "]"
	default:
		u.Host = host
	}
	return u.String(), nil
}
