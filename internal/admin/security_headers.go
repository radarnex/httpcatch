package admin

import "net/http"

// cspHTML is the Content-Security-Policy applied to HTML-rendering routes.
// frame-ancestors 'none' is the clickjacking defence; combined with
// X-Frame-Options: DENY it covers both legacy and modern browsers.
const cspHTML = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// htmlSecurityHeaders returns a middleware that sets browser-hardening response
// headers appropriate for HTML-rendered pages.
func htmlSecurityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", cspHTML)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}
}

// jsonSecurityHeaders returns a middleware that sets browser-hardening response
// headers appropriate for JSON API routes. No CSP is applied; these endpoints
// do not render HTML and a CSP would be noise.
func jsonSecurityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}
}
