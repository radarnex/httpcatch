package admin

import "sync/atomic"

// EventsCounters tracks ingestion and rejection counters for the Events API.
// Counters are incremented by the events handler and read by the metrics handler.
type EventsCounters struct {
	ingestedResponse atomic.Uint64
	ingestedOutbound atomic.Uint64

	rejectedInvalidJSON          atomic.Uint64
	rejectedPayloadTooLarge      atomic.Uint64
	rejectedUnknownType          atomic.Uint64
	rejectedMissingType          atomic.Uint64
	rejectedMissingRequiredField atomic.Uint64
	rejectedEmptyBatch           atomic.Uint64
	rejectedBatchTooLarge        atomic.Uint64
	rejectedBodyTooLarge         atomic.Uint64

	droppedQueueFull atomic.Uint64
}

func NewEventsCounters() *EventsCounters { return &EventsCounters{} }

func (c *EventsCounters) incIngestedResponse() { c.ingestedResponse.Add(1) }
func (c *EventsCounters) incIngestedOutbound() { c.ingestedOutbound.Add(1) }

const (
	RejectReasonInvalidJSON          = "invalid_json"
	RejectReasonPayloadTooLarge      = "payload_too_large"
	RejectReasonUnknownType          = "unknown_type"
	RejectReasonMissingType          = "missing_type"
	RejectReasonMissingRequiredField = "missing_required_field"
	RejectReasonEmptyBatch           = "empty_batch"
	RejectReasonBatchTooLarge        = "batch_too_large"
	RejectReasonBodyTooLarge         = "body_too_large"
)

func (c *EventsCounters) incRejected(reason string) {
	switch reason {
	case RejectReasonInvalidJSON:
		c.rejectedInvalidJSON.Add(1)
	case RejectReasonPayloadTooLarge:
		c.rejectedPayloadTooLarge.Add(1)
	case RejectReasonUnknownType:
		c.rejectedUnknownType.Add(1)
	case RejectReasonMissingType:
		c.rejectedMissingType.Add(1)
	case RejectReasonMissingRequiredField:
		c.rejectedMissingRequiredField.Add(1)
	case RejectReasonEmptyBatch:
		c.rejectedEmptyBatch.Add(1)
	case RejectReasonBatchTooLarge:
		c.rejectedBatchTooLarge.Add(1)
	case RejectReasonBodyTooLarge:
		c.rejectedBodyTooLarge.Add(1)
	}
}

func (c *EventsCounters) incDroppedQueueFull() { c.droppedQueueFull.Add(1) }

func (c *EventsCounters) EventsIngestedResponseTotal() uint64 {
	return c.ingestedResponse.Load()
}
func (c *EventsCounters) EventsIngestedOutboundTotal() uint64 {
	return c.ingestedOutbound.Load()
}
func (c *EventsCounters) EventsRejectedInvalidJSONTotal() uint64 {
	return c.rejectedInvalidJSON.Load()
}
func (c *EventsCounters) EventsRejectedPayloadTooLargeTotal() uint64 {
	return c.rejectedPayloadTooLarge.Load()
}
func (c *EventsCounters) EventsRejectedUnknownTypeTotal() uint64 {
	return c.rejectedUnknownType.Load()
}
func (c *EventsCounters) EventsRejectedMissingTypeTotal() uint64 {
	return c.rejectedMissingType.Load()
}
func (c *EventsCounters) EventsRejectedMissingRequiredFieldTotal() uint64 {
	return c.rejectedMissingRequiredField.Load()
}
func (c *EventsCounters) EventsRejectedEmptyBatchTotal() uint64 {
	return c.rejectedEmptyBatch.Load()
}
func (c *EventsCounters) EventsRejectedBatchTooLargeTotal() uint64 {
	return c.rejectedBatchTooLarge.Load()
}
func (c *EventsCounters) EventsRejectedBodyTooLargeTotal() uint64 {
	return c.rejectedBodyTooLarge.Load()
}
func (c *EventsCounters) EventsDroppedQueueFullTotal() uint64 {
	return c.droppedQueueFull.Load()
}
