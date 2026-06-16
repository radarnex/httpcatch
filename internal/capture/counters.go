package capture

import "sync/atomic"

// Counters surfaces the in-process counters that sit alongside the queue's
// dropped_total — each one is incremented at record-construction time and
// read via its own accessor.
type Counters struct {
	withoutService     atomic.Uint64
	withoutCorrelation atomic.Uint64
}

func NewCounters() *Counters { return &Counters{} }

func (c *Counters) incWithoutService()     { c.withoutService.Add(1) }
func (c *Counters) incWithoutCorrelation() { c.withoutCorrelation.Add(1) }

func (c *Counters) CapturedWithoutServiceTotal() uint64 {
	return c.withoutService.Load()
}

func (c *Counters) CapturedWithoutCorrelationTotal() uint64 {
	return c.withoutCorrelation.Load()
}
