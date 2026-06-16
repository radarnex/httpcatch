package admin_test

import (
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSecurityHeaders_HTMLRoute_SetsAllFourHeaders verifies that the HTML
// security middleware fires on the login page and delivers all required headers
// with the exact expected values.
func TestSecurityHeaders_HTMLRoute_SetsAllFourHeaders(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	resp, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	wantCSP := "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	if got := resp.Header.Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy:\n got  %q\n want %q", got, wantCSP)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options: got %q want nosniff", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options: got %q want DENY", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy: got %q want no-referrer", got)
	}
}

// TestSecurityHeaders_JSONRoute_SetsNosniffNoCSP verifies that the JSON
// security middleware fires on /healthz: nosniff is set but no CSP is present.
func TestSecurityHeaders_JSONRoute_SetsNosniffNoCSP(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options: got %q want nosniff", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options: got %q want DENY", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy: got %q want no-referrer", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "" {
		t.Errorf("Content-Security-Policy: expected empty on JSON route, got %q", got)
	}
}

// TestSecurityHeaders_StaticRoute_SetsNosniff verifies that the static asset
// handler also carries nosniff and no CSP.
func TestSecurityHeaders_StaticRoute_SetsNosniff(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	resp, err := http.Get(ts.URL + "/static/app.css")
	if err != nil {
		t.Fatalf("GET /static/app.css: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options: got %q want nosniff", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "" {
		t.Errorf("Content-Security-Policy: expected empty on static route, got %q", got)
	}
}

// TestSecurityHeaders_JSONContentTypeCharset verifies that the healthz endpoint
// returns text/plain with charset (healthz is text, not JSON; use the status
// endpoint via bearer to confirm JSON charset).
func TestSecurityHeaders_StatusEndpoint_JSONCharset(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q want application/json*", ct)
	}
	if !strings.Contains(ct, "charset=utf-8") {
		t.Errorf("Content-Type: got %q missing charset=utf-8", ct)
	}
}

// TestSecurityHeaders_AuthedUIRoute_SetsCSP verifies that CSP is set on the
// authenticated HTML UI group (using a bearer token to bypass login).
func TestSecurityHeaders_AuthedUIRoute_SetsCSP(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/requests", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("Content-Security-Policy: expected non-empty on UI HTML route")
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options: got %q want DENY", got)
	}
}

// TestSecurityHeaders_NoInlineScriptInTemplates asserts that neither
// layout.html nor login.html contain inline script bodies. An inline script
// body is a <script> tag with non-whitespace content and no src attribute.
// This pins the externalisation of the theme-bootstrap script.
func TestSecurityHeaders_NoInlineScriptInTemplates(t *testing.T) {
	t.Parallel()

	// scriptTagRe matches any <script ...> opening tag so we can inspect its attributes.
	scriptTagRe := regexp.MustCompile(`(?i)<script([^>]*)>`)

	for _, name := range []string{"ui/layout.html", "ui/login.html"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		content := string(data)
		// Find every <script ...> tag and reject any that lacks a src= attribute,
		// which would mean an inline script body follows.
		for _, m := range scriptTagRe.FindAllStringSubmatch(content, -1) {
			attrs := m[1]
			if !strings.Contains(attrs, "src=") {
				t.Errorf("%s: <script> tag without src attribute — possible inline script body: %q", name, m[0])
			}
		}
	}
}

// TestSecurityHeaders_NoInlineStyleInTemplates asserts that login.html no
// longer contains inline <style> blocks. This pins the externalisation to
// login.css.
func TestSecurityHeaders_NoInlineStyleInTemplates(t *testing.T) {
	t.Parallel()

	inlineStyleRe := regexp.MustCompile(`(?i)<style[^>]*>[^<]+</style>`)

	data, err := os.ReadFile("ui/login.html")
	if err != nil {
		t.Fatalf("read login.html: %v", err)
	}
	if inlineStyleRe.Match(data) {
		t.Error("login.html: contains an inline <style> block; externalise to a .css file")
	}
}
