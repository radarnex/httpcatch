package admin_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/config"
)

// newUIServer spins up a test server with the given token and returns its
// base URL + a no-redirect client.
func newUIServer(t *testing.T, token string) (string, *http.Client) {
	t.Helper()
	ts := newTestServer(t, token)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return ts.URL, client
}

// sessionCookieFor logs in and returns the session cookie from the response.
func sessionCookieFor(t *testing.T, baseURL, token string, client *http.Client) *http.Cookie {
	t.Helper()
	form := url.Values{"token": {token}}
	resp, err := client.PostForm(baseURL+"/auth/login", form)
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "httpcatch_session" {
			return c
		}
	}
	t.Fatal("no session cookie after login")
	return nil
}

func TestIndex_WithSession_Redirects303ToUIRequests(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)
	cookie := sessionCookieFor(t, base, testAdminToken, client)

	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/requests" {
		t.Errorf("Location: got %q want /ui/requests", loc)
	}
}

func TestIndex_WithoutSession_Redirects303(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET / no session: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/login?next=") {
		t.Errorf("Location %q: expected /login?next=", loc)
	}
}

func TestStatic_AppCSS_Returns200WithHeaders(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	req, _ := http.NewRequest(http.MethodGet, base+"/static/app.css", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /static/app.css: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("Content-Type: got %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: got %q", cc)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("ETag: header missing")
	}
}

func TestStatic_AppJS_Returns200WithHeaders(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	req, _ := http.NewRequest(http.MethodGet, base+"/static/app.js", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /static/app.js: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
		t.Errorf("Content-Type: got %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: got %q", cc)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("ETag: header missing")
	}
}

func TestStatic_IfNoneMatch_Returns304(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	// First request to capture the ETag.
	resp1, err := client.Get(base + "/static/app.css")
	if err != nil {
		t.Fatalf("first GET /static/app.css: %v", err)
	}
	io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()
	etag := resp1.Header.Get("ETag")
	if etag == "" {
		t.Fatal("ETag missing on first response")
	}

	// Second request with If-None-Match.
	req2, _ := http.NewRequest(http.MethodGet, base+"/static/app.css", nil)
	req2.Header.Set("If-None-Match", etag)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("conditional GET /static/app.css: %v", err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("status: got %d want 304", resp2.StatusCode)
	}
}

func TestStatic_IndexHTML_Returns404(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	resp, err := client.Get(base + "/static/index.html")
	if err != nil {
		t.Fatalf("GET /static/index.html: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
}

func TestStatic_MissingFile_Returns404(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	resp, err := client.Get(base + "/static/missing.css")
	if err != nil {
		t.Fatalf("GET /static/missing.css: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
}

func TestStatic_Unauthenticated_Returns200(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	// No cookie, no auth — static assets are public.
	resp, err := client.Get(base + "/static/app.css")
	if err != nil {
		t.Fatalf("GET /static/app.css unauthenticated: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

// findIDs walks an HTML parse tree and collects all id attribute values.
func findIDs(n *html.Node, ids map[string]bool) {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" {
				ids[a.Val] = true
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		findIDs(c, ids)
	}
}

func TestLayout_StructuralIDs(t *testing.T) {
	t.Parallel()

	cfg := config.AdminConfig{
		Bind:          "127.0.0.1:0",
		Token:         testAdminToken,
		SessionTTL:    time.Hour,
		SessionSecure: false,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}

	base, client := newUIServer(t, testAdminToken)
	cookie := sessionCookieFor(t, base, testAdminToken, client)

	req, _ := http.NewRequest(http.MethodGet, base+"/ui/requests", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	defer resp.Body.Close()

	doc, err := html.Parse(resp.Body)
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}

	ids := make(map[string]bool)
	findIDs(doc, ids)

	required := []string{
		"chip-unredacted",
		"chip-dropped",
		"chip-redaction-errors",
		"chip-service",
		"chip-correlation",
		"buildinfo",
	}
	for _, id := range required {
		if !ids[id] {
			t.Errorf("layout: missing element with id=%q", id)
		}
	}

	_ = srv // used only to confirm New() succeeds; router is on the test server
}
