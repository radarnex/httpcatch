package admin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/radarnex/httpcatch/internal/buildinfo"
)

// MetricSources holds the counter accessors and the unredacted signal wired in
// at construction time. Each field is a zero-argument function so handlers
// read the live value on every request without caching.
type MetricSources struct {
	DroppedTotal                    func() uint64
	CapturedWithoutCorrelationTotal func() uint64
	CapturedWithoutServiceTotal     func() uint64
	RedactionErrorsTotal            func() uint64
	// Unredacted reports whether the redaction ruleset has no rules configured.
	// When true, the UI shows a prominent banner warning the operator.
	Unredacted func() bool

	// Events API counters.
	EventsIngestedResponseTotal          func() uint64
	EventsIngestedOutboundTotal          func() uint64
	EventsRejectedInvalidJSONTotal       func() uint64
	EventsRejectedPayloadTooLargeTotal   func() uint64
	EventsRejectedUnknownTypeTotal       func() uint64
	EventsRejectedMissingTypeTotal       func() uint64
	EventsRejectedMissingRequiredFieldTotal func() uint64
	EventsRejectedEmptyBatchTotal        func() uint64
}

// metricSources is the package-internal form; identical shape, kept separate
// so the exported struct can be composed freely without exposing internal naming.
type metricSources struct {
	droppedTotal                    func() uint64
	capturedWithoutCorrelationTotal func() uint64
	capturedWithoutServiceTotal     func() uint64
	redactionErrorsTotal            func() uint64
	unredacted                      func() bool

	eventsIngestedResponseTotal          func() uint64
	eventsIngestedOutboundTotal          func() uint64
	eventsRejectedInvalidJSONTotal       func() uint64
	eventsRejectedPayloadTooLargeTotal   func() uint64
	eventsRejectedUnknownTypeTotal       func() uint64
	eventsRejectedMissingTypeTotal       func() uint64
	eventsRejectedMissingRequiredFieldTotal func() uint64
	eventsRejectedEmptyBatchTotal        func() uint64
}

func zeroUint64() uint64 { return 0 }
func falseBool() bool   { return false }

func nilToZero(f func() uint64) func() uint64 {
	if f == nil {
		return zeroUint64
	}
	return f
}

func toInternal(s MetricSources) metricSources {
	unredacted := s.Unredacted
	if unredacted == nil {
		unredacted = falseBool
	}
	return metricSources{
		droppedTotal:                    nilToZero(s.DroppedTotal),
		capturedWithoutCorrelationTotal: nilToZero(s.CapturedWithoutCorrelationTotal),
		capturedWithoutServiceTotal:     nilToZero(s.CapturedWithoutServiceTotal),
		redactionErrorsTotal:            nilToZero(s.RedactionErrorsTotal),
		unredacted:                      unredacted,

		eventsIngestedResponseTotal:          nilToZero(s.EventsIngestedResponseTotal),
		eventsIngestedOutboundTotal:          nilToZero(s.EventsIngestedOutboundTotal),
		eventsRejectedInvalidJSONTotal:       nilToZero(s.EventsRejectedInvalidJSONTotal),
		eventsRejectedPayloadTooLargeTotal:   nilToZero(s.EventsRejectedPayloadTooLargeTotal),
		eventsRejectedUnknownTypeTotal:       nilToZero(s.EventsRejectedUnknownTypeTotal),
		eventsRejectedMissingTypeTotal:       nilToZero(s.EventsRejectedMissingTypeTotal),
		eventsRejectedMissingRequiredFieldTotal: nilToZero(s.EventsRejectedMissingRequiredFieldTotal),
		eventsRejectedEmptyBatchTotal:        nilToZero(s.EventsRejectedEmptyBatchTotal),
	}
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
func metricsHandler(src metricSources) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, "# HELP httpcatch_dropped_total Total records dropped because the capture queue was full.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_dropped_total counter\n")
		fmt.Fprintf(w, "httpcatch_dropped_total %d\n", src.droppedTotal())

		fmt.Fprintf(w, "# HELP httpcatch_captured_without_correlation_total Total captured records with a synthesized correlation id (no traceparent or X-Request-ID).\n")
		fmt.Fprintf(w, "# TYPE httpcatch_captured_without_correlation_total counter\n")
		fmt.Fprintf(w, "httpcatch_captured_without_correlation_total %d\n", src.capturedWithoutCorrelationTotal())

		fmt.Fprintf(w, "# HELP httpcatch_captured_without_service_total Total captured records with a fallback service derived from the Host header.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_captured_without_service_total counter\n")
		fmt.Fprintf(w, "httpcatch_captured_without_service_total %d\n", src.capturedWithoutServiceTotal())

		fmt.Fprintf(w, "# HELP httpcatch_redaction_errors_total Total best-effort redaction failures (counter ticks on unparseable JSON or sjson write failure).\n")
		fmt.Fprintf(w, "# TYPE httpcatch_redaction_errors_total counter\n")
		fmt.Fprintf(w, "httpcatch_redaction_errors_total %d\n", src.redactionErrorsTotal())

		fmt.Fprintf(w, "# HELP httpcatch_events_ingested_total Total events successfully enqueued via the Events API.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_events_ingested_total counter\n")
		fmt.Fprintf(w, "httpcatch_events_ingested_total{type=\"response\"} %d\n", src.eventsIngestedResponseTotal())
		fmt.Fprintf(w, "httpcatch_events_ingested_total{type=\"outbound\"} %d\n", src.eventsIngestedOutboundTotal())

		fmt.Fprintf(w, "# HELP httpcatch_events_rejected_total Total events rejected by the Events API, by reason.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_events_rejected_total counter\n")
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"invalid_json\"} %d\n", src.eventsRejectedInvalidJSONTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"payload_too_large\"} %d\n", src.eventsRejectedPayloadTooLargeTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"unknown_type\"} %d\n", src.eventsRejectedUnknownTypeTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"missing_type\"} %d\n", src.eventsRejectedMissingTypeTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"missing_required_field\"} %d\n", src.eventsRejectedMissingRequiredFieldTotal())
		fmt.Fprintf(w, "httpcatch_events_rejected_total{reason=\"empty_batch\"} %d\n", src.eventsRejectedEmptyBatchTotal())

		version := labelEscaper.Replace(buildinfo.Version)
		buildTime := labelEscaper.Replace(buildinfo.BuildTime)
		fmt.Fprintf(w, "# HELP httpcatch_build_info Build identity for the running binary. Always value 1.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_build_info gauge\n")
		fmt.Fprintf(w, "httpcatch_build_info{version=\"%s\",build_time=\"%s\"} 1\n", version, buildTime)
	}
}
