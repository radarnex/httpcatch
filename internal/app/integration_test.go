package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/net/html"

	"github.com/radarnex/httpcatch/internal/app"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/redact"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// freeAddr returns a local address with an available port by briefly binding
// and releasing it.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// syncBuffer lets the worker write while the test polls. Lines is updated on
// Write so poll loops do not have to scan the buffer each iteration.
type syncBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines atomic.Int64
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	b.lines.Add(int64(bytes.Count(p, []byte{'\n'})))
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) CountLines() int { return int(b.lines.Load()) }

func testLogger(buf io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func runPipeline(t *testing.T, cfg config.Config, stdoutBuf io.Writer, logBuf io.Writer, extras ...sinks.Sink) (*app.App, *httptest.Server, func()) {
	t.Helper()
	a, err := app.Build(cfg, testLogger(logBuf), stdoutBuf, extras...)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	ts := httptest.NewServer(a.Handler)
	ctx, cancel := context.WithCancel(context.Background())
	a.Workers.Start(ctx)

	teardown := func() {
		ts.Close()
		a.Queue.Close()
		a.Workers.Wait()
		if a.SQLite != nil {
			_ = a.SQLite.Close()
		}
		cancel()
	}
	return a, ts, teardown
}

func fire(t *testing.T, url, method string, body []byte, hdr http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// json.Unmarshal base64-decodes []byte fields; Body is plain text in stdout output.
type stdoutRequest struct {
	capture.CapturedRequest
	Body string `json:"body"`
}

func decodeLines(s string) ([]capture.CapturedRequest, error) {
	out := []capture.CapturedRequest{}
	for line := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var r stdoutRequest
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("line %q: %w", line, err)
		}
		r.CapturedRequest.Body = []byte(r.Body)
		out = append(out, r.CapturedRequest)
	}
	return out, nil
}

// recentRequests converts the polymorphic Recent snapshot to a typed slice by
// asserting each entry is a *CapturedRequest. Tests that only enqueue
// CapturedRequest records can rely on this without a per-element branch.
func recentRequests(t *testing.T, m *sinks.MemorySink, n int) []*capture.CapturedRequest {
	t.Helper()
	recs := m.Recent(n)
	out := make([]*capture.CapturedRequest, len(recs))
	for i, r := range recs {
		cr, ok := r.(*capture.CapturedRequest)
		if !ok {
			t.Fatalf("recentRequests[%d]: got %T, want *capture.CapturedRequest", i, r)
		}
		out[i] = cr
	}
	return out
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestIntegration_EndToEnd_BodyCapShape(t *testing.T) {
	t.Parallel()

	const cap = 1024
	cfg := config.Defaults()
	cfg.QueueSize = 1024
	cfg.Workers = 4
	cfg.BodyCap = cap
	cfg.Sinks.Stdout = true

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	_, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	type fired struct {
		marker     string
		bodyLen    int
		wantOrig   int
		wantTrunc  bool
		wantCapped int
	}
	cases := make([]fired, 0, 100)
	for i := range 30 {
		cases = append(cases, fired{
			marker: fmt.Sprintf("under-%d", i), bodyLen: 100,
			wantOrig: 100, wantTrunc: false, wantCapped: 100,
		})
	}
	for i := range 30 {
		cases = append(cases, fired{
			marker: fmt.Sprintf("at-%d", i), bodyLen: cap,
			wantOrig: cap, wantTrunc: false, wantCapped: cap,
		})
	}
	for i := range 30 {
		cases = append(cases, fired{
			marker: fmt.Sprintf("over-%d", i), bodyLen: cap + 512,
			wantOrig: cap + 1, wantTrunc: true, wantCapped: cap,
		})
	}
	for i := range 10 {
		cases = append(cases, fired{
			marker: fmt.Sprintf("empty-%d", i), bodyLen: 0,
			wantOrig: 0, wantTrunc: false, wantCapped: 0,
		})
	}

	for _, c := range cases {
		body := bytes.Repeat([]byte("x"), c.bodyLen)
		hdr := http.Header{"X-Test-Marker": []string{c.marker}}
		resp := fire(t, ts.URL+"/anywhere", "POST", body, hdr)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status got %d want 202", c.marker, resp.StatusCode)
		}
	}

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= len(cases)
	}, 5*time.Second) {
		t.Fatalf("timed out waiting for %d records; got %d lines",
			len(cases), stdoutBuf.CountLines())
	}

	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != len(cases) {
		t.Fatalf("records: got %d want %d", len(records), len(cases))
	}

	byMarker := map[string]capture.CapturedRequest{}
	for _, r := range records {
		markers := r.Headers["X-Test-Marker"]
		if len(markers) != 1 {
			t.Fatalf("record %s: expected exactly one X-Test-Marker, got %v", r.ID, markers)
		}
		if _, dup := byMarker[markers[0]]; dup {
			t.Fatalf("duplicate marker %s", markers[0])
		}
		byMarker[markers[0]] = r
	}

	for _, c := range cases {
		r, ok := byMarker[c.marker]
		if !ok {
			t.Fatalf("missing record for marker %s", c.marker)
		}
		if r.Method != "POST" {
			t.Errorf("%s: method got %q want POST", c.marker, r.Method)
		}
		if r.Path != "/anywhere" {
			t.Errorf("%s: path got %q want /anywhere", c.marker, r.Path)
		}
		if r.BodyOriginalSize != c.wantOrig {
			t.Errorf("%s: body_original_size got %d want %d", c.marker, r.BodyOriginalSize, c.wantOrig)
		}
		if r.BodyTruncated != c.wantTrunc {
			t.Errorf("%s: body_truncated got %v want %v", c.marker, r.BodyTruncated, c.wantTrunc)
		}
		if len(r.Body) != c.wantCapped {
			t.Errorf("%s: body length got %d want %d", c.marker, len(r.Body), c.wantCapped)
		}
		if r.ID == "" {
			t.Errorf("%s: id is empty", c.marker)
		}
		if r.Timestamp.IsZero() {
			t.Errorf("%s: timestamp is zero", c.marker)
		}
		// httptest sends Host=127.0.0.1:port and no service/correlation
		// headers, so derivation lands on the host fallback and a
		// synthesized correlation UUID.
		if r.ServiceSource != capture.ServiceSourceHost {
			t.Errorf("%s: service_source got %q want %q", c.marker, r.ServiceSource, capture.ServiceSourceHost)
		}
		if r.Service == "" || r.Service == capture.UnknownService {
			t.Errorf("%s: service got %q, expected non-empty host-derived value", c.marker, r.Service)
		}
		if r.CorrelationSource != capture.CorrelationSourceSynthesized {
			t.Errorf("%s: correlation_source got %q want %q", c.marker, r.CorrelationSource, capture.CorrelationSourceSynthesized)
		}
		if r.CorrelationID == "" {
			t.Errorf("%s: correlation_id is empty for synthesized record", c.marker)
		}
	}

	if !strings.Contains(logBuf.String(), "unredacted") {
		t.Errorf("expected logs to contain 'unredacted', got:\n%s", logBuf.String())
	}
}

func TestIntegration_BodyCapDisabled(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.BodyCap = 0
	cfg.Sinks.Stdout = true
	cfg.Workers = 2

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	_, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	huge := bytes.Repeat([]byte("z"), 1<<17) // 128 KiB
	resp := fire(t, ts.URL+"/big", "PUT", huge, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status got %d want 202", resp.StatusCode)
	}

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 1
	}, 5*time.Second) {
		t.Fatal("timed out waiting for record")
	}

	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records: got %d want 1", len(records))
	}
	if records[0].BodyTruncated {
		t.Error("body_truncated should be false when body_cap=0")
	}
	if records[0].BodyOriginalSize != len(huge) {
		t.Errorf("body_original_size got %d want %d", records[0].BodyOriginalSize, len(huge))
	}
	if len(records[0].Body) != len(huge) {
		t.Errorf("body length got %d want %d", len(records[0].Body), len(huge))
	}
}

type slowSink struct {
	inner sinks.Sink
	delay time.Duration
}

func (s *slowSink) Name() string { return "slow-" + s.inner.Name() }
func (s *slowSink) Write(ctx context.Context, r capture.Record) error {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.inner.Write(ctx, r)
}

func TestIntegration_DropSemantics(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.QueueSize = 1
	cfg.Workers = 1
	cfg.BodyCap = 1 << 20
	cfg.Sinks.Stdout = false

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	slow := &slowSink{inner: sinks.NewWriterSink(&stdoutBuf), delay: 30 * time.Millisecond}
	a, ts, teardown := runPipeline(t, cfg, io.Discard, &logBuf, slow)
	defer teardown()

	const total = 200
	var wg sync.WaitGroup
	var ok202 atomic.Uint64
	for range total {
		wg.Go(func() {
			resp := fire(t, ts.URL+"/drop", "POST", []byte("payload"), nil)
			if resp.StatusCode == http.StatusAccepted {
				ok202.Add(1)
			}
		})
	}
	wg.Wait()

	if got := ok202.Load(); got != total {
		t.Fatalf("expected all %d responses to be 202, got %d", total, got)
	}

	dropped := a.Queue.DroppedTotal()
	if dropped == 0 {
		t.Fatal("expected dropped_total > 0 with capacity=1, workers=1, slow sink")
	}

	wantWritten := uint64(total) - dropped
	if !waitFor(func() bool {
		return uint64(stdoutBuf.CountLines()) >= wantWritten
	}, 30*time.Second) {
		t.Fatalf("timed out: dropped=%d, want emitted=%d, got %d lines",
			dropped, wantWritten, stdoutBuf.CountLines())
	}

	emitted := uint64(stdoutBuf.CountLines())
	if dropped+emitted != total {
		t.Fatalf("dropped(%d) + emitted(%d) != total(%d)", dropped, emitted, total)
	}
}

type failingSink struct{}

func (failingSink) Name() string { return "failing" }
func (failingSink) Write(context.Context, capture.Record) error {
	return fmt.Errorf("intentional sink failure")
}

func TestIntegration_SinkIsolation(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Workers = 1

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	_, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf, failingSink{})
	defer teardown()

	resp := fire(t, ts.URL+"/iso", "POST", []byte("hello"), nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status got %d want 202", resp.StatusCode)
	}

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 1
	}, 5*time.Second) {
		t.Fatal("timed out waiting for stdout record despite failing sink")
	}

	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records: got %d want 1", len(records))
	}
	if records[0].Path != "/iso" {
		t.Errorf("path got %q want /iso", records[0].Path)
	}

	resp = fire(t, ts.URL+"/iso2", "GET", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("second request: status got %d want 202", resp.StatusCode)
	}
	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 2
	}, 5*time.Second) {
		t.Fatal("worker appears to have stopped processing after sink error")
	}
}

func TestIntegration_AnyMethodAnyPath_Returns202(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	_, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	cases := []struct {
		method, path string
	}{
		{"GET", "/"},
		{"PUT", "/api/x/y"},
		{"DELETE", "/healthz"},
		{"PATCH", "/metrics"},
		{"OPTIONS", "/requests/123"},
		{"HEAD", "/events"},
	}
	for _, c := range cases {
		resp := fire(t, ts.URL+c.path, c.method, nil, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("%s %s: got %d want 202", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestIntegration_StartupWarnings_ZeroSinks(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	// No sinks enabled.

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	out := logBuf.String()
	if !strings.Contains(out, "unredacted") {
		t.Errorf("expected unredacted-mode warning, got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "zero sinks") {
		t.Errorf("expected zero-sinks warning, got:\n%s", out)
	}
	if len(a.Sinks) != 0 {
		t.Errorf("expected zero sinks, got %d", len(a.Sinks))
	}
}

func TestIntegration_IdentifiersAndCounters(t *testing.T) {
	t.Parallel()

	const traceID = "0af7651916cd43dd8448eb211c80319c"

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Workers = 2
	cfg.QueueSize = 64

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	type fired struct {
		marker            string
		headers           http.Header
		wantService       string
		wantServiceSource string
		wantCorrID        string
		wantCorrSource    string
	}
	cases := []fired{
		{
			marker:            "default-header",
			headers:           http.Header{capture.DefaultServiceHeader: []string{"orders"}},
			wantService:       "orders",
			wantServiceSource: capture.ServiceSourceHeader,
			wantCorrSource:    capture.CorrelationSourceSynthesized,
		},
		{
			marker:            "host-fallback",
			headers:           http.Header{},
			wantServiceSource: capture.ServiceSourceHost,
			wantCorrSource:    capture.CorrelationSourceSynthesized,
		},
		{
			marker:            "traceparent-wins",
			headers:           http.Header{capture.TraceparentHeader: []string{"00-" + traceID + "-b7ad6b7169203331-01"}, capture.RequestIDHeader: []string{"req-loser"}},
			wantServiceSource: capture.ServiceSourceHost,
			wantCorrID:        traceID,
			wantCorrSource:    capture.CorrelationSourceTraceparent,
		},
		{
			marker:            "request-id-only",
			headers:           http.Header{capture.RequestIDHeader: []string{"req-xyz"}},
			wantServiceSource: capture.ServiceSourceHost,
			wantCorrID:        "req-xyz",
			wantCorrSource:    capture.CorrelationSourceRequestID,
		},
		{
			marker:            "x-correlation-id-ignored",
			headers:           http.Header{"X-Correlation-ID": []string{"should-be-ignored"}},
			wantServiceSource: capture.ServiceSourceHost,
			wantCorrSource:    capture.CorrelationSourceSynthesized,
		},
	}

	for _, c := range cases {
		hdr := c.headers.Clone()
		hdr.Set("X-Test-Marker", c.marker)
		resp := fire(t, ts.URL+"/x", "POST", []byte("payload"), hdr)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status got %d want 202", c.marker, resp.StatusCode)
		}
	}

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= len(cases)
	}, 5*time.Second) {
		t.Fatalf("timed out waiting for %d records; got %d lines", len(cases), stdoutBuf.CountLines())
	}

	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	byMarker := map[string]capture.CapturedRequest{}
	for _, r := range records {
		ms := r.Headers["X-Test-Marker"]
		if len(ms) != 1 {
			t.Fatalf("record %s: expected 1 marker, got %v", r.ID, ms)
		}
		byMarker[ms[0]] = r
	}

	for _, c := range cases {
		r, ok := byMarker[c.marker]
		if !ok {
			t.Fatalf("missing record for marker %s", c.marker)
		}
		if c.wantService != "" && r.Service != c.wantService {
			t.Errorf("%s: service got %q want %q", c.marker, r.Service, c.wantService)
		}
		if r.ServiceSource != c.wantServiceSource {
			t.Errorf("%s: service_source got %q want %q", c.marker, r.ServiceSource, c.wantServiceSource)
		}
		if c.wantServiceSource == capture.ServiceSourceHost && r.Service == "" {
			t.Errorf("%s: host-derived service should be non-empty", c.marker)
		}
		if c.wantCorrID != "" && r.CorrelationID != c.wantCorrID {
			t.Errorf("%s: correlation_id got %q want %q", c.marker, r.CorrelationID, c.wantCorrID)
		}
		if r.CorrelationSource != c.wantCorrSource {
			t.Errorf("%s: correlation_source got %q want %q", c.marker, r.CorrelationSource, c.wantCorrSource)
		}
	}

	// All five cases sent a Host header (httptest sets it), so the service
	// fallback never bottoms out at "unknown" — no without-service hits.
	if got := a.Counters.CapturedWithoutServiceTotal(); got != 0 {
		t.Errorf("captured_without_service_total: got %d want 0", got)
	}

	// Three of five synthesized a correlation id: host-fallback,
	// x-correlation-id-ignored, default-header (which sets no correlation).
	const wantSynth = 3
	if got := a.Counters.CapturedWithoutCorrelationTotal(); got != wantSynth {
		t.Errorf("captured_without_correlation_total: got %d want %d", got, wantSynth)
	}
}

func TestIntegration_CapturedWithoutServiceCounter(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Workers = 1

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	// Go's http.Client always populates the Host header. Drop down to raw
	// TCP and send an HTTP/1.0 request without a Host header so the service
	// fallback chain actually bottoms out at "unknown".
	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reqBytes := "POST /u HTTP/1.0\r\nContent-Length: 2\r\n\r\nhi"
	if _, err := conn.Write([]byte(reqBytes)); err != nil {
		t.Fatalf("write: %v", err)
	}
	respBuf := make([]byte, 512)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Read(respBuf)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(respBuf[:n]), "202 Accepted") {
		t.Fatalf("response did not include 202 Accepted, got: %q", string(respBuf[:n]))
	}

	if !waitFor(func() bool { return stdoutBuf.CountLines() >= 1 }, 5*time.Second) {
		t.Fatal("timed out waiting for record")
	}

	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records: got %d want 1", len(records))
	}
	if records[0].ServiceSource != capture.ServiceSourceUnknown {
		t.Errorf("service_source: got %q want %q", records[0].ServiceSource, capture.ServiceSourceUnknown)
	}
	if records[0].Service != capture.UnknownService {
		t.Errorf("service: got %q want %q", records[0].Service, capture.UnknownService)
	}
	if got := a.Counters.CapturedWithoutServiceTotal(); got != 1 {
		t.Errorf("captured_without_service_total: got %d want 1", got)
	}
}

func TestIntegration_CustomServiceHeader(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Workers = 1
	cfg.ServiceHeader = "X-Service-Tag"

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	_, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	hdr := http.Header{
		"X-Service-Tag":              []string{"billing"},
		capture.DefaultServiceHeader: []string{"ignored"},
	}
	resp := fire(t, ts.URL+"/c", "POST", []byte("x"), hdr)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	if !waitFor(func() bool { return stdoutBuf.CountLines() >= 1 }, 5*time.Second) {
		t.Fatal("timed out waiting for record")
	}
	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records: got %d want 1", len(records))
	}
	if records[0].Service != "billing" {
		t.Errorf("service: got %q want %q", records[0].Service, "billing")
	}
	if records[0].ServiceSource != capture.ServiceSourceHeader {
		t.Errorf("service_source: got %q want %q", records[0].ServiceSource, capture.ServiceSourceHeader)
	}
}

func TestIntegration_MemoryAndStdout_FanOutAndEviction(t *testing.T) {
	t.Parallel()

	const (
		capacity = 10
		fired    = 50
	)
	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.QueueSize = 256
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = capacity

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	if a.Memory == nil {
		t.Fatal("expected app.Memory to be set when sinks.memory is enabled")
	}
	if a.Memory.Capacity() != capacity {
		t.Fatalf("memory capacity: got %d want %d", a.Memory.Capacity(), capacity)
	}

	markers := make([]string, fired)
	for i := range fired {
		markers[i] = fmt.Sprintf("rec-%02d", i)
		hdr := http.Header{"X-Test-Marker": []string{markers[i]}}
		resp := fire(t, ts.URL+"/m", "POST", []byte(markers[i]), hdr)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status got %d want 202", markers[i], resp.StatusCode)
		}
	}

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= fired && a.Memory.Len() >= capacity
	}, 10*time.Second) {
		t.Fatalf("timed out: stdout=%d (want %d), memory=%d (want %d)",
			stdoutBuf.CountLines(), fired, a.Memory.Len(), capacity)
	}

	stdoutRecords, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if len(stdoutRecords) != fired {
		t.Fatalf("stdout records: got %d want %d", len(stdoutRecords), fired)
	}

	memRecords := recentRequests(t, a.Memory, fired)
	if len(memRecords) != capacity {
		t.Fatalf("memory records: got %d want %d", len(memRecords), capacity)
	}

	for i, r := range memRecords {
		wantMarker := markers[fired-1-i]
		got := r.Headers["X-Test-Marker"]
		if len(got) != 1 || got[0] != wantMarker {
			t.Errorf("memory[%d]: marker got %v want %q", i, got, wantMarker)
		}
	}

	stdoutByID := make(map[string]capture.CapturedRequest, len(stdoutRecords))
	for _, r := range stdoutRecords {
		stdoutByID[r.ID] = r
	}
	for i, mem := range memRecords {
		std, ok := stdoutByID[mem.ID]
		if !ok {
			t.Fatalf("memory[%d] id %q not present in stdout", i, mem.ID)
		}
		if std.Method != mem.Method {
			t.Errorf("id %s: method stdout=%q memory=%q", mem.ID, std.Method, mem.Method)
		}
		if std.Path != mem.Path {
			t.Errorf("id %s: path stdout=%q memory=%q", mem.ID, std.Path, mem.Path)
		}
		if std.Service != mem.Service {
			t.Errorf("id %s: service stdout=%q memory=%q", mem.ID, std.Service, mem.Service)
		}
		if std.ServiceSource != mem.ServiceSource {
			t.Errorf("id %s: service_source stdout=%q memory=%q", mem.ID, std.ServiceSource, mem.ServiceSource)
		}
		if std.CorrelationID != mem.CorrelationID {
			t.Errorf("id %s: correlation_id stdout=%q memory=%q", mem.ID, std.CorrelationID, mem.CorrelationID)
		}
		if std.CorrelationSource != mem.CorrelationSource {
			t.Errorf("id %s: correlation_source stdout=%q memory=%q", mem.ID, std.CorrelationSource, mem.CorrelationSource)
		}
		if std.BodyOriginalSize != mem.BodyOriginalSize {
			t.Errorf("id %s: body_original_size stdout=%d memory=%d", mem.ID, std.BodyOriginalSize, mem.BodyOriginalSize)
		}
		if std.BodyTruncated != mem.BodyTruncated {
			t.Errorf("id %s: body_truncated stdout=%v memory=%v", mem.ID, std.BodyTruncated, mem.BodyTruncated)
		}
		if !bytes.Equal(std.Body, mem.Body) {
			t.Errorf("id %s: body bytes diverge (stdout=%q memory=%q)", mem.ID, std.Body, mem.Body)
		}
		if !std.Timestamp.Equal(mem.Timestamp) {
			t.Errorf("id %s: timestamp stdout=%v memory=%v", mem.ID, std.Timestamp, mem.Timestamp)
		}
	}

	if dropped := a.Queue.DroppedTotal(); dropped != 0 {
		t.Errorf("dropped_total: got %d want 0 (queue=%d, fired=%d)",
			dropped, cfg.QueueSize, fired)
	}
}

func TestIntegration_MemoryStdoutAndFailingSink_OneShotIsolation(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 10

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf, failingSink{})
	defer teardown()

	if a.Memory == nil {
		t.Fatal("expected app.Memory to be set when sinks.memory is enabled")
	}

	resp := fire(t, ts.URL+"/three-sinks", "POST", []byte("payload"), http.Header{
		"X-Test-Marker": []string{"triple"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 1 && a.Memory.Len() >= 1
	}, 5*time.Second) {
		t.Fatalf("timed out: stdout=%d memory=%d", stdoutBuf.CountLines(), a.Memory.Len())
	}

	stdoutRecords, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(stdoutRecords) != 1 {
		t.Fatalf("stdout: got %d records want 1", len(stdoutRecords))
	}
	memRecords := recentRequests(t, a.Memory, 1)
	if len(memRecords) != 1 {
		t.Fatalf("memory: got %d records want 1", len(memRecords))
	}
	if stdoutRecords[0].ID != memRecords[0].ID {
		t.Errorf("id mismatch: stdout=%q memory=%q", stdoutRecords[0].ID, memRecords[0].ID)
	}
	if stdoutRecords[0].Path != "/three-sinks" || memRecords[0].Path != "/three-sinks" {
		t.Errorf("path: stdout=%q memory=%q want /three-sinks",
			stdoutRecords[0].Path, memRecords[0].Path)
	}
}

func TestIntegration_StartupWarnings_UnredactedAlwaysFires(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	if !strings.Contains(logBuf.String(), "unredacted") {
		t.Errorf("expected unredacted warning even with a sink enabled, got:\n%s", logBuf.String())
	}
	if strings.Contains(strings.ToLower(logBuf.String()), "zero sinks") {
		t.Errorf("zero-sinks warning should not fire when stdout is enabled")
	}
}

type sqliteRow struct {
	id                                               string
	timestamp                                        int64
	service, serviceSource, host, corrID, corrSource string
	method, path, sourceIP, contentType              string
	queryJSON, headersJSON, cookiesJSON              string
	body                                             []byte
	truncated, origSize                              int
}

func sqliteCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM captured_requests").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func readAllSQLite(t *testing.T, db *sql.DB) []sqliteRow {
	t.Helper()
	rows, err := db.Query(`SELECT id, timestamp, service, service_source, host,
        correlation_id, correlation_source, method, path, source_ip,
        content_type, query, headers, cookies, body, body_truncated,
        body_original_size FROM captured_requests`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []sqliteRow
	for rows.Next() {
		var r sqliteRow
		if err := rows.Scan(&r.id, &r.timestamp, &r.service, &r.serviceSource,
			&r.host, &r.corrID, &r.corrSource, &r.method, &r.path,
			&r.sourceIP, &r.contentType, &r.queryJSON, &r.headersJSON,
			&r.cookiesJSON, &r.body, &r.truncated, &r.origSize); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestIntegration_AllThreeSinks_Consistent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fan-out.db")

	cfg := config.Defaults()
	cfg.Workers = 2
	cfg.QueueSize = 256
	cfg.BodyCap = 256
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 100
	cfg.Sinks.SQLite = true
	cfg.Sinks.SQLitePath = dbPath

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	if a.SQLite == nil {
		t.Fatal("expected app.SQLite to be set when sinks.sqlite is enabled")
	}

	type fired struct {
		marker    string
		method    string
		path      string
		bodyLen   int
		wantTrunc bool
		hdr       http.Header
	}
	cases := []fired{
		{
			marker: "full-headers", method: "POST", path: "/api/orders", bodyLen: 50,
			hdr: http.Header{
				capture.DefaultServiceHeader: []string{"orders"},
				capture.RequestIDHeader:      []string{"req-1"},
			},
		},
		{
			marker: "trace-only", method: "PUT", path: "/widgets/42", bodyLen: 80,
			hdr: http.Header{
				capture.TraceparentHeader: []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
			},
		},
		{
			marker: "no-corr", method: "DELETE", path: "/cleanup", bodyLen: 10,
			hdr: http.Header{},
		},
		{
			marker: "body-over-cap", method: "POST", path: "/upload", bodyLen: 512, wantTrunc: true,
			hdr: http.Header{},
		},
		{
			marker: "x-correlation-ignored", method: "PATCH", path: "/p", bodyLen: 5,
			hdr: http.Header{"X-Correlation-ID": []string{"ignored"}},
		},
	}
	for _, c := range cases {
		hdr := c.hdr.Clone()
		hdr.Set("X-Test-Marker", c.marker)
		body := bytes.Repeat([]byte("x"), c.bodyLen)
		resp := fire(t, ts.URL+c.path, c.method, body, hdr)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status %d want 202", c.marker, resp.StatusCode)
		}
	}

	sqliteDB := a.SQLite.DB()
	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= len(cases) &&
			a.Memory.Len() >= len(cases) &&
			sqliteCount(t, sqliteDB) >= len(cases)
	}, 10*time.Second) {
		t.Fatalf("timed out: stdout=%d memory=%d sqlite=%d (want >= %d)",
			stdoutBuf.CountLines(), a.Memory.Len(), sqliteCount(t, sqliteDB), len(cases))
	}
	sqliteRows := readAllSQLite(t, sqliteDB)

	stdoutRecs, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	memRecs := recentRequests(t, a.Memory, len(cases))

	stdoutByMarker := map[string]capture.CapturedRequest{}
	for _, r := range stdoutRecs {
		m := r.Headers["X-Test-Marker"]
		if len(m) == 1 {
			stdoutByMarker[m[0]] = r
		}
	}
	memByMarker := map[string]capture.CapturedRequest{}
	for _, r := range memRecs {
		m := r.Headers["X-Test-Marker"]
		if len(m) == 1 {
			memByMarker[m[0]] = *r
		}
	}
	sqliteByID := map[string]sqliteRow{}
	for _, r := range sqliteRows {
		sqliteByID[r.id] = r
	}

	for _, c := range cases {
		std, ok := stdoutByMarker[c.marker]
		if !ok {
			t.Fatalf("%s: missing from stdout", c.marker)
		}
		mem, ok := memByMarker[c.marker]
		if !ok {
			t.Fatalf("%s: missing from memory", c.marker)
		}
		sq, ok := sqliteByID[std.ID]
		if !ok {
			t.Fatalf("%s: id %q missing from sqlite", c.marker, std.ID)
		}

		if std.ID != mem.ID {
			t.Errorf("%s: id stdout=%q memory=%q", c.marker, std.ID, mem.ID)
		}
		if std.Method != mem.Method || std.Method != sq.method {
			t.Errorf("%s: method stdout=%q memory=%q sqlite=%q",
				c.marker, std.Method, mem.Method, sq.method)
		}
		if std.Path != mem.Path || std.Path != sq.path {
			t.Errorf("%s: path stdout=%q memory=%q sqlite=%q",
				c.marker, std.Path, mem.Path, sq.path)
		}
		if std.Service != sq.service {
			t.Errorf("%s: service stdout=%q sqlite=%q", c.marker, std.Service, sq.service)
		}
		if std.ServiceSource != sq.serviceSource {
			t.Errorf("%s: service_source stdout=%q sqlite=%q",
				c.marker, std.ServiceSource, sq.serviceSource)
		}
		if std.CorrelationID != sq.corrID {
			t.Errorf("%s: correlation_id stdout=%q sqlite=%q",
				c.marker, std.CorrelationID, sq.corrID)
		}
		if std.CorrelationSource != sq.corrSource {
			t.Errorf("%s: correlation_source stdout=%q sqlite=%q",
				c.marker, std.CorrelationSource, sq.corrSource)
		}
		if std.BodyOriginalSize != sq.origSize {
			t.Errorf("%s: body_original_size stdout=%d sqlite=%d",
				c.marker, std.BodyOriginalSize, sq.origSize)
		}
		if (sq.truncated == 1) != std.BodyTruncated {
			t.Errorf("%s: body_truncated stdout=%v sqlite=%d",
				c.marker, std.BodyTruncated, sq.truncated)
		}
		if string(sq.body) != string(std.Body) {
			t.Errorf("%s: body bytes diverge between stdout and sqlite", c.marker)
		}
		if std.Timestamp.UnixNano() != sq.timestamp {
			t.Errorf("%s: timestamp stdout=%d sqlite=%d",
				c.marker, std.Timestamp.UnixNano(), sq.timestamp)
		}
		host := http.Header(std.Headers).Get(capture.HostHeader)
		if host != sq.host {
			t.Errorf("%s: host stdout=%q sqlite=%q", c.marker, host, sq.host)
		}
		if c.wantTrunc && sq.truncated != 1 {
			t.Errorf("%s: expected body_truncated=1 in sqlite", c.marker)
		}
	}
}

func TestIntegration_SQLite_UnwritableDirectoryFailsStartup(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("posix-mode directory permission test")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	cfg := config.Defaults()
	cfg.Sinks.SQLite = true
	cfg.Sinks.SQLitePath = filepath.Join(locked, "x.db")

	var logBuf bytes.Buffer
	_, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err == nil {
		t.Fatal("expected app.Build to fail when sqlite directory is unwritable")
	}
	if !strings.Contains(err.Error(), "sqlite") {
		t.Errorf("error %q does not mention sqlite", err)
	}
}

func TestIntegration_SQLite_MissingDirectoryFailsStartup(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.SQLite = true
	cfg.Sinks.SQLitePath = filepath.Join(t.TempDir(), "nope", "x.db")

	var logBuf bytes.Buffer
	_, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err == nil {
		t.Fatal("expected app.Build to fail when sqlite directory does not exist")
	}
}

func TestIntegration_HeaderRedaction(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 2
	cfg.QueueSize = 64
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 100
	cfg.Redaction.Headers = []string{"authorization"}

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	// Request A: has Authorization header that must be redacted, plus a safe header.
	fire(t, ts.URL+"/redact-test", "POST", []byte("body-a"), http.Header{
		"Authorization": []string{"Bearer secret-token"},
		"X-Safe":        []string{"keep-me"},
		"X-Marker":      []string{"req-a"},
	})

	// Request B: no Authorization header; X-Other must pass through untouched.
	fire(t, ts.URL+"/redact-test", "POST", []byte("body-b"), http.Header{
		"X-Other":  []string{"visible"},
		"X-Marker": []string{"req-b"},
	})

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 2 && a.Memory.Len() >= 2
	}, 5*time.Second) {
		t.Fatalf("timed out: stdout=%d memory=%d", stdoutBuf.CountLines(), a.Memory.Len())
	}

	stdoutRecords, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if len(stdoutRecords) != 2 {
		t.Fatalf("stdout: got %d records want 2", len(stdoutRecords))
	}

	byMarker := map[string]capture.CapturedRequest{}
	for _, r := range stdoutRecords {
		ms := r.Headers["X-Marker"]
		if len(ms) == 1 {
			byMarker[ms[0]] = r
		}
	}

	recA, ok := byMarker["req-a"]
	if !ok {
		t.Fatal("missing stdout record for req-a")
	}
	if vals := recA.Headers["Authorization"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("req-a Authorization: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := recA.Headers["X-Safe"]; len(vals) != 1 || vals[0] != "keep-me" {
		t.Errorf("req-a X-Safe: got %v, want [keep-me]", vals)
	}

	recB, ok := byMarker["req-b"]
	if !ok {
		t.Fatal("missing stdout record for req-b")
	}
	if vals := recB.Headers["X-Other"]; len(vals) != 1 || vals[0] != "visible" {
		t.Errorf("req-b X-Other: got %v, want [visible]", vals)
	}
	if _, hasAuth := recB.Headers["Authorization"]; hasAuth {
		t.Error("req-b should have no Authorization header")
	}

	// Verify memory sink sees identical redaction as stdout.
	memRecords := recentRequests(t, a.Memory, 10)
	memByMarker := map[string]*capture.CapturedRequest{}
	for _, r := range memRecords {
		ms := r.Headers["X-Marker"]
		if len(ms) == 1 {
			memByMarker[ms[0]] = r
		}
	}

	memA, ok := memByMarker["req-a"]
	if !ok {
		t.Fatal("missing memory record for req-a")
	}
	if vals := memA.Headers["Authorization"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("memory req-a Authorization: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := memA.Headers["X-Safe"]; len(vals) != 1 || vals[0] != "keep-me" {
		t.Errorf("memory req-a X-Safe: got %v, want [keep-me]", vals)
	}

	memB, ok := memByMarker["req-b"]
	if !ok {
		t.Fatal("missing memory record for req-b")
	}
	if vals := memB.Headers["X-Other"]; len(vals) != 1 || vals[0] != "visible" {
		t.Errorf("memory req-b X-Other: got %v, want [visible]", vals)
	}

	// Confirm IDs match between stdout and memory.
	if recA.ID != memA.ID {
		t.Errorf("req-a ID: stdout=%q memory=%q", recA.ID, memA.ID)
	}
	if recB.ID != memB.ID {
		t.Errorf("req-b ID: stdout=%q memory=%q", recB.ID, memB.ID)
	}

	if strings.Contains(logBuf.String(), "unredacted") {
		t.Errorf("unredacted warning should not fire when rules are configured, got:\n%s", logBuf.String())
	}
}

func TestIntegration_HeaderAndQueryRedaction(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 2
	cfg.QueueSize = 64
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 100
	cfg.Redaction.Headers = []string{"authorization"}
	cfg.Redaction.QueryParams = []string{"token"}

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	fire(t, ts.URL+"/q-test?token=secret-token&page=2", "POST", []byte("body"), http.Header{
		"Authorization": []string{"Bearer secret"},
		"X-Safe":        []string{"keep-me"},
		"X-Marker":      []string{"req-q"},
	})

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 1 && a.Memory.Len() >= 1
	}, 5*time.Second) {
		t.Fatalf("timed out: stdout=%d memory=%d", stdoutBuf.CountLines(), a.Memory.Len())
	}

	stdoutRecords, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if len(stdoutRecords) != 1 {
		t.Fatalf("stdout: got %d records want 1", len(stdoutRecords))
	}
	rec := stdoutRecords[0]

	if vals := rec.Headers["Authorization"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("Authorization: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := rec.Headers["X-Safe"]; len(vals) != 1 || vals[0] != "keep-me" {
		t.Errorf("X-Safe: got %v, want [keep-me]", vals)
	}
	if vals := rec.Query["token"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("query token: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := rec.Query["page"]; len(vals) != 1 || vals[0] != "2" {
		t.Errorf("query page: got %v, want [2]", vals)
	}

	memRecords := recentRequests(t, a.Memory, 1)
	if len(memRecords) != 1 {
		t.Fatalf("memory: got %d records want 1", len(memRecords))
	}
	memRec := memRecords[0]
	if vals := memRec.Headers["Authorization"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("memory Authorization: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := memRec.Query["token"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("memory query token: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := memRec.Query["page"]; len(vals) != 1 || vals[0] != "2" {
		t.Errorf("memory query page: got %v, want [2]", vals)
	}

	if rec.ID != memRec.ID {
		t.Errorf("ID: stdout=%q memory=%q", rec.ID, memRec.ID)
	}

	if strings.Contains(logBuf.String(), "unredacted") {
		t.Errorf("unredacted warning should not fire when rules are configured, got:\n%s", logBuf.String())
	}
}

func TestIntegration_JSONPathRedaction(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 2
	cfg.QueueSize = 64
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 100
	cfg.Redaction.JSONPaths = []string{"credentials.token"}

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	body := []byte(`{"credentials":{"token":"deadbeef","user":"alice"},"meta":{"keep":true}}`)
	fire(t, ts.URL+"/json-redact?keep=2", "POST", body, http.Header{
		"Content-Type": []string{"application/json"},
		"X-Safe":       []string{"keep-me"},
		"X-Marker":     []string{"req-json"},
	})

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 1 && a.Memory.Len() >= 1
	}, 5*time.Second) {
		t.Fatalf("timed out: stdout=%d memory=%d", stdoutBuf.CountLines(), a.Memory.Len())
	}

	stdoutRecords, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if len(stdoutRecords) != 1 {
		t.Fatalf("stdout: got %d records want 1", len(stdoutRecords))
	}
	rec := stdoutRecords[0]

	if got := gjson.GetBytes(rec.Body, "credentials.token").String(); got != redact.Redacted {
		t.Errorf("credentials.token: got %q, want %q", got, redact.Redacted)
	}
	if got := gjson.GetBytes(rec.Body, "credentials.user").String(); got != "alice" {
		t.Errorf("credentials.user: got %q, want alice", got)
	}
	if got := gjson.GetBytes(rec.Body, "meta.keep").String(); got != "true" {
		t.Errorf("meta.keep: got %q, want true", got)
	}

	if vals := rec.Headers["X-Safe"]; len(vals) != 1 || vals[0] != "keep-me" {
		t.Errorf("X-Safe: got %v, want [keep-me] (JSON-path rule must not touch headers)", vals)
	}
	if vals := rec.Query["keep"]; len(vals) != 1 || vals[0] != "2" {
		t.Errorf("query keep: got %v, want [2] (JSON-path rule must not touch query)", vals)
	}

	memRecords := recentRequests(t, a.Memory, 1)
	if len(memRecords) != 1 {
		t.Fatalf("memory: got %d records want 1", len(memRecords))
	}
	memRec := memRecords[0]
	if got := gjson.GetBytes(memRec.Body, "credentials.token").String(); got != redact.Redacted {
		t.Errorf("memory credentials.token: got %q, want %q", got, redact.Redacted)
	}
	if got := gjson.GetBytes(memRec.Body, "credentials.user").String(); got != "alice" {
		t.Errorf("memory credentials.user: got %q, want alice", got)
	}
	if got := gjson.GetBytes(memRec.Body, "meta.keep").String(); got != "true" {
		t.Errorf("memory meta.keep: got %q, want true", got)
	}

	if rec.ID != memRec.ID {
		t.Errorf("ID: stdout=%q memory=%q", rec.ID, memRec.ID)
	}

	if got := a.Ruleset.RedactionErrorsTotal(); got != 0 {
		t.Errorf("RedactionErrorsTotal: got %d, want 0", got)
	}
	if strings.Contains(logBuf.String(), "unredacted") {
		t.Errorf("unredacted warning should not fire when rules are configured, got:\n%s", logBuf.String())
	}
}

func TestIntegration_RegexRedaction(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 2
	cfg.QueueSize = 64
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 100
	cfg.Redaction.Regex = []config.RegexRuleConfig{
		{Name: "ipv4", Pattern: `\b(?:\d{1,3}\.){3}\d{1,3}\b`},
		{Name: "token_like", Pattern: `Bearer\s+[A-Za-z0-9._-]+`},
		{Name: "aws_access_key", Pattern: `AKIA[0-9A-Z]{16}`},
	}

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	body := []byte(`{"client":"10.0.0.1","note":"ok"}`)
	fire(t, ts.URL+"/regex-redact?key=AKIAABCDEFGHIJKLMNOP&page=2", "POST", body, http.Header{
		"Content-Type":  []string{"application/json"},
		"Authorization": []string{"Bearer deadbeefdeadbeef"},
		"X-Marker":      []string{"req-regex"},
	})

	if !waitFor(func() bool {
		return stdoutBuf.CountLines() >= 1 && a.Memory.Len() >= 1
	}, 5*time.Second) {
		t.Fatalf("timed out: stdout=%d memory=%d", stdoutBuf.CountLines(), a.Memory.Len())
	}

	stdoutRecords, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if len(stdoutRecords) != 1 {
		t.Fatalf("stdout: got %d records want 1", len(stdoutRecords))
	}
	rec := stdoutRecords[0]

	wantBody := `{"client":"` + redact.Redacted + `","note":"ok"}`
	if string(rec.Body) != wantBody {
		t.Errorf("body: got %q, want %q", rec.Body, wantBody)
	}
	if vals := rec.Headers["Authorization"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("Authorization: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := rec.Query["key"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("query key: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := rec.Query["page"]; len(vals) != 1 || vals[0] != "2" {
		t.Errorf("query page: got %v, want [2]", vals)
	}

	memRecords := recentRequests(t, a.Memory, 1)
	if len(memRecords) != 1 {
		t.Fatalf("memory: got %d records want 1", len(memRecords))
	}
	memRec := memRecords[0]
	if string(memRec.Body) != wantBody {
		t.Errorf("memory body: got %q, want %q", memRec.Body, wantBody)
	}
	if vals := memRec.Headers["Authorization"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("memory Authorization: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := memRec.Query["key"]; len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("memory query key: got %v, want [%q]", vals, redact.Redacted)
	}
	if vals := memRec.Query["page"]; len(vals) != 1 || vals[0] != "2" {
		t.Errorf("memory query page: got %v, want [2]", vals)
	}

	if rec.ID != memRec.ID {
		t.Errorf("ID: stdout=%q memory=%q", rec.ID, memRec.ID)
	}

	if got := a.Ruleset.RedactionErrorsTotal(); got != 0 {
		t.Errorf("RedactionErrorsTotal: got %d, want 0", got)
	}
	if strings.Contains(logBuf.String(), "unredacted") {
		t.Errorf("unredacted warning should not fire when rules are configured, got:\n%s", logBuf.String())
	}
}

func TestIntegration_CookieAndHeaderOrdering_HeaderRuleWins(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.QueueSize = 32
	cfg.Sinks.Stdout = true
	cfg.Redaction.Headers = []string{"cookie"}
	cfg.Redaction.Cookies = []config.CookieRuleConfig{
		{Mode: "redact", Names: []string{"session_id"}},
	}

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	_, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	fire(t, ts.URL+"/cookie-ordering-header-wins", "GET", nil, http.Header{
		"Cookie":   []string{"session_id=secret; user_pref=dark; tracking=abc"},
		"X-Marker": []string{"hdr-wins"},
	})

	if !waitFor(func() bool { return stdoutBuf.CountLines() >= 1 }, 5*time.Second) {
		t.Fatal("timed out waiting for record")
	}
	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records: got %d want 1", len(records))
	}
	vals := records[0].Headers["Cookie"]
	if len(vals) != 1 || vals[0] != redact.Redacted {
		t.Errorf("Cookie: got %v, want [%q] (header rule must overwrite the cookie redactor's output)", vals, redact.Redacted)
	}
}

func TestIntegration_AdminPort_HealthzAndCapturePort(t *testing.T) {
	t.Parallel()

	adminAddr := freeAddr(t)
	captureAddr := freeAddr(t)

	cfg := config.Defaults()
	cfg.CapturePort = mustPort(t, captureAddr)
	cfg.Admin.Bind = adminAddr
	cfg.Sinks.Stdout = true

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), &stdoutBuf)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after context cancel")
		}
	}()

	// Wait for both ports to be ready.
	waitPort(t, adminAddr, 3*time.Second)
	waitPort(t, captureAddr, 3*time.Second)

	// Hit /healthz on the admin port.
	resp, err := http.Get("http://" + adminAddr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status: got %d want 200", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Errorf("healthz body: got %q want ok", string(body))
	}

	// Fire a captured request at the capture port and assert it lands.
	captureResp, err := http.Post("http://"+captureAddr+"/test", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("capture POST: %v", err)
	}
	io.Copy(io.Discard, captureResp.Body)
	captureResp.Body.Close()
	if captureResp.StatusCode != http.StatusAccepted {
		t.Errorf("capture status: got %d want 202", captureResp.StatusCode)
	}

	if !waitFor(func() bool { return stdoutBuf.CountLines() >= 1 }, 5*time.Second) {
		t.Fatal("timed out waiting for captured record in stdout")
	}
}

func TestIntegration_AdminPort_BindRefusal_NonLoopback(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Admin.Bind = "0.0.0.0:0"
	// Token and InsecureListen both unset: should refuse.

	var logBuf bytes.Buffer
	_, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err == nil {
		t.Fatal("expected app.Build to fail for non-loopback bind without token or insecure flag")
	}
	if !strings.Contains(err.Error(), "admin") {
		t.Errorf("error %q should mention admin", err.Error())
	}
}

func TestIntegration_AdminPort_HealthzIgnoresBogusAuth(t *testing.T) {
	t.Parallel()

	adminAddr := freeAddr(t)
	captureAddr := freeAddr(t)

	cfg := config.Defaults()
	cfg.CapturePort = mustPort(t, captureAddr)
	cfg.Admin.Bind = adminAddr

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after context cancel")
		}
	}()

	waitPort(t, adminAddr, 3*time.Second)

	req, _ := http.NewRequest(http.MethodGet, "http://"+adminAddr+"/healthz", nil)
	req.Header.Set("Authorization", "Bearer totally-invalid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz with auth header: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz with bogus auth: got %d want 200", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Errorf("body: got %q want ok", string(body))
	}
}

// TestIntegration_ShutdownDrainsQueueToCtxAwareSink verifies that records
// already in the capture queue when Serve's context is cancelled are still
// written to ctx-aware sinks during shutdown drain. A slowSink delays each
// write and returns ctx.Err() on cancellation; if workers received the
// cancelled root ctx the drain would abort and records would be lost.
func TestIntegration_ShutdownDrainsQueueToCtxAwareSink(t *testing.T) {
	t.Parallel()

	adminAddr := freeAddr(t)
	captureAddr := freeAddr(t)

	cfg := config.Defaults()
	cfg.CapturePort = mustPort(t, captureAddr)
	cfg.Admin.Bind = adminAddr
	cfg.Workers = 1
	cfg.QueueSize = 32

	const records = 5
	const perWriteDelay = 100 * time.Millisecond

	memSink := sinks.NewMemorySink(100)
	slow := &slowSink{inner: memSink, delay: perWriteDelay}

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard, slow)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx) }()

	waitPort(t, captureAddr, 3*time.Second)

	for i := range records {
		body := fmt.Sprintf("payload-%d", i)
		resp, err := http.Post("http://"+captureAddr+"/drain-test", "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatalf("capture POST %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("capture POST %d: got %d want 202", i, resp.StatusCode)
		}
	}

	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}

	got := memSink.Recent(records + 1)
	if len(got) != records {
		t.Fatalf("memory sink recorded %d records after shutdown drain; want %d (records enqueued before SIGTERM must survive drain)", len(got), records)
	}
}

// mustPort extracts the port number from "host:port".
func mustPort(t *testing.T, addr string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("mustPort: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("mustPort parse %q: %v", portStr, err)
	}
	return port
}

// waitPort polls until the given address accepts a TCP connection or the
// timeout expires.
func waitPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("port %s not ready within %v", addr, timeout)
}

func TestIntegration_CookieOrdering_OnlyNamedCookieRedacted(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.QueueSize = 32
	cfg.Sinks.Stdout = true
	cfg.Redaction.Cookies = []config.CookieRuleConfig{
		{Mode: "redact", Names: []string{"session_id"}},
	}

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	_, ts, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	fire(t, ts.URL+"/cookie-ordering-cookie-only", "GET", nil, http.Header{
		"Cookie":   []string{"session_id=secret; user_pref=dark; tracking=abc"},
		"X-Marker": []string{"cookie-only"},
	})

	if !waitFor(func() bool { return stdoutBuf.CountLines() >= 1 }, 5*time.Second) {
		t.Fatal("timed out waiting for record")
	}
	records, err := decodeLines(stdoutBuf.String())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records: got %d want 1", len(records))
	}
	vals := records[0].Headers["Cookie"]
	if len(vals) != 1 {
		t.Fatalf("Cookie: got %v, want 1 value", vals)
	}
	got := vals[0]
	if !strings.Contains(got, "session_id="+redact.Redacted) {
		t.Errorf("Cookie %q missing session_id=%q", got, redact.Redacted)
	}
	if !strings.Contains(got, "user_pref=dark") {
		t.Errorf("Cookie %q missing user_pref=dark (sibling cookie should survive)", got)
	}
	if !strings.Contains(got, "tracking=abc") {
		t.Errorf("Cookie %q missing tracking=abc (sibling cookie should survive)", got)
	}
}

// startFullApp boots both the capture port and the admin port on ephemeral
// addresses and returns the capture URL, the admin base URL, and a teardown
// function. The admin port is probed until ready before the function returns.
func startFullApp(t *testing.T, cfg config.Config) (captureURL, adminURL string, teardown func()) {
	t.Helper()

	a, err := app.Build(cfg, testLogger(io.Discard), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Capture port via httptest so we get an ephemeral address.
	captureSrv := httptest.NewServer(a.Handler)
	a.Workers.Start(ctx)

	// Admin port.
	adminSrv := httptest.NewServer(a.Admin.Router())

	teardown = func() {
		captureSrv.Close()
		adminSrv.Close()
		a.Queue.Close()
		a.Workers.Wait()
		cancel()
	}
	return captureSrv.URL, adminSrv.URL, teardown
}

func TestIntegration_Metrics(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.QueueSize = 64

	captureURL, adminURL, teardown := startFullApp(t, cfg)
	defer teardown()

	// Initial GET /metrics — assert 200 and documented shape.
	resp, err := http.Get(adminURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	// promhttp negotiates the Prometheus text format and appends an escaping
	// parameter (e.g. "; escaping=underscores") to the content type. Assert the
	// stable prefix rather than the exact string.
	if !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type: got %q want prefix text/plain; version=0.0.4", ct)
	}

	metricNames := []string{
		"httpcatch_dropped_total",
		"httpcatch_captured_without_correlation_total",
		"httpcatch_captured_without_service_total",
		"httpcatch_redaction_errors_total",
		"httpcatch_build_info",
	}
	bodyStr := string(body)
	for _, name := range metricNames {
		if !strings.Contains(bodyStr, name) {
			t.Errorf("body missing metric %q\nbody:\n%s", name, bodyStr)
		}
	}
	if !strings.Contains(bodyStr, "httpcatch_captured_without_service_total 0") {
		t.Errorf("expected initial without_service_total to be 0\nbody:\n%s", bodyStr)
	}

	// Fire a raw TCP request with no Host header to bump captured_without_service_total.
	addr := strings.TrimPrefix(captureURL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial capture port: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("POST /u HTTP/1.0\r\nContent-Length: 2\r\n\r\nhi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 512)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "202 Accepted") {
		t.Fatalf("expected 202, got: %q", string(buf[:n]))
	}

	// Wait for the worker to process the record so the counter increments.
	if !waitFor(func() bool {
		resp2, err2 := http.Get(adminURL + "/metrics")
		if err2 != nil {
			return false
		}
		b, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		return strings.Contains(string(b), "httpcatch_captured_without_service_total 1")
	}, 5*time.Second) {
		t.Error("httpcatch_captured_without_service_total did not reach 1 within timeout")
	}
}

func TestIntegration_Status(t *testing.T) {
	t.Parallel()

	const token = "integration-test-token-status"

	// A single header rule so IsUnredacted() returns false.
	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.QueueSize = 64
	cfg.Admin.Token = token
	cfg.Redaction = config.RedactionConfig{
		Headers: []string{"x-secret"},
	}

	a, err := app.Build(cfg, testLogger(io.Discard), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	captureSrv := httptest.NewServer(a.Handler)
	a.Workers.Start(ctx)
	adminSrv := httptest.NewServer(a.Admin.Router())
	defer func() {
		captureSrv.Close()
		adminSrv.Close()
		a.Queue.Close()
		a.Workers.Wait()
		cancel()
	}()

	bearer := func(req *http.Request) *http.Request {
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	}
	getStatus := func() map[string]any {
		req, _ := http.NewRequest(http.MethodGet, adminSrv.URL+"/status", nil)
		resp, err := http.DefaultClient.Do(bearer(req))
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

	// Initial fetch: unredacted false (one header rule configured), all counters 0.
	m := getStatus()
	if m["unredacted"] != false {
		t.Errorf("initial unredacted: got %v want false", m["unredacted"])
	}
	counters := m["counters"].(map[string]any)
	if counters["captured_without_service_total"].(float64) != 0 {
		t.Errorf("initial without_service_total: got %v want 0", counters["captured_without_service_total"])
	}

	// Confirm version and build_time are present.
	if _, ok := m["version"]; !ok {
		t.Error("status: missing version field")
	}
	if _, ok := m["build_time"]; !ok {
		t.Error("status: missing build_time field")
	}

	// Fire a raw TCP request with no Host header to bump captured_without_service_total.
	addr := strings.TrimPrefix(captureSrv.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial capture port: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("POST /u HTTP/1.0\r\nContent-Length: 2\r\n\r\nhi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 512)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "202 Accepted") {
		t.Fatalf("expected 202, got: %q", string(buf[:n]))
	}

	// Wait for the worker to process the record.
	if !waitFor(func() bool {
		m2 := getStatus()
		c2 := m2["counters"].(map[string]any)
		return c2["captured_without_service_total"].(float64) == 1
	}, 5*time.Second) {
		t.Error("captured_without_service_total did not reach 1 within timeout")
	}
}

// collectIDs walks an HTML parse tree and returns all id attribute values.
func collectIDs(n *html.Node, ids map[string]bool) {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" {
				ids[a.Val] = true
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectIDs(c, ids)
	}
}

func TestIntegration_UIShell(t *testing.T) {
	t.Parallel()

	const token = "integration-ui-token"

	cfg := config.Defaults()
	cfg.Admin.Token = token

	_, adminURL, teardown := startFullApp(t, cfg)
	defer teardown()

	noFollow := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Log in to obtain a session cookie.
	form := url.Values{"token": {token}}
	loginResp, err := noFollow.PostForm(adminURL+"/auth/login", form)
	if err != nil {
		t.Fatalf("POST /auth/login: %v", err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Errorf("login status: got %d want 303", loginResp.StatusCode)
	}
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

	// GET / with the session cookie → 303 redirect to /ui/requests.
	req, _ := http.NewRequest(http.MethodGet, adminURL+"/", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(sessionCookie)
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET / status: got %d want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/requests" {
		t.Errorf("GET / Location: got %q want /ui/requests", loc)
	}

	// GET /ui/requests with the session cookie → 200 + HTML body with shell layout.
	req2, _ := http.NewRequest(http.MethodGet, adminURL+"/ui/requests", nil)
	req2.Header.Set("Accept", "text/html")
	req2.AddCookie(sessionCookie)
	resp2, err := noFollow.Do(req2)
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("GET /ui/requests status: got %d want 200", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("GET /ui/requests Content-Type: got %q", ct)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "httpcatch") {
		t.Error("GET /ui/requests body: missing wordmark httpcatch")
	}
	if !strings.Contains(bodyStr, "/static/app.css") {
		t.Error("GET /ui/requests body: missing /static/app.css reference")
	}
	if !strings.Contains(bodyStr, "/static/app.js") {
		t.Error("GET /ui/requests body: missing /static/app.js reference")
	}

	// Parse HTML and assert the required banner DOM ids are present.
	doc, err := html.Parse(strings.NewReader(bodyStr))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	ids := make(map[string]bool)
	collectIDs(doc, ids)
	for _, id := range []string{
		"chip-unredacted", "chip-dropped", "chip-redaction-errors",
		"chip-service", "chip-correlation",
	} {
		if !ids[id] {
			t.Errorf("layout: missing element with id=%q", id)
		}
	}

	// GET /static/app.css (no auth) → 200 + correct content type.
	cssResp, err := http.Get(adminURL + "/static/app.css")
	if err != nil {
		t.Fatalf("GET /static/app.css: %v", err)
	}
	io.Copy(io.Discard, cssResp.Body)
	cssResp.Body.Close()
	if cssResp.StatusCode != http.StatusOK {
		t.Errorf("GET /static/app.css status: got %d want 200", cssResp.StatusCode)
	}
	if ct := cssResp.Header.Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("GET /static/app.css Content-Type: got %q", ct)
	}

	// GET /static/app.js (no auth) → 200 + correct content type.
	jsResp, err := http.Get(adminURL + "/static/app.js")
	if err != nil {
		t.Fatalf("GET /static/app.js: %v", err)
	}
	io.Copy(io.Discard, jsResp.Body)
	jsResp.Body.Close()
	if jsResp.StatusCode != http.StatusOK {
		t.Errorf("GET /static/app.js status: got %d want 200", jsResp.StatusCode)
	}
	if ct := jsResp.Header.Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
		t.Errorf("GET /static/app.js Content-Type: got %q", ct)
	}
}

// TestIntegration_AllRecordVariants_PipelineRoundTrip boots the full pipeline
// with all three sinks enabled, enqueues one record of each variant directly,
// drains the queue, and asserts each sink observed each variant in the correct
// table (for SQLite) or slot (for memory).
func TestIntegration_AllRecordVariants_PipelineRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "variants.db")

	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.QueueSize = 32
	cfg.Sinks.Stdout = true
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 10
	cfg.Sinks.SQLite = true
	cfg.Sinks.SQLitePath = dbPath

	var stdoutBuf syncBuffer
	var logBuf bytes.Buffer
	a, _, teardown := runPipeline(t, cfg, &stdoutBuf, &logBuf)
	defer teardown()

	corrID := "0af7651916cd43dd8448eb211c80319c"

	reqRec := &capture.CapturedRequest{
		ID:                "req-variant-1",
		Timestamp:         time.Now().UTC(),
		Method:            "GET",
		Path:              "/items",
		Query:             map[string][]string{},
		Headers:           map[string][]string{"Content-Type": {"text/plain"}},
		Cookies:           []capture.Cookie{},
		Body:              []byte("req-body"),
		BodyOriginalSize:  8,
		ContentType:       "text/plain",
		SourceIP:          "10.0.0.1",
		Service:           "items",
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationID:     corrID,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	respRec := &capture.ResponseEvent{
		ID:                "resp-variant-1",
		Timestamp:         time.Now().UTC(),
		CorrelationID:     corrID,
		CorrelationSource: capture.CorrelationSourceTraceparent,
		Service:           "items",
		ServiceSource:     capture.ServiceSourceHeader,
		Status:            200,
		Headers:           map[string][]string{"Content-Type": {"application/json"}},
		Body:              []byte(`{"ok":true}`),
		BodyOriginalSize:  11,
		ContentType:       "application/json",
		DurationMS:        10,
	}
	outRec := &capture.OutboundEvent{
		ID:                "out-variant-1",
		Timestamp:         time.Now().UTC(),
		CorrelationID:     corrID,
		CorrelationSource: capture.CorrelationSourceTraceparent,
		Service:           "items",
		ServiceSource:     capture.ServiceSourceHeader,
		DurationMS:        5,
		Request: capture.OutboundRequestHalf{
			Method:           "POST",
			Path:             "/payments",
			Headers:          map[string][]string{"Content-Type": {"application/json"}},
			Body:             []byte(`{"amount":1}`),
			BodyOriginalSize: 12,
			ContentType:      "application/json",
		},
		Response: &capture.OutboundResponseHalf{
			Status:           201,
			Headers:          map[string][]string{"X-Tx": {"txid"}},
			Body:             []byte(`{"created":true}`),
			BodyOriginalSize: 16,
			ContentType:      "application/json",
		},
	}

	a.Queue.Enqueue(reqRec)
	a.Queue.Enqueue(respRec)
	a.Queue.Enqueue(outRec)

	if !waitFor(func() bool { return stdoutBuf.CountLines() >= 3 }, 10*time.Second) {
		t.Fatalf("timed out waiting for 3 stdout lines; got %d", stdoutBuf.CountLines())
	}

	// Verify stdout emits a kind discriminator for each variant.
	lines := strings.Split(strings.TrimRight(stdoutBuf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout: got %d lines want 3", len(lines))
	}
	kindsSeen := map[string]bool{}
	for _, line := range lines {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("stdout line not valid JSON: %v", err)
		}
		var kind string
		if err := json.Unmarshal(obj["kind"], &kind); err != nil {
			t.Fatalf("kind field missing or invalid: %v", err)
		}
		kindsSeen[kind] = true
	}
	for _, want := range []string{"request", "response_event", "outbound_event"} {
		if !kindsSeen[want] {
			t.Errorf("stdout missing kind %q", want)
		}
	}

	// Verify memory holds all three variants (by correlation_id O(1) index).
	if got := a.Memory.ByCorrelationID(corrID); got == nil {
		t.Error("ByCorrelationID: expected non-nil for correlation_id that was enqueued")
	}
	recent := a.Memory.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("memory: got %d records want 3", len(recent))
	}

	// Verify SQLite: CapturedRequest goes to captured_requests, events go to events.
	sqliteDB, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open sqlite for verification: %v", err)
	}
	t.Cleanup(func() { _ = sqliteDB.Close() })

	var reqCount int
	if err := sqliteDB.QueryRow("SELECT COUNT(*) FROM captured_requests WHERE id=?", reqRec.ID).Scan(&reqCount); err != nil {
		t.Fatalf("count captured_requests: %v", err)
	}
	if reqCount != 1 {
		t.Errorf("captured_requests: got %d rows for CapturedRequest, want 1", reqCount)
	}

	var respCount int
	if err := sqliteDB.QueryRow("SELECT COUNT(*) FROM events WHERE id=? AND type='response'", respRec.ID).Scan(&respCount); err != nil {
		t.Fatalf("count events (response): %v", err)
	}
	if respCount != 1 {
		t.Errorf("events: got %d rows for ResponseEvent, want 1", respCount)
	}

	// ResponseEvent should have NULL request-half columns.
	var reqMethod sql.NullString
	if err := sqliteDB.QueryRow("SELECT request_method FROM events WHERE id=?", respRec.ID).Scan(&reqMethod); err != nil {
		t.Fatalf("select request_method for ResponseEvent: %v", err)
	}
	if reqMethod.Valid {
		t.Errorf("ResponseEvent request_method: expected NULL, got %q", reqMethod.String)
	}

	var outCount int
	if err := sqliteDB.QueryRow("SELECT COUNT(*) FROM events WHERE id=? AND type='outbound'", outRec.ID).Scan(&outCount); err != nil {
		t.Fatalf("count events (outbound): %v", err)
	}
	if outCount != 1 {
		t.Errorf("events: got %d rows for OutboundEvent, want 1", outCount)
	}

	// OutboundEvent with non-nil response should have populated response columns.
	var respStatus sql.NullInt64
	if err := sqliteDB.QueryRow("SELECT response_status FROM events WHERE id=?", outRec.ID).Scan(&respStatus); err != nil {
		t.Fatalf("select response_status for OutboundEvent: %v", err)
	}
	if !respStatus.Valid || respStatus.Int64 != 201 {
		t.Errorf("OutboundEvent response_status: got %v, want 201", respStatus)
	}
}

// TestIntegration_GetRequests_MemoryAndSQLite boots the full app with both
// sinks enabled, fires N captured requests through the capture port, then
// asserts GET /requests returns the population in correct order and that
// pagination exhausts to the same union.
func TestIntegration_GetRequests_MemoryAndSQLite(t *testing.T) {
	t.Parallel()

	const token = "int-test-token-requests"
	const numRequests = 12
	const pageSize = 5

	cfg := config.Defaults()
	cfg.Workers = 2
	cfg.QueueSize = 64
	cfg.Sinks.Memory = true
	cfg.Sinks.MemoryCapacity = 100
	cfg.Sinks.SQLite = true
	cfg.Sinks.SQLitePath = t.TempDir() + "/requests-test.db"
	cfg.Admin.Token = token
	cfg.Admin.Bind = freeAddr(t)

	captureURL, adminURL, teardown := startFullApp(t, cfg)
	defer teardown()

	bearer := func(req *http.Request) *http.Request {
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	}

	// Fire numRequests captured requests, each with a distinct X-Request-Id for
	// correlation.
	for i := range numRequests {
		req, _ := http.NewRequest(http.MethodGet, captureURL+"/api/items", nil)
		req.Header.Set("X-Request-Id", fmt.Sprintf("req-%03d", i))
		req.Header.Set("X-Httpcatch-Service", "items")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("fire %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("fire %d: got %d want 202", i, resp.StatusCode)
		}
	}

	// Wait for all records to be processed.
	if !waitFor(func() bool {
		req, _ := http.NewRequest(http.MethodGet, adminURL+"/requests?limit=100", nil)
		resp, err := http.DefaultClient.Do(bearer(req))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var b struct {
			Records []map[string]any `json:"records"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
			return false
		}
		return len(b.Records) >= numRequests
	}, 10*time.Second) {
		t.Fatal("timed out waiting for all records to appear in GET /requests")
	}

	// GET /requests with no params → 200, correct source header.
	req1, _ := http.NewRequest(http.MethodGet, adminURL+"/requests?limit=100", nil)
	resp1, err := http.DefaultClient.Do(bearer(req1))
	if err != nil {
		t.Fatalf("GET /requests: %v", err)
	}
	var body1 struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.NewDecoder(resp1.Body).Decode(&body1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp1.StatusCode)
	}
	if len(body1.Records) != numRequests {
		t.Errorf("records: got %d want %d", len(body1.Records), numRequests)
	}

	// Verify sort order: timestamps must be non-increasing.
	for i := 1; i < len(body1.Records); i++ {
		tsStr := body1.Records[i]["timestamp"].(string)
		prevStr := body1.Records[i-1]["timestamp"].(string)
		if tsStr > prevStr {
			t.Errorf("sort order violation at [%d]: %q > %q", i, tsStr, prevStr)
		}
	}

	// Verify each row carries required fields.
	requiredFields := []string{"id", "kind", "timestamp", "service", "method", "path", "correlation_id", "source_ip", "event_count", "has_events", "status"}
	for i, rec := range body1.Records {
		for _, f := range requiredFields {
			if _, ok := rec[f]; !ok {
				t.Errorf("records[%d] missing field %q", i, f)
			}
		}
		if rec["kind"] != "request" {
			t.Errorf("records[%d].kind: got %v want request", i, rec["kind"])
		}
	}

	// Paginate through with pageSize and assert union equals population.
	var pagedIDs []string
	var cursorParam string
	for {
		url := fmt.Sprintf("%s/requests?limit=%d", adminURL, pageSize)
		if cursorParam != "" {
			url += "&cursor=" + cursorParam
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(bearer(req))
		if err != nil {
			t.Fatalf("paginate GET /requests: %v", err)
		}
		var b struct {
			Records    []map[string]any `json:"records"`
			NextCursor *string          `json:"next_cursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
			resp.Body.Close()
			t.Fatalf("paginate decode: %v", err)
		}
		resp.Body.Close()
		for _, rec := range b.Records {
			pagedIDs = append(pagedIDs, rec["id"].(string))
		}
		if b.NextCursor == nil {
			break
		}
		cursorParam = *b.NextCursor
	}

	if len(pagedIDs) != numRequests {
		t.Errorf("pagination union: got %d want %d", len(pagedIDs), numRequests)
	}
	// No duplicates.
	seen := make(map[string]struct{})
	for _, id := range pagedIDs {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate id %q in pagination result", id)
		}
		seen[id] = struct{}{}
	}
}

// TestIntegration_GetRequests_Auth checks auth behaviour on GET /requests.
func TestIntegration_GetRequests_Auth(t *testing.T) {
	t.Parallel()

	const token = "int-test-token-auth-requests"
	cfg := config.Defaults()
	cfg.Workers = 1
	cfg.QueueSize = 16
	cfg.Admin.Token = token
	cfg.Admin.Bind = freeAddr(t)

	_, adminURL, teardown := startFullApp(t, cfg)
	defer teardown()

	// Unauthenticated → 401.
	req, _ := http.NewRequest(http.MethodGet, adminURL+"/requests", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /requests unauthenticated: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated: got %d want 401", resp.StatusCode)
	}

	// Valid bearer → 200 with json.
	req2, _ := http.NewRequest(http.MethodGet, adminURL+"/requests", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET /requests bearer: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("bearer: got %d want 200", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: got %q want application/json; charset=utf-8", ct)
	}
}

func TestIntegration_StartupWarnings_UnboundedBodyCap(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.BodyCap = 0
	cfg.Sinks.Stdout = true

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	out := logBuf.String()
	if !strings.Contains(out, app.UnboundedBodyCapWarning) {
		t.Errorf("expected unbounded-body-cap warning when body_cap=0, got:\n%s", out)
	}
}

func TestIntegration_StartupWarnings_UnboundedBodyCap_DoesNotFireOnDefault(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Redaction.Headers = []string{"authorization"}

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	if strings.Contains(logBuf.String(), app.UnboundedBodyCapWarning) {
		t.Errorf("unbounded-body-cap warning must not fire at default body_cap (%d), got:\n%s",
			cfg.BodyCap, logBuf.String())
	}
}

func TestIntegration_StartupWarnings_UnboundedEventsPayload(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.MaxEventsPayload = 0
	cfg.Sinks.Stdout = true

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	out := logBuf.String()
	if !strings.Contains(out, app.UnboundedEventsPayloadWarning) {
		t.Errorf("expected unbounded-events-payload warning when max_events_payload=0, got:\n%s", out)
	}
}

func TestIntegration_StartupWarnings_UnboundedEventsPayload_DoesNotFireOnDefault(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Redaction.Headers = []string{"authorization"}

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	if strings.Contains(logBuf.String(), app.UnboundedEventsPayloadWarning) {
		t.Errorf("unbounded-events-payload warning must not fire at default max_events_payload (%d), got:\n%s",
			cfg.MaxEventsPayload, logBuf.String())
	}
}

func TestIntegration_StartupWarnings_PlaintextSessionCookie(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Admin.Bind = freeAddr(t) // 127.0.0.1:N — adjusted below.
	// Force a non-loopback host so the bind reason is token-configured.
	cfg.Admin.Bind = "0.0.0.0" + cfg.Admin.Bind[strings.LastIndex(cfg.Admin.Bind, ":"):]
	cfg.Admin.Token = "x" //nolint:gosec // test fixture, not a real secret
	cfg.Admin.SessionSecure = false

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	out := logBuf.String()
	if !strings.Contains(out, app.PlaintextSessionCookieWarning) {
		t.Errorf("expected plaintext-session-cookie warning on non-loopback bind with session_secure=false, got:\n%s", out)
	}
}

func TestIntegration_StartupWarnings_PlaintextSessionCookie_DoesNotFireOnLoopback(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults() // bind defaults to 127.0.0.1:8081
	cfg.Sinks.Stdout = true
	cfg.Admin.SessionSecure = false

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	if strings.Contains(logBuf.String(), app.PlaintextSessionCookieWarning) {
		t.Errorf("plaintext-session-cookie warning must not fire on loopback bind, got:\n%s", logBuf.String())
	}
}

func TestIntegration_StartupWarnings_PlaintextSessionCookie_DoesNotFireWhenSecure(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Admin.Bind = freeAddr(t)
	cfg.Admin.Bind = "0.0.0.0" + cfg.Admin.Bind[strings.LastIndex(cfg.Admin.Bind, ":"):]
	cfg.Admin.Token = "x" //nolint:gosec // test fixture, not a real secret
	cfg.Admin.SessionSecure = true

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	if strings.Contains(logBuf.String(), app.PlaintextSessionCookieWarning) {
		t.Errorf("plaintext-session-cookie warning must not fire when session_secure=true, got:\n%s", logBuf.String())
	}
}

func TestIntegration_StartupWarnings_UnboundedEventsBatch(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.MaxEventsPerBatch = 0
	cfg.Sinks.Stdout = true

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	out := logBuf.String()
	if !strings.Contains(out, app.UnboundedEventsBatchWarning) {
		t.Errorf("expected unbounded-events-batch warning when max_events_per_batch=0, got:\n%s", out)
	}
}

func TestIntegration_StartupWarnings_UnboundedEventsBatch_DoesNotFireOnDefault(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Sinks.Stdout = true
	cfg.Redaction.Headers = []string{"authorization"}

	var logBuf bytes.Buffer
	a, err := app.Build(cfg, testLogger(&logBuf), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	a.EmitStartupWarnings()

	if strings.Contains(logBuf.String(), app.UnboundedEventsBatchWarning) {
		t.Errorf("unbounded-events-batch warning must not fire at default max_events_per_batch (%d), got:\n%s",
			cfg.MaxEventsPerBatch, logBuf.String())
	}
}

// Ensure the redact package is used via the ruleset wiring in Build; this
// compile-time reference keeps the import alive if test helpers above change.
var _ = (*redact.Ruleset)(nil)

// adminAddr starts a real admin server and returns its base URL plus the App.
func adminAddr(t *testing.T, token string) (string, *app.App) {
	t.Helper()
	addr := freeAddr(t)
	cfg := config.Defaults()
	cfg.Admin.Bind = addr
	cfg.Admin.Token = token
	cfg.Admin.SessionTTL = time.Hour
	cfg.Sinks.Stdout = true

	a, err := app.Build(cfg, testLogger(io.Discard), io.Discard)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}

	ctx := t.Context()
	go func() { _ = a.Admin.Serve(ctx) }()

	// Wait for the admin server to become reachable.
	base := "http://" + addr
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err2 := http.Get(base + "/healthz")
		if err2 == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return base, a
}

// TestIntegration_AuthLimiter_LoopbackRateLimited confirms that repeated
// failed login attempts from 127.0.0.1 are still throttled.
func TestIntegration_AuthLimiter_LoopbackRateLimited(t *testing.T) {
	t.Parallel()

	const token = "a-very-long-admin-token-for-integration-testing-32c"
	base, _ := adminAddr(t, token)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for i := range 6 {
		form := url.Values{"token": {"wrong-token-" + fmt.Sprint(i)}}
		resp, err := client.PostForm(base+"/auth/login", form)
		if err != nil {
			t.Fatalf("POST /auth/login #%d: %v", i+1, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if i < 5 && resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("loopback IP was rate-limited before the bucket was exhausted at attempt %d", i+1)
		}
		if i == 5 && resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("attempt 6 status: got %d want 429", resp.StatusCode)
		}
	}
}

// TestIntegration_AuthLimiter_BearerFailureCounted verifies that failed bearer
// auth on a protected route increments the invalid_token counter visible via
// /metrics.
func TestIntegration_AuthLimiter_BearerFailureCounted(t *testing.T) {
	t.Parallel()

	const token = "a-very-long-admin-token-for-integration-testing-32d"
	base, _ := adminAddr(t, token)

	// Send a request with a wrong bearer token to a protected route.
	req, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", resp.StatusCode)
	}

	// Read /metrics and verify invalid_token counter is non-zero.
	mResp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(mResp.Body)
	mResp.Body.Close()

	bodyStr := string(body)
	// The counter must be present and > 0 (value should be "1").
	if !strings.Contains(bodyStr, `httpcatch_auth_failures_total{reason="invalid_token"} 1`) {
		t.Errorf("expected invalid_token counter = 1 in /metrics;\nbody:\n%s", bodyStr)
	}
}

// TestIntegration_CaptureBind_HonoredAtListen confirms that setting
// CaptureBind to a loopback address causes the capture port to listen on that
// address. The test uses port 0 so the OS assigns an ephemeral port.
func TestIntegration_CaptureBind_HonoredAtListen(t *testing.T) {
	t.Parallel()

	adminAddr := freeAddr(t)

	cfg := config.Defaults()
	cfg.Admin.Bind = adminAddr
	cfg.CaptureBind = "127.0.0.1:0"
	cfg.Sinks.Stdout = true

	var stdoutBuf syncBuffer
	var logBuf syncBuffer
	a, err := app.Build(cfg, testLogger(&logBuf), &stdoutBuf)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after context cancel")
		}
	}()

	waitPort(t, adminAddr, 3*time.Second)

	// Read the actual bound address from the log output, which emits
	// "capture port listening addr=<actual>" after net.Listen succeeds.
	var captureAddr string
	if !waitFor(func() bool {
		out := logBuf.String()
		for _, line := range strings.Split(out, "\n") {
			const marker = "addr="
			idx := strings.Index(line, marker)
			if idx >= 0 && strings.Contains(line, "capture port listening") {
				captureAddr = strings.TrimSpace(line[idx+len(marker):])
				// strip any trailing key=value fields (slog text format)
				if sp := strings.IndexByte(captureAddr, ' '); sp >= 0 {
					captureAddr = captureAddr[:sp]
				}
				return true
			}
		}
		return false
	}, 3*time.Second) {
		t.Fatalf("timed out waiting for capture port address in logs:\n%s", logBuf.String())
	}

	resp, err := http.Post("http://"+captureAddr+"/bind-test", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("POST to capture: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("capture status: got %d want 202", resp.StatusCode)
	}

	if !waitFor(func() bool { return stdoutBuf.CountLines() >= 1 }, 5*time.Second) {
		t.Fatal("timed out waiting for captured record in stdout")
	}
}

// TestIntegration_AuthLimiter_MetricsExposed confirms that /metrics always
// emits both reason= series for httpcatch_auth_failures_total.
func TestIntegration_AuthLimiter_MetricsExposed(t *testing.T) {
	t.Parallel()

	const token = "a-very-long-admin-token-for-integration-testing-32e"
	base, _ := adminAddr(t, token)

	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	bodyStr := string(body)
	checks := []string{
		`# HELP httpcatch_auth_failures_total`,
		`# TYPE httpcatch_auth_failures_total counter`,
		`httpcatch_auth_failures_total{reason="invalid_token"}`,
		`httpcatch_auth_failures_total{reason="rate_limited"}`,
	}
	for _, want := range checks {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("body missing %q\nbody:\n%s", want, bodyStr)
		}
	}
}

// TestIntegration_CSRF_LogoutCrossSite_Blocked_SessionSurvives verifies that a
// cross-site POST to /auth/logout is rejected with 403 and that the session
// cookie remains valid. It also checks that the csrf_blocked counter appears in
// /metrics.
func TestIntegration_CSRF_LogoutCrossSite_Blocked_SessionSurvives(t *testing.T) {
	t.Parallel()

	const token = "a-very-long-admin-token-for-integration-testing-csrf1"
	base, _ := adminAddr(t, token)

	noFollow := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Obtain a session by logging in with the correct token.
	form := url.Values{"token": {token}}
	loginResp, err := noFollow.PostForm(base+"/auth/login", form)
	if err != nil {
		t.Fatalf("POST /auth/login: %v", err)
	}
	io.Copy(io.Discard, loginResp.Body)
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status: got %d want 303", loginResp.StatusCode)
	}

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

	// Attempt a cross-site logout; the CSRF middleware must reject it.
	logoutReq, _ := http.NewRequest(http.MethodPost, base+"/auth/logout", nil)
	logoutReq.AddCookie(sessionCookie)
	logoutReq.Header.Set("Sec-Fetch-Site", "cross-site")
	logoutResp, err := noFollow.Do(logoutReq)
	if err != nil {
		t.Fatalf("POST /auth/logout (cross-site): %v", err)
	}
	io.Copy(io.Discard, logoutResp.Body)
	logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site logout status: got %d want 403", logoutResp.StatusCode)
	}

	// Session must still be valid — the logout was blocked before it could revoke.
	checkReq, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	checkReq.AddCookie(sessionCookie)
	checkResp, err := noFollow.Do(checkReq)
	if err != nil {
		t.Fatalf("GET /status after blocked logout: %v", err)
	}
	io.Copy(io.Discard, checkResp.Body)
	checkResp.Body.Close()
	if checkResp.StatusCode != http.StatusOK {
		t.Errorf("session after blocked logout: got %d want 200", checkResp.StatusCode)
	}

	// The csrf_blocked counter must be visible in /metrics.
	mResp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	mBody, _ := io.ReadAll(mResp.Body)
	mResp.Body.Close()

	if !strings.Contains(string(mBody), `httpcatch_auth_failures_total{reason="csrf_blocked"} 1`) {
		t.Errorf("expected csrf_blocked counter = 1 in /metrics;\nbody:\n%s", string(mBody))
	}
}
