package admin_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// startServer boots a real admin server on an ephemeral port and returns its
// base URL. The server is shut down when the test context is cancelled.
func startServer(t *testing.T, token string) string {
	t.Helper()
	return startServerWithReaders(t, token, admin.ReadSources{})
}

// startServerWithReaders boots an admin server backed by the given read sources.
func startServerWithReaders(t *testing.T, token string, readers admin.ReadSources) string {
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
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{}, admin.ServerOptions{Readers: readers})
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
	req1, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	req1.Header.Set("Accept", "text/html")
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("step1 GET /status: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusSeeOther {
		t.Errorf("step1: got %d want 303", resp1.StatusCode)
	}
	loc1 := resp1.Header.Get("Location")
	if !strings.Contains(loc1, "/login?next=") {
		t.Errorf("step1: Location %q does not contain /login?next=", loc1)
	}
	if !strings.Contains(loc1, url.QueryEscape("/status")) {
		t.Errorf("step1: Location %q does not contain URL-encoded /status", loc1)
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

	// Step 3: GET /status with cookie → 200 JSON.
	req3, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	req3.AddCookie(sessionCookie)
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("step3 GET /status: %v", err)
	}
	io.Copy(io.Discard, resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("step3: got %d want 200", resp3.StatusCode)
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

	// Step 5: GET /status with revoked cookie → 303 to login (html accept).
	req5, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	req5.Header.Set("Accept", "text/html")
	req5.AddCookie(sessionCookie)
	resp5, err := client.Do(req5)
	if err != nil {
		t.Fatalf("step5 GET /status revoked cookie: %v", err)
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
	req1, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	req1.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("GET /status valid bearer: %v", err)
	}
	io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("valid bearer: got %d want 200", resp1.StatusCode)
	}

	// Wrong bearer → 401.
	req2, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	req2.Header.Set("Authorization", "Bearer wrong")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("GET /status wrong bearer: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong bearer: got %d want 401", resp2.StatusCode)
	}
}

// TestE2E_Requests_QParameter seeds a sink with three captured requests and
// verifies GET /requests?q=service:orders method:POST returns only the row
// matching both terms. Field-qualified queries route to the SQLite reader.
func TestE2E_Requests_QParameter(t *testing.T) {
	t.Parallel()

	sq, err := sinks.NewSQLiteSink(t.TempDir() + "/e2e.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sq.Close() })

	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	fixtures := []*capture.CapturedRequest{
		{
			ID: "r1", Timestamp: base, Service: "orders", Method: "POST",
			Path: "/api/orders/1", CorrelationID: "c1", SourceIP: "x",
			Headers: map[string][]string{"Host": {"h"}}, Query: map[string][]string{},
			Cookies: []capture.Cookie{}, Body: []byte{},
			ServiceSource: capture.ServiceSourceHeader, CorrelationSource: capture.CorrelationSourceTraceparent,
		},
		{
			ID: "r2", Timestamp: base.Add(time.Second), Service: "orders", Method: "GET",
			Path: "/api/orders/2", CorrelationID: "c2", SourceIP: "x",
			Headers: map[string][]string{"Host": {"h"}}, Query: map[string][]string{},
			Cookies: []capture.Cookie{}, Body: []byte{},
			ServiceSource: capture.ServiceSourceHeader, CorrelationSource: capture.CorrelationSourceTraceparent,
		},
		{
			ID: "r3", Timestamp: base.Add(2 * time.Second), Service: "payments", Method: "POST",
			Path: "/api/payments", CorrelationID: "c3", SourceIP: "x",
			Headers: map[string][]string{"Host": {"h"}}, Query: map[string][]string{},
			Cookies: []capture.Cookie{}, Body: []byte{},
			ServiceSource: capture.ServiceSourceHeader, CorrelationSource: capture.CorrelationSourceTraceparent,
		},
	}
	for _, r := range fixtures {
		if err := sq.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}

	srvURL := startServerWithReaders(t, testAdminToken, admin.ReadSources{SQLite: sq})
	client := &http.Client{}

	q := url.QueryEscape("service:orders method:POST")
	req, _ := http.NewRequest(http.MethodGet, srvURL+"/requests?q="+q, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /requests: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if src := resp.Header.Get("X-Httpcatch-Read-Source"); src != "sqlite" {
		t.Errorf("X-Httpcatch-Read-Source: got %q want sqlite", src)
	}

	var body struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(body.Records))
	}
	if id, _ := body.Records[0]["id"].(string); id != "r1" {
		t.Errorf("got id %q want r1", id)
	}
}

// TestE2E_UI_MultiFeatureQuery_ChipsBannerRowsConsistent boots a real admin
// server, seeds a captured request and a non-matching one, then submits a
// multi-feature search-language query. The rendered HTML must include one
// chip per parsed term, the amber scan banner (the query has a leading-
// wildcard substring on a freeform position), and exactly the matching row.
func TestE2E_UI_MultiFeatureQuery_ChipsBannerRowsConsistent(t *testing.T) {
	t.Parallel()

	sq, err := sinks.NewSQLiteSink(t.TempDir() + "/e2e-multifeature.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sq.Close() })

	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	match := &capture.CapturedRequest{
		ID: "match", Timestamp: base, Service: "foo", Method: "GET",
		Path: "/billing-api/charge", CorrelationID: "c-match", SourceIP: "x",
		Headers: map[string][]string{"User-Agent": {"client/0.3"}},
		Query:   map[string][]string{}, Cookies: []capture.Cookie{},
		Body:          []byte{},
		ServiceSource: capture.ServiceSourceHeader, CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	skipHealth := &capture.CapturedRequest{
		ID: "health", Timestamp: base.Add(time.Second), Service: "foo", Method: "GET",
		Path: "/health", CorrelationID: "c-health", SourceIP: "x",
		Headers: map[string][]string{"User-Agent": {"client/0.3"}},
		Query:   map[string][]string{}, Cookies: []capture.Cookie{},
		Body:          []byte{},
		ServiceSource: capture.ServiceSourceHeader, CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	for _, r := range []*capture.CapturedRequest{match, skipHealth} {
		if err := sq.Write(ctx, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}

	srvURL := startServerWithReaders(t, testAdminToken, admin.ReadSources{SQLite: sq})

	q := "service:foo *billing* -path:/health header.user-agent:client"
	req, _ := http.NewRequest(http.MethodGet, srvURL+"/ui/requests?q="+url.QueryEscape(q), nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	s := string(bodyBytes)

	for _, want := range []string{
		`data-token="service:foo"`,
		`data-token="*billing*"`,
		`data-token="-path:/health"`,
		`data-token="header.User-Agent:client"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("chip strip missing %q", want)
		}
	}

	if !strings.Contains(s, `id="scan-banner"`) {
		t.Fatal("scan-banner element missing")
	}
	if strings.Contains(s, `id="scan-banner" class="scan-banner" role="status" hidden`) {
		t.Error("scan-banner must be visible — query contains a leading-wildcard substring on a freeform term")
	}

	if !strings.Contains(s, ">match<") && !strings.Contains(s, `data-id="match"`) {
		t.Error("expected matching row id=match in rendered table")
	}
	if strings.Contains(s, `data-id="health"`) {
		t.Error("non-matching row id=health must be filtered out")
	}
}

// TestE2E_UI_ScanBanner_RendersForLeadingWildcardQuery boots a real admin
// server and verifies that the amber unindexed-scan banner appears in the
// rendered HTML when q has a leading wildcard on an indexed field, and is
// absent (hidden) when the query is index-friendly.
func TestE2E_UI_ScanBanner_RendersForLeadingWildcardQuery(t *testing.T) {
	t.Parallel()

	srvURL := startServer(t, testAdminToken)

	fetch := func(q string) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srvURL+"/ui/requests?q="+url.QueryEscape(q), nil)
		req.Header.Set("Authorization", "Bearer "+testAdminToken)
		req.Header.Set("Accept", "text/html")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /ui/requests?q=%s: %v", q, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	qualifying := fetch("host:*api*")
	if !strings.Contains(qualifying, `id="scan-banner"`) {
		t.Fatal("e2e: scan-banner element missing from qualifying-query page")
	}
	if !strings.Contains(qualifying, "Unindexed scan") {
		t.Error("e2e: scan-banner headline missing from qualifying-query page")
	}
	// The banner must be visible (no `hidden` attribute on the banner div).
	if strings.Contains(qualifying, `id="scan-banner" class="scan-banner" role="status" hidden`) {
		t.Error("e2e: scan-banner should be visible for qualifying query")
	}

	nonQualifying := fetch("host:billing-api")
	if !strings.Contains(nonQualifying, `id="scan-banner"`) {
		t.Fatal("e2e: scan-banner element missing from non-qualifying-query page")
	}
	if !strings.Contains(nonQualifying, `id="scan-banner" class="scan-banner" role="status" hidden`) {
		t.Error("e2e: scan-banner must be hidden for non-qualifying query")
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
