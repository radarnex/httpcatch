package admin_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/buildinfo"
	"github.com/radarnex/httpcatch/internal/config"
)

// fakeSourcesWith builds a MetricSources from explicit counter values and the
// unredacted signal so individual tests can inject precise state.
func fakeSourcesWith(dropped, correlation, service, redaction uint64, unredacted bool) admin.MetricSources {
	return admin.MetricSources{
		DroppedTotal:                    func() uint64 { return dropped },
		CapturedWithoutCorrelationTotal: func() uint64 { return correlation },
		CapturedWithoutServiceTotal:     func() uint64 { return service },
		RedactionErrorsTotal:            func() uint64 { return redaction },
		Unredacted:                      func() bool { return unredacted },
	}
}

func newStatusServer(t *testing.T, src admin.MetricSources) *httptest.Server {
	t.Helper()
	cfg := config.AdminConfig{
		Bind:          "127.0.0.1:0",
		Token:         testAdminToken,
		SessionTTL:    time.Hour,
		SessionSecure: false,
	}
	srv, err := admin.New(cfg, discardLogger(), src)
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

// decodeStatus GETs /status with a bearer token and decodes the response into
// a typed struct. It fatals on network or decode errors so callers can focus on
// assertion logic.
func decodeStatus(t *testing.T, ts *httptest.Server) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /status: got %d want 200", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	return m
}

// TestStatus_BearerAuth_200_Shape uses httptest.NewRecorder to avoid spawning
// live server goroutines while mutating package-level buildinfo vars.
func TestStatus_BearerAuth_200_Shape(t *testing.T) {
	orig, origBT := buildinfo.Version, buildinfo.BuildTime
	buildinfo.Version = "v0.1.0"
	buildinfo.BuildTime = "2026-05-18T12:00:00Z"
	t.Cleanup(func() { buildinfo.Version = orig; buildinfo.BuildTime = origBT })

	src := fakeSourcesWith(7, 13, 19, 23, true)
	cfg := config.AdminConfig{
		Bind:       "127.0.0.1:0",
		Token:      testAdminToken,
		SessionTTL: time.Hour,
	}
	srv, err := admin.New(cfg, discardLogger(), src)
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}

	var m map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if m["unredacted"] != true {
		t.Errorf("unredacted: got %v want true", m["unredacted"])
	}
	counters, ok := m["counters"].(map[string]any)
	if !ok {
		t.Fatalf("counters: expected object, got %T", m["counters"])
	}
	wantCounters := map[string]float64{
		"dropped_total":                      7,
		"captured_without_correlation_total": 13,
		"captured_without_service_total":     19,
		"redaction_errors_total":             23,
	}
	for k, want := range wantCounters {
		got, ok := counters[k].(float64)
		if !ok {
			t.Errorf("counters.%s: not a number (%T)", k, counters[k])
			continue
		}
		if got != want {
			t.Errorf("counters.%s: got %v want %v", k, got, want)
		}
	}
	if m["version"] != "v0.1.0" {
		t.Errorf("version: got %v want v0.1.0", m["version"])
	}
	if m["build_time"] != "2026-05-18T12:00:00Z" {
		t.Errorf("build_time: got %v want 2026-05-18T12:00:00Z", m["build_time"])
	}
}

func TestStatus_CookieAuth_200(t *testing.T) {
	t.Parallel()

	ts := newStatusServer(t, fakeSourcesWith(0, 0, 0, 0, false))
	client := noFollowClient(t)

	// Log in to get a session cookie.
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

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.AddCookie(sessionCookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /status with cookie: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("cookie auth: got %d want 200", resp.StatusCode)
	}
}

func TestStatus_NoAuth_APIClient_401(t *testing.T) {
	t.Parallel()

	ts := newStatusServer(t, fakeSourcesWith(0, 0, 0, 0, false))
	c := testClient(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET /status no auth: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth API: got %d want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") != `Bearer realm="httpcatch"` {
		t.Errorf("WWW-Authenticate: got %q", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestStatus_NoAuth_HTMLClient_303(t *testing.T) {
	t.Parallel()

	ts := newStatusServer(t, fakeSourcesWith(0, 0, 0, 0, false))
	client := noFollowClient(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /status no auth HTML: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("no auth HTML: got %d want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/login?next=%2Fstatus" {
		t.Errorf("Location: got %q want /login?next=%%2Fstatus", loc)
	}
}

func TestStatus_AllFieldsPresentWhenZero(t *testing.T) {
	t.Parallel()

	ts := newStatusServer(t, fakeSourcesWith(0, 0, 0, 0, false))
	m := decodeStatus(t, ts)

	requiredTopLevel := []string{"unredacted", "counters", "version", "build_time"}
	for _, k := range requiredTopLevel {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}

	counters, ok := m["counters"].(map[string]any)
	if !ok {
		t.Fatalf("counters: expected object, got %T", m["counters"])
	}
	requiredCounters := []string{
		"dropped_total",
		"captured_without_correlation_total",
		"captured_without_service_total",
		"redaction_errors_total",
	}
	for _, k := range requiredCounters {
		if _, ok := counters[k]; !ok {
			t.Errorf("missing counters key %q", k)
		}
	}

	// Decode into typed struct to confirm types.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := testClient(t).Do(req)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	type statusResponse struct {
		Unredacted bool `json:"unredacted"`
		Counters   struct {
			DroppedTotal                    uint64 `json:"dropped_total"`
			CapturedWithoutCorrelationTotal uint64 `json:"captured_without_correlation_total"`
			CapturedWithoutServiceTotal     uint64 `json:"captured_without_service_total"`
			RedactionErrorsTotal            uint64 `json:"redaction_errors_total"`
		} `json:"counters"`
		Version   string `json:"version"`
		BuildTime string `json:"build_time"`
	}
	var sr statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	if sr.Unredacted != false {
		t.Error("typed: Unredacted should be false")
	}
}

func TestStatus_ContentTypeAndCacheControl(t *testing.T) {
	t.Parallel()

	ts := newStatusServer(t, fakeSourcesWith(0, 0, 0, 0, false))
	c := testClient(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: got %q want application/json; charset=utf-8", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q want no-store", cc)
	}
}

func TestStatus_ReflectsAccessors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		src        admin.MetricSources
		assertFunc func(t *testing.T, m map[string]any)
	}{
		{
			name: "dropped",
			src:  fakeSourcesWith(42, 0, 0, 0, false),
			assertFunc: func(t *testing.T, m map[string]any) {
				t.Helper()
				c := m["counters"].(map[string]any)
				if c["dropped_total"].(float64) != 42 {
					t.Errorf("dropped_total: got %v want 42", c["dropped_total"])
				}
			},
		},
		{
			name: "correlation",
			src:  fakeSourcesWith(0, 11, 0, 0, false),
			assertFunc: func(t *testing.T, m map[string]any) {
				t.Helper()
				c := m["counters"].(map[string]any)
				if c["captured_without_correlation_total"].(float64) != 11 {
					t.Errorf("captured_without_correlation_total: got %v want 11", c["captured_without_correlation_total"])
				}
			},
		},
		{
			name: "service",
			src:  fakeSourcesWith(0, 0, 5, 0, false),
			assertFunc: func(t *testing.T, m map[string]any) {
				t.Helper()
				c := m["counters"].(map[string]any)
				if c["captured_without_service_total"].(float64) != 5 {
					t.Errorf("captured_without_service_total: got %v want 5", c["captured_without_service_total"])
				}
			},
		},
		{
			name: "redaction_errors",
			src:  fakeSourcesWith(0, 0, 0, 99, false),
			assertFunc: func(t *testing.T, m map[string]any) {
				t.Helper()
				c := m["counters"].(map[string]any)
				if c["redaction_errors_total"].(float64) != 99 {
					t.Errorf("redaction_errors_total: got %v want 99", c["redaction_errors_total"])
				}
			},
		},
		{
			name: "unredacted_true",
			src:  fakeSourcesWith(0, 0, 0, 0, true),
			assertFunc: func(t *testing.T, m map[string]any) {
				t.Helper()
				if m["unredacted"] != true {
					t.Errorf("unredacted: got %v want true", m["unredacted"])
				}
			},
		},
		{
			name: "unredacted_false",
			src:  fakeSourcesWith(0, 0, 0, 0, false),
			assertFunc: func(t *testing.T, m map[string]any) {
				t.Helper()
				if m["unredacted"] != false {
					t.Errorf("unredacted: got %v want false", m["unredacted"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := newStatusServer(t, tc.src)
			m := decodeStatus(t, ts)
			tc.assertFunc(t, m)
		})
	}
}

func TestAdminPing_Removed(t *testing.T) {
	t.Parallel()

	ts := newStatusServer(t, fakeSourcesWith(0, 0, 0, 0, false))
	c := testClient(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET /admin/ping: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /admin/ping: got %d want 404 (route was removed)", resp.StatusCode)
	}
}
