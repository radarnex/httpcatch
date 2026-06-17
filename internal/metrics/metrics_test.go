package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestProcessingBuckets verifies the exact bucket values and that they are
// strictly ascending.
func TestProcessingBuckets(t *testing.T) {
	t.Parallel()

	want := []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}
	got := ProcessingBuckets()

	if len(got) != len(want) {
		t.Fatalf("ProcessingBuckets len: got %d, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("bucket[%d]: got %v, want %v", i, got[i], v)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("bucket[%d]=%v is not greater than bucket[%d]=%v (not strictly ascending)", i, got[i], i-1, got[i-1])
		}
	}
}

// TestNew_AllNilSources checks that constructing a Prom with all nil Sources
// accessors does not panic and that Handler() serves HTTP 200. Every
// httpcatch counter and gauge must appear as an explicit zero, and
// httpcatch_build_info must render with empty labels at value 1.
func TestNew_AllNilSources(t *testing.T) {
	t.Parallel()

	p := New(Sources{})

	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status: got %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Flat counters must be present at zero.
	flatZeros := []string{
		`httpcatch_captured_total 0`,
		`httpcatch_dropped_total 0`,
		`httpcatch_capture_errors_total 0`,
		`httpcatch_captured_without_correlation_total 0`,
		`httpcatch_captured_without_service_total 0`,
		`httpcatch_redaction_errors_total 0`,
		`httpcatch_worker_panics_total 0`,
		`httpcatch_events_dropped_queue_full_total 0`,
	}
	for _, line := range flatZeros {
		if !strings.Contains(body, line) {
			t.Errorf("missing zero series: %q", line)
		}
	}

	// Labeled counters — ingested.
	ingestedZeros := []string{
		`httpcatch_events_ingested_total{type="outbound"} 0`,
		`httpcatch_events_ingested_total{type="response"} 0`,
	}
	for _, line := range ingestedZeros {
		if !strings.Contains(body, line) {
			t.Errorf("missing zero series: %q", line)
		}
	}

	// Rejected reasons — all 8.
	rejectedZeros := []string{
		`httpcatch_events_rejected_total{reason="batch_too_large"} 0`,
		`httpcatch_events_rejected_total{reason="body_too_large"} 0`,
		`httpcatch_events_rejected_total{reason="empty_batch"} 0`,
		`httpcatch_events_rejected_total{reason="invalid_json"} 0`,
		`httpcatch_events_rejected_total{reason="missing_required_field"} 0`,
		`httpcatch_events_rejected_total{reason="missing_type"} 0`,
		`httpcatch_events_rejected_total{reason="payload_too_large"} 0`,
		`httpcatch_events_rejected_total{reason="unknown_type"} 0`,
	}
	for _, line := range rejectedZeros {
		if !strings.Contains(body, line) {
			t.Errorf("missing zero series: %q", line)
		}
	}

	// Auth failure reasons.
	authZeros := []string{
		`httpcatch_auth_failures_total{reason="csrf_blocked"} 0`,
		`httpcatch_auth_failures_total{reason="invalid_token"} 0`,
		`httpcatch_auth_failures_total{reason="rate_limited"} 0`,
	}
	for _, line := range authZeros {
		if !strings.Contains(body, line) {
			t.Errorf("missing zero series: %q", line)
		}
	}

	// Sink write errors.
	sinkZeros := []string{
		`httpcatch_sink_write_errors_total{sink="memory"} 0`,
		`httpcatch_sink_write_errors_total{sink="sqlite"} 0`,
		`httpcatch_sink_write_errors_total{sink="stdout"} 0`,
	}
	for _, line := range sinkZeros {
		if !strings.Contains(body, line) {
			t.Errorf("missing zero series: %q", line)
		}
	}

	// Orphan gauges (zero int = zero float).
	orphanZeros := []string{
		`httpcatch_orphans{type="outbound"} 0`,
		`httpcatch_orphans{type="response"} 0`,
	}
	for _, line := range orphanZeros {
		if !strings.Contains(body, line) {
			t.Errorf("missing zero series: %q", line)
		}
	}

	// Build info with empty labels, value 1.
	if !strings.Contains(body, `httpcatch_build_info{build_time="",version=""} 1`) {
		t.Errorf("missing build_info zero line; body snippet: %q", body[:min(500, len(body))])
	}
}

// TestCollector_ValueMapping builds a Sources with a distinct non-zero value
// for every accessor and asserts that each metric is emitted with the correct
// value and label set. Using distinct numbers catches mis-wiring.
func TestCollector_ValueMapping(t *testing.T) {
	t.Parallel()

	src := Sources{
		CapturedTotal:                           func() uint64 { return 1 },
		DroppedTotal:                            func() uint64 { return 2 },
		CaptureErrorsTotal:                      func() uint64 { return 3 },
		CapturedWithoutCorrelationTotal:         func() uint64 { return 4 },
		CapturedWithoutServiceTotal:             func() uint64 { return 5 },
		RedactionErrorsTotal:                    func() uint64 { return 6 },
		WorkerPanicsTotal:                       func() uint64 { return 7 },
		EventsIngestedResponseTotal:             func() uint64 { return 8 },
		EventsIngestedOutboundTotal:             func() uint64 { return 9 },
		EventsRejectedInvalidJSONTotal:          func() uint64 { return 10 },
		EventsRejectedPayloadTooLargeTotal:      func() uint64 { return 11 },
		EventsRejectedUnknownTypeTotal:          func() uint64 { return 12 },
		EventsRejectedMissingTypeTotal:          func() uint64 { return 13 },
		EventsRejectedMissingRequiredFieldTotal: func() uint64 { return 14 },
		EventsRejectedEmptyBatchTotal:           func() uint64 { return 15 },
		EventsRejectedBatchTooLargeTotal:        func() uint64 { return 16 },
		EventsRejectedBodyTooLargeTotal:         func() uint64 { return 17 },
		EventsDroppedQueueFullTotal:             func() uint64 { return 18 },
		OrphansResponse:                         func() int { return 19 },
		OrphansOutbound:                         func() int { return 20 },
		AuthFailuresInvalidTokenTotal:           func() uint64 { return 21 },
		AuthFailuresRateLimitedTotal:            func() uint64 { return 22 },
		AuthFailuresCSRFBlockedTotal:            func() uint64 { return 23 },
		SinkWriteErrorsMemoryTotal:              func() uint64 { return 24 },
		SinkWriteErrorsSQLiteTotal:              func() uint64 { return 25 },
		SinkWriteErrorsStdoutTotal:              func() uint64 { return 26 },
		Version:                                 "v1.2.3",
		BuildTime:                               "2025-01-02T03:04:05Z",
	}

	c := newCollector(src)

	expected := `
# HELP httpcatch_captured_total Total requests successfully enqueued to the capture queue by the capture endpoint.
# TYPE httpcatch_captured_total counter
httpcatch_captured_total 1
# HELP httpcatch_dropped_total Total records dropped because the capture queue was full.
# TYPE httpcatch_dropped_total counter
httpcatch_dropped_total 2
# HELP httpcatch_capture_errors_total Total capture requests dropped before enqueue because the request body could not be read.
# TYPE httpcatch_capture_errors_total counter
httpcatch_capture_errors_total 3
# HELP httpcatch_captured_without_correlation_total Total captured records with a synthesized correlation id (no traceparent or X-Request-ID).
# TYPE httpcatch_captured_without_correlation_total counter
httpcatch_captured_without_correlation_total 4
# HELP httpcatch_captured_without_service_total Total captured records whose service fallback chain bottomed out at "unknown" (no configured service header and no Host header).
# TYPE httpcatch_captured_without_service_total counter
httpcatch_captured_without_service_total 5
# HELP httpcatch_redaction_errors_total Total best-effort redaction failures (counter ticks on unparseable JSON or sjson write failure).
# TYPE httpcatch_redaction_errors_total counter
httpcatch_redaction_errors_total 6
# HELP httpcatch_worker_panics_total Total panics recovered by the pipeline worker pool; the worker continues after each.
# TYPE httpcatch_worker_panics_total counter
httpcatch_worker_panics_total 7
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"httpcatch_captured_total",
		"httpcatch_dropped_total",
		"httpcatch_capture_errors_total",
		"httpcatch_captured_without_correlation_total",
		"httpcatch_captured_without_service_total",
		"httpcatch_redaction_errors_total",
		"httpcatch_worker_panics_total",
	); err != nil {
		t.Errorf("flat counter mismatch: %v", err)
	}

	expectedIngested := `
# HELP httpcatch_events_ingested_total Total events successfully enqueued via the Events API.
# TYPE httpcatch_events_ingested_total counter
httpcatch_events_ingested_total{type="outbound"} 9
httpcatch_events_ingested_total{type="response"} 8
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedIngested),
		"httpcatch_events_ingested_total",
	); err != nil {
		t.Errorf("events_ingested mismatch: %v", err)
	}

	expectedRejected := `
# HELP httpcatch_events_rejected_total Total events rejected by the Events API, by reason.
# TYPE httpcatch_events_rejected_total counter
httpcatch_events_rejected_total{reason="batch_too_large"} 16
httpcatch_events_rejected_total{reason="body_too_large"} 17
httpcatch_events_rejected_total{reason="empty_batch"} 15
httpcatch_events_rejected_total{reason="invalid_json"} 10
httpcatch_events_rejected_total{reason="missing_required_field"} 14
httpcatch_events_rejected_total{reason="missing_type"} 13
httpcatch_events_rejected_total{reason="payload_too_large"} 11
httpcatch_events_rejected_total{reason="unknown_type"} 12
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedRejected),
		"httpcatch_events_rejected_total",
	); err != nil {
		t.Errorf("events_rejected mismatch: %v", err)
	}

	expectedDropped := `
# HELP httpcatch_events_dropped_queue_full_total Total events dropped because the queue was full at the time of enqueue, as reported by the Events API.
# TYPE httpcatch_events_dropped_queue_full_total counter
httpcatch_events_dropped_queue_full_total 18
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedDropped),
		"httpcatch_events_dropped_queue_full_total",
	); err != nil {
		t.Errorf("events_dropped_queue_full mismatch: %v", err)
	}

	expectedOrphans := `
# HELP httpcatch_orphans Current count of orphan events (no matching captured request in the store), sampled at scrape time.
# TYPE httpcatch_orphans gauge
httpcatch_orphans{type="outbound"} 20
httpcatch_orphans{type="response"} 19
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedOrphans),
		"httpcatch_orphans",
	); err != nil {
		t.Errorf("orphans mismatch: %v", err)
	}

	expectedAuth := `
# HELP httpcatch_auth_failures_total Total admin auth failures, by reason.
# TYPE httpcatch_auth_failures_total counter
httpcatch_auth_failures_total{reason="csrf_blocked"} 23
httpcatch_auth_failures_total{reason="invalid_token"} 21
httpcatch_auth_failures_total{reason="rate_limited"} 22
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedAuth),
		"httpcatch_auth_failures_total",
	); err != nil {
		t.Errorf("auth_failures mismatch: %v", err)
	}

	expectedSink := `
# HELP httpcatch_sink_write_errors_total Total sink write failures, by sink.
# TYPE httpcatch_sink_write_errors_total counter
httpcatch_sink_write_errors_total{sink="memory"} 24
httpcatch_sink_write_errors_total{sink="sqlite"} 25
httpcatch_sink_write_errors_total{sink="stdout"} 26
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedSink),
		"httpcatch_sink_write_errors_total",
	); err != nil {
		t.Errorf("sink_write_errors mismatch: %v", err)
	}

	expectedBuildInfo := `
# HELP httpcatch_build_info Build identity for the running binary. Always value 1.
# TYPE httpcatch_build_info gauge
httpcatch_build_info{build_time="2025-01-02T03:04:05Z",version="v1.2.3"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedBuildInfo),
		"httpcatch_build_info",
	); err != nil {
		t.Errorf("build_info mismatch: %v", err)
	}
}

// TestMetricMetadata checks that the expected # HELP and # TYPE lines appear
// in the scrape output, verifying gauge/counter/histogram type assignments and
// that httpcatch_orphans is a gauge (not a counter and not named _total).
func TestMetricMetadata(t *testing.T) {
	t.Parallel()

	p := New(Sources{})
	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	p.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	gauges := []string{
		"# TYPE httpcatch_orphans gauge",
		"# TYPE httpcatch_build_info gauge",
	}
	for _, line := range gauges {
		if !strings.Contains(body, line) {
			t.Errorf("missing gauge TYPE line: %q", line)
		}
	}

	counters := []string{
		"# TYPE httpcatch_captured_total counter",
		"# TYPE httpcatch_dropped_total counter",
		"# TYPE httpcatch_capture_errors_total counter",
		"# TYPE httpcatch_captured_without_correlation_total counter",
		"# TYPE httpcatch_captured_without_service_total counter",
		"# TYPE httpcatch_redaction_errors_total counter",
		"# TYPE httpcatch_worker_panics_total counter",
		"# TYPE httpcatch_events_ingested_total counter",
		"# TYPE httpcatch_events_rejected_total counter",
		"# TYPE httpcatch_events_dropped_queue_full_total counter",
		"# TYPE httpcatch_auth_failures_total counter",
		"# TYPE httpcatch_sink_write_errors_total counter",
	}
	for _, line := range counters {
		if !strings.Contains(body, line) {
			t.Errorf("missing counter TYPE line: %q", line)
		}
	}

	// The processing duration metric must be a histogram.
	if !strings.Contains(body, "# TYPE httpcatch_pipeline_processing_duration_seconds histogram") {
		t.Errorf("missing histogram TYPE line for processing duration")
	}
	if !strings.Contains(body, "# HELP httpcatch_pipeline_processing_duration_seconds") {
		t.Errorf("missing HELP line for processing duration")
	}

	// httpcatch_orphans must NOT be named _total (it is a gauge, not a counter).
	if strings.Contains(body, "httpcatch_orphans_total") {
		t.Errorf("httpcatch_orphans must not have _total suffix (it is a gauge)")
	}
}

// TestProcessingObserver_BucketAccumulation observes two durations that land
// in distinct buckets and verifies cumulative counts, _count, _sum, and +Inf.
func TestProcessingObserver_BucketAccumulation(t *testing.T) {
	t.Parallel()

	p := New(Sources{})
	obs := p.ProcessingObserver()

	// 0.0002s falls into le="0.00025" (the second bucket).
	obs.Observe(0.0002)
	// 0.02s falls into le="0.025" (the tenth bucket).
	obs.Observe(0.02)

	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	p.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	// _count must be 2 (total observations).
	if !strings.Contains(body, "httpcatch_pipeline_processing_duration_seconds_count 2") {
		t.Errorf("expected _count 2; body snippet:\n%s", excerptLines(body, "httpcatch_pipeline_processing_duration_seconds"))
	}

	// le="0.00025" catches 0.0002 → cumulative count 1.
	if !strings.Contains(body, `httpcatch_pipeline_processing_duration_seconds_bucket{le="0.00025"} 1`) {
		t.Errorf("expected le=0.00025 bucket count 1")
	}

	// le="0.025" catches both observations → cumulative count 2.
	if !strings.Contains(body, `httpcatch_pipeline_processing_duration_seconds_bucket{le="0.025"} 2`) {
		t.Errorf("expected le=0.025 bucket count 2")
	}

	// le="+Inf" must equal _count.
	if !strings.Contains(body, `httpcatch_pipeline_processing_duration_seconds_bucket{le="+Inf"} 2`) {
		t.Errorf("expected le=+Inf count 2")
	}
}

// TestProcessingObserver_ZeroObservations verifies that before any Observe
// call the histogram emits _count 0 and le="+Inf" 0.
func TestProcessingObserver_ZeroObservations(t *testing.T) {
	t.Parallel()

	p := New(Sources{})

	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	p.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "httpcatch_pipeline_processing_duration_seconds_count 0") {
		t.Errorf("expected _count 0 with no observations")
	}
	if !strings.Contains(body, `httpcatch_pipeline_processing_duration_seconds_bucket{le="+Inf"} 0`) {
		t.Errorf("expected le=+Inf 0 with no observations")
	}
}

// TestGoCollectorWired verifies that the Go runtime collector is registered so
// that go_goroutines appears in the scrape output.
func TestGoCollectorWired(t *testing.T) {
	t.Parallel()

	p := New(Sources{})
	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	p.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "go_goroutines") {
		t.Errorf("go_goroutines not found in exposition; Go collector may not be registered")
	}
}

// TestBuildInfoLabelOrdering asserts the exact text-format line for
// httpcatch_build_info. client_golang sorts labels alphabetically, so
// build_time must appear before version in the output.
func TestBuildInfoLabelOrdering(t *testing.T) {
	t.Parallel()

	src := Sources{
		Version:   "v9.8.7",
		BuildTime: "2024-06-01T12:00:00Z",
	}
	c := newCollector(src)

	expected := `
# HELP httpcatch_build_info Build identity for the running binary. Always value 1.
# TYPE httpcatch_build_info gauge
httpcatch_build_info{build_time="2024-06-01T12:00:00Z",version="v9.8.7"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "httpcatch_build_info"); err != nil {
		t.Errorf("build_info label ordering mismatch: %v", err)
	}
}

// TestHandlerContentType checks that the handler's Content-Type header begins
// with the expected Prometheus text-format prefix.
func TestHandlerContentType(t *testing.T) {
	t.Parallel()

	p := New(Sources{})
	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	p.Handler().ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	const wantPrefix = "text/plain; version=0.0.4"
	if !strings.HasPrefix(ct, wantPrefix) {
		t.Errorf("Content-Type %q does not begin with %q", ct, wantPrefix)
	}
}

// TestNilSources_NoPanic verifies that all accessor calls with nil functions
// return zero and do not panic, exercising countUint and countInt directly
// via the collector.
func TestNilSources_NoPanic(t *testing.T) {
	t.Parallel()

	c := newCollector(Sources{})

	expected := `
# HELP httpcatch_captured_total Total requests successfully enqueued to the capture queue by the capture endpoint.
# TYPE httpcatch_captured_total counter
httpcatch_captured_total 0
# HELP httpcatch_dropped_total Total records dropped because the capture queue was full.
# TYPE httpcatch_dropped_total counter
httpcatch_dropped_total 0
# HELP httpcatch_events_dropped_queue_full_total Total events dropped because the queue was full at the time of enqueue, as reported by the Events API.
# TYPE httpcatch_events_dropped_queue_full_total counter
httpcatch_events_dropped_queue_full_total 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"httpcatch_captured_total",
		"httpcatch_dropped_total",
		"httpcatch_events_dropped_queue_full_total",
	); err != nil {
		t.Errorf("nil-source zero values: %v", err)
	}
}

// excerptLines returns all lines from s that contain substr, for concise
// diagnostic output in test failures.
func excerptLines(s, substr string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
