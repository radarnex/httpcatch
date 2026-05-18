package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

// syncBuffer wraps bytes.Buffer with a mutex so the worker can write while
// the test goroutine polls via String/Count.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) CountLines() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), "\n")
}

func testLogger(buf io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// runPipeline boots the app's components against an httptest server and returns
// a teardown that closes the queue and waits for the worker pool to drain.
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

// waitFor polls until cond returns true or the deadline expires.
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
		marker      string
		bodyLen     int
		wantOrig    int
		wantTrunc   bool
		wantCapped  int
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
		return strings.Count(stdoutBuf.String(), "\n") >= len(cases)
	}, 5*time.Second) {
		t.Fatalf("timed out waiting for %d records; got %d lines",
			len(cases), strings.Count(stdoutBuf.String(), "\n"))
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
		if r.Service != capture.PlaceholderService {
			t.Errorf("%s: service got %q want %q", c.marker, r.Service, capture.PlaceholderService)
		}
		if r.ServiceSource != capture.PlaceholderServiceSource {
			t.Errorf("%s: service_source got %q want %q", c.marker, r.ServiceSource, capture.PlaceholderServiceSource)
		}
		if r.CorrelationSource != capture.PlaceholderCorrelationSource {
			t.Errorf("%s: correlation_source got %q want %q", c.marker, r.CorrelationSource, capture.PlaceholderCorrelationSource)
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

// slowSink wraps another sink and sleeps before delegating.
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
	cfg.Sinks.Stdout = false // no built-in stdout; we'll attach a slow one as extra

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

// failingSink errors on every write.
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
