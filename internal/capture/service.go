package capture

import (
	"net"
	"net/http"
	"strings"
)

const (
	DefaultServiceHeader = "X-Httpcatch-Service"
	HostHeader           = "Host"
	UnknownService       = "unknown"

	ServiceSourceHeader  = "header"
	ServiceSourceHost    = "host"
	ServiceSourceUnknown = "unknown"

	// maxServiceLabelBytes caps the length accepted for a service label. Values
	// longer than this are treated as absent so the chain falls through to the
	// next candidate.
	maxServiceLabelBytes = 256
)

// SanitiseServiceLabel trims whitespace, lowercases, and validates a candidate
// service label. It returns "" when the value contains control characters
// (U+0000–U+001F or U+007F) or exceeds maxServiceLabelBytes, so callers can
// fall through to the next step in the resolution chain.
func SanitiseServiceLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxServiceLabelBytes {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return strings.ToLower(s)
}

// IdentifyService resolves the service label for a captured request using
// the chain configured-header → Host → "unknown". The configured header
// defaults to DefaultServiceHeader when configuredHeader is empty. Host is
// read from the headers map (the capture handler stamps r.Host onto the
// clone it passes here), lowercased, and stripped of a trailing port.
func IdentifyService(headers http.Header, configuredHeader string) (value, source string) {
	name := configuredHeader
	if name == "" {
		name = DefaultServiceHeader
	}
	if raw := headers.Get(name); raw != "" {
		if v := SanitiseServiceLabel(raw); v != "" {
			return v, ServiceSourceHeader
		}
	}

	host := headers.Get(HostHeader)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if v := SanitiseServiceLabel(host); v != "" {
		return v, ServiceSourceHost
	}
	return UnknownService, ServiceSourceUnknown
}
