package admin_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/buildinfo"
	"github.com/radarnex/httpcatch/internal/config"
)

func fakeSources(dropped, correlation, service, redaction uint64) admin.MetricSources {
	return admin.MetricSources{
		DroppedTotal:                    func() uint64 { return dropped },
		CapturedWithoutCorrelationTotal: func() uint64 { return correlation },
		CapturedWithoutServiceTotal:     func() uint64 { return service },
		RedactionErrorsTotal:            func() uint64 { return redaction },
	}
}

func getMetrics(t *testing.T, src admin.MetricSources) *httptest.ResponseRecorder {
	t.Helper()
	srv, err := admin.New(config.AdminConfig{
		Bind:       "127.0.0.1:0",
		SessionTTL: time.Hour,
	}, discardLogger(), src)
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func TestMetrics_StatusAndContentType(t *testing.T) {
	t.Parallel()

	rec := getMetrics(t, fakeSources(0, 0, 0, 0))

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type: got %q want %q", ct, "text/plain; version=0.0.4; charset=utf-8")
	}
}

func TestMetrics_AllMetricNamesPresent(t *testing.T) {
	t.Parallel()

	rec := getMetrics(t, fakeSources(0, 0, 0, 0))
	body := rec.Body.String()

	checks := []string{
		"httpcatch_dropped_total",
		"httpcatch_captured_without_correlation_total",
		"httpcatch_captured_without_service_total",
		"httpcatch_redaction_errors_total",
		"httpcatch_build_info",
		"# HELP httpcatch_dropped_total",
		"# TYPE httpcatch_dropped_total counter",
		"# HELP httpcatch_captured_without_correlation_total",
		"# TYPE httpcatch_captured_without_correlation_total counter",
		"# HELP httpcatch_captured_without_service_total",
		"# TYPE httpcatch_captured_without_service_total counter",
		"# HELP httpcatch_redaction_errors_total",
		"# TYPE httpcatch_redaction_errors_total counter",
		"# HELP httpcatch_build_info",
		"# TYPE httpcatch_build_info gauge",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestMetrics_CounterValuesReflectAccessors(t *testing.T) {
	t.Parallel()

	rec := getMetrics(t, fakeSources(7, 13, 19, 23))
	body := rec.Body.String()

	cases := []struct {
		metric string
		value  string
	}{
		{"httpcatch_dropped_total", "httpcatch_dropped_total 7"},
		{"httpcatch_captured_without_correlation_total", "httpcatch_captured_without_correlation_total 13"},
		{"httpcatch_captured_without_service_total", "httpcatch_captured_without_service_total 19"},
		{"httpcatch_redaction_errors_total", "httpcatch_redaction_errors_total 23"},
	}
	for _, c := range cases {
		if !strings.Contains(body, c.value) {
			t.Errorf("body missing %q\nbody:\n%s", c.value, body)
		}
	}
}

func TestMetrics_BuildInfoLabels(t *testing.T) {
	orig := buildinfo.Version
	origBT := buildinfo.BuildTime
	buildinfo.Version = "v1.2.3"
	buildinfo.BuildTime = "2026-05-18T12:00:00Z"
	t.Cleanup(func() {
		buildinfo.Version = orig
		buildinfo.BuildTime = origBT
	})

	rec := getMetrics(t, fakeSources(0, 0, 0, 0))
	body := rec.Body.String()

	want := `httpcatch_build_info{version="v1.2.3",build_time="2026-05-18T12:00:00Z"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("body missing %q\nbody:\n%s", want, body)
	}
}

func TestMetrics_LabelEscaping(t *testing.T) {
	orig := buildinfo.Version
	buildinfo.Version = `quote-and-\backslash"hostile"`
	t.Cleanup(func() { buildinfo.Version = orig })

	rec := getMetrics(t, fakeSources(0, 0, 0, 0))
	body := rec.Body.String()

	want := `version="quote-and-\\backslash\"hostile\""`
	if !strings.Contains(body, want) {
		t.Errorf("body missing %q\nbody:\n%s", want, body)
	}
}

func TestMetrics_UnauthenticatedSuccess(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := config.AdminConfig{
		Bind:       addr,
		Token:      testAdminToken,
		SessionTTL: time.Hour,
	}
	srv, err := admin.New(cfg, discardLogger(), fakeSources(0, 0, 0, 0))
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}

	ctx := t.Context()
	go func() { _ = srv.Serve(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server did not start: %v", err)
	}

	// No auth header — must succeed.
	resp1, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics no auth: %v", err)
	}
	io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("no auth: got %d want 200", resp1.StatusCode)
	}

	// Invalid bearer header — must still succeed.
	req2, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
	req2.Header.Set("Authorization", "Bearer invalid-token-xyz")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET /metrics invalid bearer: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("invalid bearer: got %d want 200", resp2.StatusCode)
	}
}

func TestMetrics_OrphansGauge(t *testing.T) {
	t.Parallel()

	src := admin.MetricSources{
		OrphansResponse: func() int { return 3 },
		OrphansOutbound: func() int { return 7 },
	}
	rec := getMetrics(t, src)
	body := rec.Body.String()

	if !strings.Contains(body, "httpcatch_orphans_total{type=\"response\"} 3") {
		t.Errorf("orphans response gauge not found; body:\n%s", body)
	}
	if !strings.Contains(body, "httpcatch_orphans_total{type=\"outbound\"} 7") {
		t.Errorf("orphans outbound gauge not found; body:\n%s", body)
	}
}

func TestMetrics_OrphansGauge_NilFuncsDefaultToZero(t *testing.T) {
	t.Parallel()

	// OrphansResponse and OrphansOutbound both nil — must not panic and must emit 0.
	rec := getMetrics(t, admin.MetricSources{})
	body := rec.Body.String()

	if !strings.Contains(body, "httpcatch_orphans_total{type=\"response\"} 0") {
		t.Errorf("orphans response gauge not found with nil func; body:\n%s", body)
	}
	if !strings.Contains(body, "httpcatch_orphans_total{type=\"outbound\"} 0") {
		t.Errorf("orphans outbound gauge not found with nil func; body:\n%s", body)
	}
}
