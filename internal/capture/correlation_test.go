package capture

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestIdentifyCorrelation(t *testing.T) {
	t.Parallel()

	const (
		traceID = "0af7651916cd43dd8448eb211c80319c"
		rid     = "req-abc-123"
		corrID  = "corr-should-be-ignored"
	)
	traceparent := "00-" + traceID + "-b7ad6b7169203331-01"

	mk := func(pairs ...string) http.Header { return mkHeader(t, pairs...) }

	tests := []struct {
		name           string
		headers        http.Header
		wantValue      string
		wantSource     string
		wantSynthCheck bool
	}{
		{
			name:       "traceparent only extracts trace-id segment",
			headers:    mk(TraceparentHeader, traceparent),
			wantValue:  traceID,
			wantSource: CorrelationSourceTraceparent,
		},
		{
			name:       "x-request-id only",
			headers:    mk(RequestIDHeader, rid),
			wantValue:  rid,
			wantSource: CorrelationSourceRequestID,
		},
		{
			name:       "traceparent wins when both present",
			headers:    mk(TraceparentHeader, traceparent, RequestIDHeader, rid),
			wantValue:  traceID,
			wantSource: CorrelationSourceTraceparent,
		},
		{
			name:           "x-correlation-id alone is ignored and synthesizes",
			headers:        mk("X-Correlation-ID", corrID),
			wantSource:     CorrelationSourceSynthesized,
			wantSynthCheck: true,
		},
		{
			name:           "no headers synthesizes",
			headers:        mk(),
			wantSource:     CorrelationSourceSynthesized,
			wantSynthCheck: true,
		},
		{
			name:       "malformed traceparent falls through to x-request-id",
			headers:    mk(TraceparentHeader, "garbage", RequestIDHeader, rid),
			wantValue:  rid,
			wantSource: CorrelationSourceRequestID,
		},
		{
			name:           "all-zero trace-id falls through to synthesized when no x-request-id",
			headers:        mk(TraceparentHeader, "00-"+allZeroTraceID+"-b7ad6b7169203331-01"),
			wantSource:     CorrelationSourceSynthesized,
			wantSynthCheck: true,
		},
		{
			name:       "uppercase trace-id is normalised to lowercase",
			headers:    mk(TraceparentHeader, "00-0AF7651916CD43DD8448EB211C80319C-b7ad6b7169203331-01"),
			wantValue:  traceID,
			wantSource: CorrelationSourceTraceparent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotValue, gotSource := IdentifyCorrelation(tt.headers)
			if gotSource != tt.wantSource {
				t.Errorf("source: got %q want %q", gotSource, tt.wantSource)
			}
			if tt.wantSynthCheck {
				if _, err := uuid.Parse(gotValue); err != nil {
					t.Errorf("synthesized id %q is not a valid UUID: %v", gotValue, err)
				}
				return
			}
			if gotValue != tt.wantValue {
				t.Errorf("value: got %q want %q", gotValue, tt.wantValue)
			}
		})
	}
}

func TestIdentifyCorrelation_SynthesizedIsUnique(t *testing.T) {
	t.Parallel()

	id1, _ := IdentifyCorrelation(http.Header{})
	id2, _ := IdentifyCorrelation(http.Header{})
	if id1 == id2 {
		t.Errorf("two synthesized ids should differ, got %q twice", id1)
	}
}
