package admin_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/config"
)

// startServer boots a real admin server on an ephemeral port and returns its
// base URL. The server is shut down when the test context is cancelled.
func startServer(t *testing.T, token string) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := config.AdminConfig{
		Bind:          addr,
		Token:         token,
		SessionTTL:    time.Hour,
		SessionSecure: false,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}

	ctx := t.Context()
	go func() { _ = srv.Serve(ctx) }()

	// Wait until the server is reachable.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err2 := http.Get("http://" + addr + "/healthz")
		if err2 == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "http://" + addr
}

func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestE2E_CookieAuthFlow(t *testing.T) {
	t.Parallel()

	base := startServer(t, testAdminToken)
	client := noFollowClient()

	// Step 1: unauthenticated request with Accept: text/html → 303 to login.
	req1, _ := http.NewRequest(http.MethodGet, base+"/admin/ping", nil)
	req1.Header.Set("Accept", "text/html")
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("step1 GET /admin/ping: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusSeeOther {
		t.Errorf("step1: got %d want 303", resp1.StatusCode)
	}
	loc1 := resp1.Header.Get("Location")
	if !strings.Contains(loc1, "/login?next=") {
		t.Errorf("step1: Location %q does not contain /login?next=", loc1)
	}
	if !strings.Contains(loc1, url.QueryEscape("/admin/ping")) {
		t.Errorf("step1: Location %q does not contain URL-encoded /admin/ping", loc1)
	}

	// Step 2: login with correct token → 303 + Set-Cookie.
	form := url.Values{"token": {testAdminToken}}
	resp2, err := client.PostForm(base+"/auth/login", form)
	if err != nil {
		t.Fatalf("step2 POST /auth/login: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Errorf("step2: got %d want 303", resp2.StatusCode)
	}
	var sessionCookie *http.Cookie
	for _, c := range resp2.Cookies() {
		if c.Name == "httpcatch_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("step2: no session cookie in response")
	}

	// Step 3: GET /admin/ping with cookie → 200 pong.
	req3, _ := http.NewRequest(http.MethodGet, base+"/admin/ping", nil)
	req3.AddCookie(sessionCookie)
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("step3 GET /admin/ping: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("step3: got %d want 200", resp3.StatusCode)
	}
	if string(body3) != "pong" {
		t.Errorf("step3: body = %q want pong", string(body3))
	}

	// Step 4: logout with cookie → 303 + clearing cookie.
	req4, _ := http.NewRequest(http.MethodPost, base+"/auth/logout", nil)
	req4.AddCookie(sessionCookie)
	resp4, err := client.Do(req4)
	if err != nil {
		t.Fatalf("step4 POST /auth/logout: %v", err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusSeeOther {
		t.Errorf("step4: got %d want 303", resp4.StatusCode)
	}
	var cleared *http.Cookie
	for _, c := range resp4.Cookies() {
		if c.Name == "httpcatch_session" {
			cleared = c
			break
		}
	}
	if cleared == nil || cleared.MaxAge != -1 {
		t.Errorf("step4: expected clearing cookie with MaxAge=-1")
	}

	// Step 5: GET /admin/ping with revoked cookie → 303 to login (html accept).
	req5, _ := http.NewRequest(http.MethodGet, base+"/admin/ping", nil)
	req5.Header.Set("Accept", "text/html")
	req5.AddCookie(sessionCookie)
	resp5, err := client.Do(req5)
	if err != nil {
		t.Fatalf("step5 GET /admin/ping revoked cookie: %v", err)
	}
	resp5.Body.Close()
	if resp5.StatusCode != http.StatusSeeOther {
		t.Errorf("step5: got %d want 303", resp5.StatusCode)
	}
}

func TestE2E_BearerAuthFlow(t *testing.T) {
	t.Parallel()

	base := startServer(t, testAdminToken)
	client := noFollowClient()

	// Valid bearer → 200.
	req1, _ := http.NewRequest(http.MethodGet, base+"/admin/ping", nil)
	req1.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("GET /admin/ping valid bearer: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("valid bearer: got %d want 200", resp1.StatusCode)
	}
	if string(body1) != "pong" {
		t.Errorf("valid bearer: body = %q want pong", string(body1))
	}

	// Wrong bearer → 401.
	req2, _ := http.NewRequest(http.MethodGet, base+"/admin/ping", nil)
	req2.Header.Set("Authorization", "Bearer wrong")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("GET /admin/ping wrong bearer: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong bearer: got %d want 401", resp2.StatusCode)
	}
}

func TestE2E_WithCookieAuth_False_SmokeTest(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	sess, err := store.Create(time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mw := admin.Middleware(testAdminToken, store, admin.WithCookieAuth(false))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Cookie only → 401.
	req1 := httptest.NewRequest(http.MethodGet, "/secret", nil)
	req1.AddCookie(&http.Cookie{Name: "httpcatch_session", Value: sess.ID})
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Errorf("cookie-only on no-cookie-auth route: got %d want 401", rec1.Code)
	}

	// Valid bearer → 200.
	req2 := httptest.NewRequest(http.MethodGet, "/secret", nil)
	req2.Header.Set("Authorization", "Bearer "+testAdminToken)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("valid bearer on no-cookie-auth route: got %d want 200", rec2.Code)
	}
}
