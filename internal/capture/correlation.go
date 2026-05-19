package capture

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
)

const (
	TraceparentHeader  = "traceparent"
	RequestIDHeader    = "X-Request-ID"
	traceparentVersion = "00"
	traceIDHexLen      = 32
	allZeroTraceID     = "00000000000000000000000000000000"

	CorrelationSourceTraceparent = "traceparent"
	CorrelationSourceRequestID   = "request_id"
	CorrelationSourceSynthesized = "synthesized"
	// CorrelationSourceExplicit is used when the events API caller supplies a
	// correlation_id directly in the event body.
	CorrelationSourceExplicit = "explicit"
)

// IdentifyCorrelation resolves the correlation identifier for a captured
// request. The chain is: trace-id segment of `traceparent` → `X-Request-ID`
// → synthesized UUID. `X-Correlation-ID` is never consulted. A malformed
// `traceparent` is treated as absent and falls through to `X-Request-ID`.
func IdentifyCorrelation(headers http.Header) (id, source string) {
	if tp := strings.TrimSpace(headers.Get(TraceparentHeader)); tp != "" {
		if traceID, ok := parseTraceparent(tp); ok {
			return traceID, CorrelationSourceTraceparent
		}
	}
	if rid := strings.TrimSpace(headers.Get(RequestIDHeader)); rid != "" {
		return rid, CorrelationSourceRequestID
	}
	return uuid.NewString(), CorrelationSourceSynthesized
}

// parseTraceparent returns the trace-id from a W3C traceparent header. The
// header must have at least version-traceid-spanid-flags; we accept the
// version-00 shape and treat unknown future versions as malformed.
func parseTraceparent(s string) (string, bool) {
	parts := strings.Split(s, "-")
	if len(parts) < 4 {
		return "", false
	}
	if parts[0] != traceparentVersion {
		return "", false
	}
	traceID := strings.ToLower(parts[1])
	if len(traceID) != traceIDHexLen {
		return "", false
	}
	for _, c := range traceID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", false
		}
	}
	if traceID == allZeroTraceID {
		return "", false
	}
	return traceID, true
}
