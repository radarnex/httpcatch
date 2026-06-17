package capture

import "sync/atomic"

// Counters surfaces the in-process counters that sit alongside the queue's
// dropped_total. The captured-family counters (captured, withoutService,
// withoutCorrelation) are incremented together only on a successful enqueue, so
// they share one population; bodyReadErrors is incremented when a request body
// cannot be read. Each is read via its own accessor.
type Counters struct {
	captured           atomic.Uint64
	withoutService     atomic.Uint64
	withoutCorrelation atomic.Uint64
	bodyReadErrors     atomic.Uint64
}

func NewCounters() *Counters { return &Counters{} }

func (c *Counters) incCaptured()           { c.captured.Add(1) }
func (c *Counters) incWithoutService()     { c.withoutService.Add(1) }
func (c *Counters) incWithoutCorrelation() { c.withoutCorrelation.Add(1) }
func (c *Counters) incBodyReadError()      { c.bodyReadErrors.Add(1) }

// CapturedTotal counts requests the capture handler successfully enqueued. It is
// owned here rather than on the queue so Events API enqueues — which share the
// same queue — are not miscounted as captured requests.
func (c *Counters) CapturedTotal() uint64 {
	return c.captured.Load()
}

func (c *Counters) CapturedWithoutServiceTotal() uint64 {
	return c.withoutService.Load()
}

func (c *Counters) CapturedWithoutCorrelationTotal() uint64 {
	return c.withoutCorrelation.Load()
}

func (c *Counters) CaptureErrorsTotal() uint64 {
	return c.bodyReadErrors.Load()
}
