package admin

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Option configures the admin auth middleware for a specific route.
type Option func(*middlewareCfg)

type middlewareCfg struct {
	cookieAuth bool
}

// WithCookieAuth controls whether the session cookie is accepted as a
// credential. Set to false on routes where bearer-only auth is required (e.g.
// the future events API) to eliminate browser-based CSRF risk at the auth layer.
func WithCookieAuth(enabled bool) Option {
	return func(c *middlewareCfg) {
		c.cookieAuth = enabled
	}
}

const sessionCookieName = "httpcatch_session"

// Middleware returns a chi-compatible middleware function that gates the next
// handler behind admin auth. By default it accepts either a valid
// Authorization: Bearer token or a valid session cookie. Pass WithCookieAuth(false)
// to restrict a route to bearer-only authentication.
func Middleware(adminToken string, store *SessionStore, opts ...Option) func(http.Handler) http.Handler {
	cfg := &middlewareCfg{cookieAuth: true}
	for _, o := range opts {
		o(cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticate(r, adminToken, store, cfg) {
				next.ServeHTTP(w, r)
				return
			}
			denyRequest(w, r)
		})
	}
}

// authenticate returns true when the request carries a valid credential.
func authenticate(r *http.Request, adminToken string, store *SessionStore, cfg *middlewareCfg) bool {
	// Bearer token check.
	if bearer := extractBearer(r); bearer != "" {
		if checkBearer(bearer, adminToken) {
			return true
		}
	}

	// Session cookie check (skipped when cookieAuth is disabled for this route).
	if cfg.cookieAuth {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			if store.Validate(cookie.Value) {
				return true
			}
		}
	}

	return false
}

// extractBearer returns the token value from Authorization: Bearer <token>, or
// an empty string when the header is absent or malformed.
func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// checkBearer performs a constant-time comparison of the presented token
// against the configured admin token. An empty adminToken always returns false
// so that an unconfigured admin port never grants access via an empty bearer.
func checkBearer(presented, adminToken string) bool {
	if adminToken == "" {
		return false
	}
	if len(presented) != len(adminToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(adminToken)) == 1
}

// denyRequest writes the appropriate failure response: a 303 redirect to the
// login page for HTML clients, or a 401 with WWW-Authenticate for API clients.
// HTML detection requires the literal substring "text/html" in the Accept
// header; "*/*" is not treated as html.
func denyRequest(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		loginURL := fmt.Sprintf("/login?next=%s", url.QueryEscape(r.RequestURI))
		http.Redirect(w, r, loginURL, http.StatusSeeOther)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="httpcatch"`)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("unauthorized\n"))
}
