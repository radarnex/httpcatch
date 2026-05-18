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
)

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
	if v := strings.TrimSpace(headers.Get(name)); v != "" {
		return v, ServiceSourceHeader
	}

	host := strings.TrimSpace(headers.Get(HostHeader))
	if host == "" {
		return UnknownService, ServiceSourceUnknown
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if host == "" {
		return UnknownService, ServiceSourceUnknown
	}
	return host, ServiceSourceHost
}
