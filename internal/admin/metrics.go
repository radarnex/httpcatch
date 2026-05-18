package admin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/radarnex/httpcatch/internal/buildinfo"
)

// MetricSources holds the counter accessors wired in at construction time.
// Each field is a zero-argument function so the handler reads the live value
// on every request without caching.
type MetricSources struct {
	DroppedTotal                    func() uint64
	CapturedWithoutCorrelationTotal func() uint64
	CapturedWithoutServiceTotal     func() uint64
	RedactionErrorsTotal            func() uint64
}

// metricSources is the package-internal form; identical shape, kept separate
// so the exported struct can be composed freely without exposing internal naming.
type metricSources struct {
	droppedTotal                    func() uint64
	capturedWithoutCorrelationTotal func() uint64
	capturedWithoutServiceTotal     func() uint64
	redactionErrorsTotal            func() uint64
}

func toInternal(s MetricSources) metricSources {
	return metricSources{
		droppedTotal:                    s.DroppedTotal,
		capturedWithoutCorrelationTotal: s.CapturedWithoutCorrelationTotal,
		capturedWithoutServiceTotal:     s.CapturedWithoutServiceTotal,
		redactionErrorsTotal:            s.RedactionErrorsTotal,
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

		version := labelEscaper.Replace(buildinfo.Version)
		buildTime := labelEscaper.Replace(buildinfo.BuildTime)
		fmt.Fprintf(w, "# HELP httpcatch_build_info Build identity for the running binary. Always value 1.\n")
		fmt.Fprintf(w, "# TYPE httpcatch_build_info gauge\n")
		fmt.Fprintf(w, "httpcatch_build_info{version=\"%s\",build_time=\"%s\"} 1\n", version, buildTime)
	}
}
