package admin

import (
	"net/http"
	"net/url"
	"strings"
)

// csrfOriginCheck returns middleware that rejects cross-site POSTs to auth
// routes. It honours Sec-Fetch-Site when present, otherwise falls back to a
// host comparison between Origin and the request's own Host. Requests with
// neither header (curl, scripted clients) are allowed through — the session
// cookie is HttpOnly + SameSite=Lax, and a CSRF attack from a real browser
// always arrives with one of the two headers set.
func csrfOriginCheck(limiter *AuthLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isSameOrigin(r) {
				if limiter != nil {
					limiter.RecordCSRFBlocked()
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isSameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		switch site {
		case "same-origin", "none":
			return true
		case "same-site":
			return originMatchesHost(r.Header.Get("Origin"), r.Host)
		default:
			return false
		}
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return originMatchesHost(origin, r.Host)
}

func originMatchesHost(origin, host string) bool {
	if origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}
