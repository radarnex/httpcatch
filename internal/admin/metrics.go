package admin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/radarnex/httpcatch/internal/buildinfo"
)

// MetricSources holds the counter accessors and the unredacted signal wired in
// at construction time. Each field is a zero-argument function so handlers
// read the live value on every request without caching. Nil fields are replaced
// by safe zero-returning functions in normalize().
type MetricSources struct {
	DroppedTotal                    func() uint64
	CapturedWithoutCorrelationTotal func() uint64
	CapturedWithoutServiceTotal     func() uint64
	RedactionErrorsTotal            func() uint64
	// Unredacted reports whether the redaction ruleset has no rules configured.
	// When true, the UI shows a prominent banner warning the operator.
	Unredacted func() bool

	// Events API counters.
	EventsIngestedResponseTotal             func() uint64
	EventsIngestedOutboundTotal             func() uint64
	EventsRejectedInvalidJSONTotal          func() uint64
	EventsRejectedPayloadTooLargeTotal      func() uint64
	EventsRejectedUnknownTypeTotal          func() uint64
	EventsRejectedMissingTypeTotal          func() uint64
	EventsRejectedMissingRequiredFieldTotal func() uint64
	EventsRejectedEmptyBatchTotal           func() uint64

	// OrphansResponse and OrphansOutbound are gauges sampled at scrape time.
	// Each returns the current count of orphan events of the respective type
	// visible in the configured store (memory or sqlite). A nil function is
	// treated as returning 0.
	OrphansResponse func() int
	OrphansOutbound func() int
}

// coalesce returns f if non-nil, otherwise a function that always returns zero.
func coalesce[T any](f func() T) func() T {
	if f != nil {
		return f
	}
	var zero T
	return func() T { return zero }
}

// normalize replaces any nil function fields with safe zero-returning defaults
// so that handlers never need nil checks.
func (s *MetricSources) normalize() {
	s.DroppedTotal = coalesce(s.DroppedTotal)
	s.CapturedWithoutCorrelationTotal = coalesce(s.CapturedWithoutCorrelationTotal)
	s.CapturedWithoutServiceTotal = coalesce(s.CapturedWithoutServiceTotal)
	s.RedactionErrorsTotal = coalesce(s.RedactionErrorsTotal)
	s.Unredacted = coalesce(s.Unredacted)
	s.EventsIngestedResponseTotal = coalesce(s.EventsIngestedResponseTotal)
	s.EventsIngestedOutboundTotal = coalesce(s.EventsIngestedOutboundTotal)
	s.EventsRejectedInvalidJSONTotal = coalesce(s.EventsRejectedInvalidJSONTotal)
	s.EventsRejectedPayloadTooLargeTotal = coalesce(s.EventsRejectedPayloadTooLargeTotal)
	s.EventsRejectedUnknownTypeTotal = coalesce(s.EventsRejectedUnknownTypeTotal)
	s.EventsRejectedMissingTypeTotal = coalesce(s.EventsRejectedMissingTypeTotal)
	s.EventsRejectedMissingRequiredFieldTotal = coalesce(s.EventsRejectedMissingRequiredFieldTotal)
	s.EventsRejectedEmptyBatchTotal = coalesce(s.EventsRejectedEmptyBatchTotal)
	s.OrphansResponse = coalesce(s.OrphansResponse)
	s.OrphansOutbound = coalesce(s.OrphansOutbound)
}

// labelEscaper applies Prometheus text exposition escaping to label values:
// backslash → \\, double-quote → \", newline → \n.
var labelEscaper = strings.NewReplacer(
	`\`, `\\`,
	`"`, `\"`,
	"\n", `\n`,
)

// metricsHandler returns an http.HandlerFunc that emits Prometheus text
// exposition v0.0.4. The route is registered outside the auth middleware
// group — it is unauthenticated by deliberate exception (see PRD user story 18).
// src must have been passed through normalize() before this call.
func metricsHandler(src MetricSources) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, "# HELP httpcatch_dropped_total Total records dropped because the capture queue was full.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_dropped_total counter\n")
		fmt.Fprintf(w, "httpcatch_dropped_total %d\n", src.DroppedTotal())

		fmt.Fprintf(w, "# HELP httpcatch_captured_without_correlation_total Total captured records with a synthesized correlation id (no traceparent or X-Request-ID).\n")
		fmt.Fprintf(w, "# TYPE httpcatch_captured_without_correlation_total counter\n")
		fmt.Fprintf(w, "httpcatch_captured_without_correlation_total %d\n", src.CapturedWithoutCorrelationTotal())

		fmt.Fprintf(w, "# HELP httpcatch_captured_without_service_total Total captured records with a fallback service derived from the Host header.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_captured_without_service_total counter\n")
		fmt.Fprintf(w, "httpcatch_captured_without_service_total %d\n", src.CapturedWithoutServiceTotal())

		fmt.Fprintf(w, "# HELP httpcatch_redaction_errors_total Total best-effort redaction failures (counter ticks on unparseable JSON or sjson write failure).\n")
		fmt.Fprintf(w, "# TYPE httpcatch_redaction_errors_total counter\n")
		fmt.Fprintf(w, "httpcatch_redaction_errors_total %d\n", src.RedactionErrorsTotal())

		fmt.Fprintf(w, "# HELP httpcatch_events_ingested_total Total events successfully enqueued via the Events API.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_events_ingested_total counter\n")
		fmt.Fprintf(w, "httpcatch_events_ingested_total{type=\"response\"} %d\n", src.EventsIngestedResponseTotal())
		fmt.Fprintf(w, "httpcatch_events_ingested_total{type=\"outbound\"} %d\n", src.EventsIngestedOutboundTotal())

		fmt.Fprintf(w, "# HELP httpcatch_events_rejected_total Total events rejected by the Events API, by reason.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_events_rejected_total counter\n")
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"invalid_json\"} %d\n", src.EventsRejectedInvalidJSONTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"payload_too_large\"} %d\n", src.EventsRejectedPayloadTooLargeTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"unknown_type\"} %d\n", src.EventsRejectedUnknownTypeTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"missing_type\"} %d\n", src.EventsRejectedMissingTypeTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"missing_required_field\"} %d\n", src.EventsRejectedMissingRequiredFieldTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"empty_batch\"} %d\n", src.EventsRejectedEmptyBatchTotal())

		fmt.Fprintf(w, "# HELP httpcatch_orphans_total Current count of orphan events (no matching captured request in the store), sampled at scrape time.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_orphans_total gauge\n")
		fmt.Fprintf(w, "httpcatch_orphans_total{type=\"response\"} %d\n", src.OrphansResponse())
		fmt.Fprintf(w, "httpcatch_orphans_total{type=\"outbound\"} %d\n", src.OrphansOutbound())

		version := labelEscaper.Replace(buildinfo.Version)
		buildTime := labelEscaper.Replace(buildinfo.BuildTime)
		fmt.Fprintf(w, "# HELP httpcatch_build_info Build identity for the running binary. Always value 1.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_build_info gauge\n")
		fmt.Fprintf(w, "httpcatch_build_info{version=\"%s\",build_time=\"%s\"} 1\n", version, buildTime)
	}
}
