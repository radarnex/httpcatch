// Package metrics owns the Prometheus registry for httpcatch. It bridges the
// process's plain atomic counters and sampled gauges into Prometheus metrics via
// a custom collector read at scrape time, owns the pipeline processing-duration
// histogram, and registers the Go runtime and process collectors. The HTTP
// handler it produces is mounted unauthenticated at /metrics by the admin server.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Sources holds the live accessors for every httpcatch-owned metric. Each
// function is read at scrape time so the exposition always reflects the current
// value; the capture, pipeline, admin, and redact subsystems own the underlying
// atomics. A nil accessor is treated as zero.
type Sources struct {
	CapturedTotal                   func() uint64
	DroppedTotal                    func() uint64
	CaptureErrorsTotal              func() uint64
	CapturedWithoutCorrelationTotal func() uint64
	CapturedWithoutServiceTotal     func() uint64
	RedactionErrorsTotal            func() uint64
	WorkerPanicsTotal               func() uint64

	EventsIngestedResponseTotal func() uint64
	EventsIngestedOutboundTotal func() uint64

	EventsRejectedInvalidJSONTotal          func() uint64
	EventsRejectedPayloadTooLargeTotal      func() uint64
	EventsRejectedUnknownTypeTotal          func() uint64
	EventsRejectedMissingTypeTotal          func() uint64
	EventsRejectedMissingRequiredFieldTotal func() uint64
	EventsRejectedEmptyBatchTotal           func() uint64
	EventsRejectedBatchTooLargeTotal        func() uint64
	EventsRejectedBodyTooLargeTotal         func() uint64
	EventsDroppedQueueFullTotal             func() uint64

	// OrphansResponse and OrphansOutbound are gauges sampled at scrape time:
	// the current count of orphan events of each type visible in the configured
	// store (memory or sqlite).
	OrphansResponse func() int
	OrphansOutbound func() int

	AuthFailuresInvalidTokenTotal func() uint64
	AuthFailuresRateLimitedTotal  func() uint64
	AuthFailuresCSRFBlockedTotal  func() uint64

	SinkWriteErrorsMemoryTotal func() uint64
	SinkWriteErrorsSQLiteTotal func() uint64
	SinkWriteErrorsStdoutTotal func() uint64

	// Version and BuildTime label the httpcatch_build_info gauge.
	Version   string
	BuildTime string
}

// ProcessingBuckets returns the histogram bounds (seconds) for the pipeline
// processing-duration metric, tuned for redaction plus sink writes.
func ProcessingBuckets() []float64 {
	return []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}
}

// Prom owns a dedicated registry, the bridging collector, the pipeline
// processing-duration histogram, and the Go and process runtime collectors.
type Prom struct {
	registry   *prometheus.Registry
	processing prometheus.Histogram
}

// New builds the registry and registers the bridging collector, the processing
// histogram, and the Go and process collectors.
func New(src Sources) *Prom {
	reg := prometheus.NewRegistry()
	processing := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "httpcatch_pipeline_processing_duration_seconds",
		Help:    "Seconds spent redacting a captured record and writing it to all configured sinks.",
		Buckets: ProcessingBuckets(),
	})
	reg.MustRegister(
		newCollector(src),
		processing,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return &Prom{registry: reg, processing: processing}
}

// Handler serves the Prometheus text exposition for this registry. It is mounted
// unauthenticated at /metrics.
func (p *Prom) Handler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}

// ProcessingObserver returns the Observer the worker pool records per-record
// processing duration into.
func (p *Prom) ProcessingObserver() prometheus.Observer {
	return p.processing
}

// collector bridges the func-sourced counters and gauges into Prometheus
// metrics. Every descriptor is emitted on each scrape so absent traffic reads as
// an explicit zero rather than a missing series.
type collector struct {
	src Sources

	captured               *prometheus.Desc
	dropped                *prometheus.Desc
	captureErrors          *prometheus.Desc
	withoutCorrelation     *prometheus.Desc
	withoutService         *prometheus.Desc
	redactionErrors        *prometheus.Desc
	workerPanics           *prometheus.Desc
	eventsIngested         *prometheus.Desc
	eventsRejected         *prometheus.Desc
	eventsDroppedQueueFull *prometheus.Desc
	orphans                *prometheus.Desc
	authFailures           *prometheus.Desc
	sinkWriteErrors        *prometheus.Desc
	buildInfo              *prometheus.Desc
}

func newCollector(src Sources) *collector {
	return &collector{
		src: src,
		captured: prometheus.NewDesc("httpcatch_captured_total",
			"Total requests successfully enqueued to the capture queue by the capture endpoint.", nil, nil),
		dropped: prometheus.NewDesc("httpcatch_dropped_total",
			"Total records dropped because the capture queue was full.", nil, nil),
		captureErrors: prometheus.NewDesc("httpcatch_capture_errors_total",
			"Total capture requests dropped before enqueue because the request body could not be read.", nil, nil),
		withoutCorrelation: prometheus.NewDesc("httpcatch_captured_without_correlation_total",
			"Total captured records with a synthesized correlation id (no traceparent or X-Request-ID).", nil, nil),
		withoutService: prometheus.NewDesc("httpcatch_captured_without_service_total",
			"Total captured records whose service fallback chain bottomed out at \"unknown\" (no configured service header and no Host header).", nil, nil),
		redactionErrors: prometheus.NewDesc("httpcatch_redaction_errors_total",
			"Total best-effort redaction failures (counter ticks on unparseable JSON or sjson write failure).", nil, nil),
		workerPanics: prometheus.NewDesc("httpcatch_worker_panics_total",
			"Total panics recovered by the pipeline worker pool; the worker continues after each.", nil, nil),
		eventsIngested: prometheus.NewDesc("httpcatch_events_ingested_total",
			"Total events successfully enqueued via the Events API.", []string{"type"}, nil),
		eventsRejected: prometheus.NewDesc("httpcatch_events_rejected_total",
			"Total events rejected by the Events API, by reason.", []string{"reason"}, nil),
		eventsDroppedQueueFull: prometheus.NewDesc("httpcatch_events_dropped_queue_full_total",
			"Total events dropped because the queue was full at the time of enqueue, as reported by the Events API.", nil, nil),
		orphans: prometheus.NewDesc("httpcatch_orphans",
			"Current count of orphan events (no matching captured request in the store), sampled at scrape time.", []string{"type"}, nil),
		authFailures: prometheus.NewDesc("httpcatch_auth_failures_total",
			"Total admin auth failures, by reason.", []string{"reason"}, nil),
		sinkWriteErrors: prometheus.NewDesc("httpcatch_sink_write_errors_total",
			"Total sink write failures, by sink.", []string{"sink"}, nil),
		buildInfo: prometheus.NewDesc("httpcatch_build_info",
			"Build identity for the running binary. Always value 1.", []string{"version", "build_time"}, nil),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.captured
	ch <- c.dropped
	ch <- c.captureErrors
	ch <- c.withoutCorrelation
	ch <- c.withoutService
	ch <- c.redactionErrors
	ch <- c.workerPanics
	ch <- c.eventsIngested
	ch <- c.eventsRejected
	ch <- c.eventsDroppedQueueFull
	ch <- c.orphans
	ch <- c.authFailures
	ch <- c.sinkWriteErrors
	ch <- c.buildInfo
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	counter := func(d *prometheus.Desc, f func() uint64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, countUint(f), labels...)
	}
	gauge := func(d *prometheus.Desc, f func() int, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, countInt(f), labels...)
	}

	counter(c.captured, c.src.CapturedTotal)
	counter(c.dropped, c.src.DroppedTotal)
	counter(c.captureErrors, c.src.CaptureErrorsTotal)
	counter(c.withoutCorrelation, c.src.CapturedWithoutCorrelationTotal)
	counter(c.withoutService, c.src.CapturedWithoutServiceTotal)
	counter(c.redactionErrors, c.src.RedactionErrorsTotal)
	counter(c.workerPanics, c.src.WorkerPanicsTotal)

	counter(c.eventsIngested, c.src.EventsIngestedResponseTotal, "response")
	counter(c.eventsIngested, c.src.EventsIngestedOutboundTotal, "outbound")

	counter(c.eventsRejected, c.src.EventsRejectedInvalidJSONTotal, "invalid_json")
	counter(c.eventsRejected, c.src.EventsRejectedPayloadTooLargeTotal, "payload_too_large")
	counter(c.eventsRejected, c.src.EventsRejectedUnknownTypeTotal, "unknown_type")
	counter(c.eventsRejected, c.src.EventsRejectedMissingTypeTotal, "missing_type")
	counter(c.eventsRejected, c.src.EventsRejectedMissingRequiredFieldTotal, "missing_required_field")
	counter(c.eventsRejected, c.src.EventsRejectedEmptyBatchTotal, "empty_batch")
	counter(c.eventsRejected, c.src.EventsRejectedBatchTooLargeTotal, "batch_too_large")
	counter(c.eventsRejected, c.src.EventsRejectedBodyTooLargeTotal, "body_too_large")

	counter(c.eventsDroppedQueueFull, c.src.EventsDroppedQueueFullTotal)

	gauge(c.orphans, c.src.OrphansResponse, "response")
	gauge(c.orphans, c.src.OrphansOutbound, "outbound")

	counter(c.authFailures, c.src.AuthFailuresInvalidTokenTotal, "invalid_token")
	counter(c.authFailures, c.src.AuthFailuresRateLimitedTotal, "rate_limited")
	counter(c.authFailures, c.src.AuthFailuresCSRFBlockedTotal, "csrf_blocked")

	counter(c.sinkWriteErrors, c.src.SinkWriteErrorsMemoryTotal, "memory")
	counter(c.sinkWriteErrors, c.src.SinkWriteErrorsSQLiteTotal, "sqlite")
	counter(c.sinkWriteErrors, c.src.SinkWriteErrorsStdoutTotal, "stdout")

	ch <- prometheus.MustNewConstMetric(c.buildInfo, prometheus.GaugeValue, 1, c.src.Version, c.src.BuildTime)
}

func countUint(f func() uint64) float64 {
	if f == nil {
		return 0
	}
	return float64(f())
}

func countInt(f func() int) float64 {
	if f == nil {
		return 0
	}
	return float64(f())
}
