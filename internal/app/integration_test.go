package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/app"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/sinks"
)

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
	a := app.Build(cfg, testLogger(logBuf), stdoutBuf, extras...)
	a.EmitStartupWarnings()

	ts := httptest.NewServer(a.Handler)
	ctx, cancel := context.WithCancel(context.Background())
	a.Workers.Start(ctx)

	teardown := func() {
		ts.Close()
		a.Queue.Close()
		a.Workers.Wait()
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

func decodeLines(s string) ([]capture.CapturedRecord, error) {
	out := []capture.CapturedRecord{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var r capture.CapturedRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("line %q: %w", line, err)
		}
		out = append(out, r)
	}
	return out, nil
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
			wantOrig: cap + 512, wantTrunc: true, wantCapped: cap,
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

	byMarker := map[string]capture.CapturedRecord{}
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
func (s *slowSink) Write(ctx context.Context, r *capture.CapturedRecord) error {
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := fire(t, ts.URL+"/drop", "POST", []byte("payload"), nil)
			if resp.StatusCode == http.StatusAccepted {
				ok202.Add(1)
			}
		}()
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
func (failingSink) Write(context.Context, *capture.CapturedRecord) error {
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
	a := app.Build(cfg, testLogger(&logBuf), io.Discard)
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

	byMarker := map[string]capture.CapturedRecord{}
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

	memRecords := a.Memory.Recent(fired)
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

	stdoutByID := make(map[string]capture.CapturedRecord, len(stdoutRecords))
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
	memRecords := a.Memory.Recent(1)
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
	a := app.Build(cfg, testLogger(&logBuf), io.Discard)
	a.EmitStartupWarnings()

	if !strings.Contains(logBuf.String(), "unredacted") {
		t.Errorf("expected unredacted warning even with a sink enabled, got:\n%s", logBuf.String())
	}
	if strings.Contains(strings.ToLower(logBuf.String()), "zero sinks") {
		t.Errorf("zero-sinks warning should not fire when stdout is enabled")
	}
}
