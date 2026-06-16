package admin

import (
	"errors"
	"fmt"
	"net"
)

// ErrInvalidBind is returned when the bind address cannot be parsed.
var ErrInvalidBind = errors.New("admin: invalid bind address")

// Reason describes why a bind was allowed, for structured logging.
type Reason string

const (
	ReasonLoopbackDefault Reason = "loopback-default"
	ReasonTokenConfigured Reason = "token-configured"
	ReasonInsecureMode    Reason = "insecure-mode"
)

// Guard enforces the bind-policy table: non-loopback binds require either a
// configured token or explicit insecure-mode opt-in. Returns the allow reason
// on success, or an error naming both remediations on refusal.
func Guard(bind string, tokenConfigured, insecureListen bool) (Reason, error) {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrInvalidBind, bind, err)
	}

	if isLoopback(host) {
		return ReasonLoopbackDefault, nil
	}

	// Non-loopback bind: require a token or explicit insecure opt-in.
	if tokenConfigured {
		return ReasonTokenConfigured, nil
	}
	if insecureListen {
		return ReasonInsecureMode, nil
	}

	return "", fmt.Errorf("admin: refuses to bind %q: set admin.token or enable admin.insecure_listen=true", bind)
}

// isLoopback returns true for addresses that are loopback by any common form:
// the IPv4/IPv6 loopback addresses, or the hostname "localhost".
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return false
}
