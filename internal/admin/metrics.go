package admin

import (
	"github.com/radarnex/httpcatch/internal/buildinfo"
	"github.com/radarnex/httpcatch/internal/metrics"
)

// MetricSources holds the counter accessors and the unredacted signal wired in
// at construction time. Each counter field is a zero-argument function so the
// values are read live; nil fields are replaced by safe zero-returning functions
// in normalize(). The Prometheus exposition is built from these by metrics.New;
// the /status page and Configuration UI read a subset directly.
type MetricSources struct {
	CapturedTotal                   func() uint64
	DroppedTotal                    func() uint64
	CaptureErrorsTotal              func() uint64
	CapturedWithoutCorrelationTotal func() uint64
	CapturedWithoutServiceTotal     func() uint64
	RedactionErrorsTotal            func() uint64
	WorkerPanicsTotal               func() uint64
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
	EventsRejectedBatchTooLargeTotal        func() uint64
	EventsRejectedBodyTooLargeTotal         func() uint64
	EventsDroppedQueueFullTotal             func() uint64

	// OrphansResponse and OrphansOutbound are gauges sampled at scrape time.
	// Each returns the current count of orphan events of the respective type
	// visible in the configured store (memory or sqlite). A nil function is
	// treated as returning 0.
	OrphansResponse func() int
	OrphansOutbound func() int

	// Auth failure counters, broken out by reason.
	AuthFailuresInvalidTokenTotal func() uint64
	AuthFailuresRateLimitedTotal  func() uint64
	AuthFailuresCSRFBlockedTotal  func() uint64

	// Sink write failure counters, one per concrete sink.
	SinkWriteErrorsMemoryTotal func() uint64
	SinkWriteErrorsSQLiteTotal func() uint64
	SinkWriteErrorsStdoutTotal func() uint64
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
// so that handlers and the metrics collector never need nil checks.
func (s *MetricSources) normalize() {
	s.CapturedTotal = coalesce(s.CapturedTotal)
	s.DroppedTotal = coalesce(s.DroppedTotal)
	s.CaptureErrorsTotal = coalesce(s.CaptureErrorsTotal)
	s.CapturedWithoutCorrelationTotal = coalesce(s.CapturedWithoutCorrelationTotal)
	s.CapturedWithoutServiceTotal = coalesce(s.CapturedWithoutServiceTotal)
	s.RedactionErrorsTotal = coalesce(s.RedactionErrorsTotal)
	s.WorkerPanicsTotal = coalesce(s.WorkerPanicsTotal)
	s.Unredacted = coalesce(s.Unredacted)
	s.EventsIngestedResponseTotal = coalesce(s.EventsIngestedResponseTotal)
	s.EventsIngestedOutboundTotal = coalesce(s.EventsIngestedOutboundTotal)
	s.EventsRejectedInvalidJSONTotal = coalesce(s.EventsRejectedInvalidJSONTotal)
	s.EventsRejectedPayloadTooLargeTotal = coalesce(s.EventsRejectedPayloadTooLargeTotal)
	s.EventsRejectedUnknownTypeTotal = coalesce(s.EventsRejectedUnknownTypeTotal)
	s.EventsRejectedMissingTypeTotal = coalesce(s.EventsRejectedMissingTypeTotal)
	s.EventsRejectedMissingRequiredFieldTotal = coalesce(s.EventsRejectedMissingRequiredFieldTotal)
	s.EventsRejectedEmptyBatchTotal = coalesce(s.EventsRejectedEmptyBatchTotal)
	s.EventsRejectedBatchTooLargeTotal = coalesce(s.EventsRejectedBatchTooLargeTotal)
	s.EventsRejectedBodyTooLargeTotal = coalesce(s.EventsRejectedBodyTooLargeTotal)
	s.EventsDroppedQueueFullTotal = coalesce(s.EventsDroppedQueueFullTotal)
	s.OrphansResponse = coalesce(s.OrphansResponse)
	s.OrphansOutbound = coalesce(s.OrphansOutbound)
	s.AuthFailuresInvalidTokenTotal = coalesce(s.AuthFailuresInvalidTokenTotal)
	s.AuthFailuresRateLimitedTotal = coalesce(s.AuthFailuresRateLimitedTotal)
	s.AuthFailuresCSRFBlockedTotal = coalesce(s.AuthFailuresCSRFBlockedTotal)
	s.SinkWriteErrorsMemoryTotal = coalesce(s.SinkWriteErrorsMemoryTotal)
	s.SinkWriteErrorsSQLiteTotal = coalesce(s.SinkWriteErrorsSQLiteTotal)
	s.SinkWriteErrorsStdoutTotal = coalesce(s.SinkWriteErrorsStdoutTotal)
}

// promSources projects the wired counters onto the metrics package's Sources.
// build_info labels are read from buildinfo at construction time.
func (s MetricSources) promSources() metrics.Sources {
	return metrics.Sources{
		CapturedTotal:                   s.CapturedTotal,
		DroppedTotal:                    s.DroppedTotal,
		CaptureErrorsTotal:              s.CaptureErrorsTotal,
		CapturedWithoutCorrelationTotal: s.CapturedWithoutCorrelationTotal,
		CapturedWithoutServiceTotal:     s.CapturedWithoutServiceTotal,
		RedactionErrorsTotal:            s.RedactionErrorsTotal,
		WorkerPanicsTotal:               s.WorkerPanicsTotal,

		EventsIngestedResponseTotal:             s.EventsIngestedResponseTotal,
		EventsIngestedOutboundTotal:             s.EventsIngestedOutboundTotal,
		EventsRejectedInvalidJSONTotal:          s.EventsRejectedInvalidJSONTotal,
		EventsRejectedPayloadTooLargeTotal:      s.EventsRejectedPayloadTooLargeTotal,
		EventsRejectedUnknownTypeTotal:          s.EventsRejectedUnknownTypeTotal,
		EventsRejectedMissingTypeTotal:          s.EventsRejectedMissingTypeTotal,
		EventsRejectedMissingRequiredFieldTotal: s.EventsRejectedMissingRequiredFieldTotal,
		EventsRejectedEmptyBatchTotal:           s.EventsRejectedEmptyBatchTotal,
		EventsRejectedBatchTooLargeTotal:        s.EventsRejectedBatchTooLargeTotal,
		EventsRejectedBodyTooLargeTotal:         s.EventsRejectedBodyTooLargeTotal,
		EventsDroppedQueueFullTotal:             s.EventsDroppedQueueFullTotal,

		OrphansResponse: s.OrphansResponse,
		OrphansOutbound: s.OrphansOutbound,

		AuthFailuresInvalidTokenTotal: s.AuthFailuresInvalidTokenTotal,
		AuthFailuresRateLimitedTotal:  s.AuthFailuresRateLimitedTotal,
		AuthFailuresCSRFBlockedTotal:  s.AuthFailuresCSRFBlockedTotal,

		SinkWriteErrorsMemoryTotal: s.SinkWriteErrorsMemoryTotal,
		SinkWriteErrorsSQLiteTotal: s.SinkWriteErrorsSQLiteTotal,
		SinkWriteErrorsStdoutTotal: s.SinkWriteErrorsStdoutTotal,

		Version:   buildinfo.Version,
		BuildTime: buildinfo.BuildTime,
	}
}
