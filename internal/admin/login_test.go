package admin_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/config"
)

func newTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	cfg := config.AdminConfig{
		Bind:          "127.0.0.1:0",
		Token:         token,
		SessionTTL:    time.Hour,
		SessionSecure: false,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

func TestLoginPage_Returns200WithForm(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	resp, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `action="/auth/login"`) {
		t.Error("body: missing form action /auth/login")
	}
	if !strings.Contains(bodyStr, `type="password"`) {
		t.Error("body: missing password input")
	}
}

func TestLoginPage_NextParam_AppearsAsHiddenInput(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	resp, err := http.Get(ts.URL + "/login?next=/admin/ping")
	if err != nil {
		t.Fatalf("GET /login?next=: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `type="hidden" name="next" value="/admin/ping"`) {
		t.Errorf("body: missing hidden next input; body excerpt: %s", bodyStr[:min(len(bodyStr), 500)])
	}
}

func TestLoginPage_UnsafeNext_IsDropped(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	resp, err := http.Get(ts.URL + "/login?next=//evil.com")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "//evil.com") {
		t.Error("body: unsafe next param was not dropped")
	}
	if strings.Contains(string(body), `type="hidden" name="next"`) {
		t.Error("body: hidden next input present for unsafe next value")
	}
}

func TestLoginPage_ErrParam_ShowsErrorMessage(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	resp, err := http.Get(ts.URL + "/login?err=1")
	if err != nil {
		t.Fatalf("GET /login?err=1: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Invalid token") {
		t.Error("body: expected 'Invalid token' error message")
	}
}

func TestLoginPost_CorrectToken_SetsCookieAndRedirects(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	form := url.Values{"token": {testAdminToken}}
	resp, err := client.PostForm(ts.URL+"/auth/login", form)
	if err != nil {
		t.Fatalf("POST /auth/login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "httpcatch_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("Set-Cookie: httpcatch_session not present")
	}
	if !sessionCookie.HttpOnly {
		t.Error("cookie: HttpOnly not set")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Error("cookie: SameSite != Lax")
	}
	if sessionCookie.Path != "/" {
		t.Errorf("cookie: Path = %q want /", sessionCookie.Path)
	}
	if sessionCookie.MaxAge != int(time.Hour/time.Second) {
		t.Errorf("cookie: MaxAge = %d want %d", sessionCookie.MaxAge, int(time.Hour/time.Second))
	}
	if sessionCookie.Secure {
		t.Error("cookie: Secure set but SessionSecure=false in config")
	}
}

func TestLoginPost_CorrectToken_WithNext_RedirectsToNext(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	form := url.Values{"token": {testAdminToken}, "next": {"/status"}}
	resp, err := client.PostForm(ts.URL+"/auth/login", form)
	if err != nil {
		t.Fatalf("POST /auth/login with next: %v", err)
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc != "/status" {
		t.Errorf("Location: got %q want /status", loc)
	}
}

func TestLoginPost_WrongToken_Returns401NoCookie(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)

	form := url.Values{"token": {"wrong-token"}}
	resp, err := http.PostForm(ts.URL+"/auth/login", form)
	if err != nil {
		t.Fatalf("POST /auth/login wrong token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "httpcatch_session" {
			t.Error("Set-Cookie: httpcatch_session should not be set on wrong token")
		}
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "wrong-token") {
		t.Error("body: submitted token was echoed in response")
	}
}

func TestLoginPost_MissingToken_Returns400(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)

	form := url.Values{}
	resp, err := http.PostForm(ts.URL+"/auth/login", form)
	if err != nil {
		t.Fatalf("POST /auth/login no token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
}

func TestLogout_ValidCookie_RevokesAndRedirects(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	sess, err := store.Create(time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cfg := config.AdminConfig{
		Bind:       "127.0.0.1:0",
		Token:      testAdminToken,
		SessionTTL: time.Hour,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	// First log in through the test server to get a real session in its store.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	form := url.Values{"token": {testAdminToken}}
	loginResp, err := client.PostForm(ts.URL+"/auth/login", form)
	if err != nil {
		t.Fatalf("POST /auth/login: %v", err)
	}
	loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == "httpcatch_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie after login")
	}
	_ = sess // only used to confirm the pattern

	// Now log out.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/logout", nil)
	req.AddCookie(sessionCookie)
	logoutResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/logout: %v", err)
	}
	logoutResp.Body.Close()

	if logoutResp.StatusCode != http.StatusSeeOther {
		t.Errorf("logout status: got %d want 303", logoutResp.StatusCode)
	}
	if logoutResp.Header.Get("Location") != "/login" {
		t.Errorf("logout Location: got %q want /login", logoutResp.Header.Get("Location"))
	}

	// Confirm the clearing cookie is set (MaxAge == -1).
	var cleared *http.Cookie
	for _, c := range logoutResp.Cookies() {
		if c.Name == "httpcatch_session" {
			cleared = c
			break
		}
	}
	if cleared == nil {
		t.Fatal("clearing cookie not set on logout response")
	}
	if cleared.MaxAge != -1 {
		t.Errorf("clearing cookie MaxAge: got %d want -1", cleared.MaxAge)
	}

	// After logout the old cookie must no longer grant access.
	statusReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	statusReq.Header.Set("Accept", "application/json")
	statusReq.AddCookie(sessionCookie)
	statusResp, err := client.Do(statusReq)
	if err != nil {
		t.Fatalf("GET /status after logout: %v", err)
	}
	statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status after logout: got %d want 401", statusResp.StatusCode)
	}
}

func TestLogout_NoCookie_NocrashAnd303(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/logout", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/logout no cookie: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
}

func TestLogout_BogusCookie_NocrashAnd303(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, testAdminToken)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "httpcatch_session", Value: "total-garbage"})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/logout bogus cookie: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
}

