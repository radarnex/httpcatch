package capture

import "time"

// RecordKind is the discriminator for the Record sum type.
type RecordKind string

const (
	KindRequest       RecordKind = "request"
	KindResponseEvent RecordKind = "response_event"
	KindOutboundEvent RecordKind = "outbound_event"
)

// Record is the polymorphic abstraction that flows through the capture queue,
// redactor, and sinks. Using an interface rather than a tagged struct keeps
// each variant's fields private to callers that do not need to branch per
// variant — sinks and the redactor receive a Record and dispatch via Kind().
type Record interface {
	Kind() RecordKind
	RecordTimestamp() time.Time
	RecordService() string
	RecordCorrelationID() string
	RecordID() string
}

type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CapturedRequest is the wire-level HTTP record produced by the capture port.
type CapturedRequest struct {
	ID            string              `json:"id"`
	Timestamp     time.Time           `json:"timestamp"`
	Method        string              `json:"method"`
	Path          string              `json:"path"`
	Query         map[string][]string `json:"query"`
	Headers       map[string][]string `json:"headers"`
	Cookies       []Cookie            `json:"cookies"`
	Body          []byte              `json:"body"`
	BodyTruncated bool                `json:"body_truncated"`
	// BodyOriginalSize is the exact byte count when BodyTruncated is false.
	// When BodyTruncated is true it is a lower bound (body_cap + 1): the body
	// exceeded the configured cap and the remainder was not read or measured.
	BodyOriginalSize  int    `json:"body_original_size"`
	ContentType       string `json:"content_type"`
	SourceIP          string `json:"source_ip"`
	Service           string `json:"service"`
	ServiceSource     string `json:"service_source"`
	CorrelationID     string `json:"correlation_id"`
	CorrelationSource string `json:"correlation_source"`
}

func (r *CapturedRequest) Kind() RecordKind            { return KindRequest }
func (r *CapturedRequest) RecordTimestamp() time.Time  { return r.Timestamp }
func (r *CapturedRequest) RecordService() string       { return r.Service }
func (r *CapturedRequest) RecordCorrelationID() string { return r.CorrelationID }
func (r *CapturedRequest) RecordID() string            { return r.ID }

// ResponseEvent carries the HTTP response an application returned for a
// correlated captured request, pushed via the Events API.
type ResponseEvent struct {
	ID                string              `json:"id"`
	Timestamp         time.Time           `json:"timestamp"`
	CorrelationID     string              `json:"correlation_id"`
	CorrelationSource string              `json:"correlation_source"`
	Service           string              `json:"service"`
	ServiceSource     string              `json:"service_source"`
	Status            int                 `json:"status"`
	Headers           map[string][]string `json:"headers"`
	Body              []byte              `json:"body"`
	BodyTruncated     bool                `json:"body_truncated"`
	// BodyOriginalSize is the exact byte count when BodyTruncated is false.
	// When BodyTruncated is true it is a lower bound (body_cap + 1): the body
	// exceeded the configured cap and the remainder was not read or measured.
	BodyOriginalSize int    `json:"body_original_size"`
	ContentType      string `json:"content_type"`
	DurationMS       int64  `json:"duration_ms"`
}

func (r *ResponseEvent) Kind() RecordKind            { return KindResponseEvent }
func (r *ResponseEvent) RecordTimestamp() time.Time  { return r.Timestamp }
func (r *ResponseEvent) RecordService() string       { return r.Service }
func (r *ResponseEvent) RecordCorrelationID() string { return r.CorrelationID }
func (r *ResponseEvent) RecordID() string            { return r.ID }

// OutboundRequestHalf is the request side of an outbound call recorded by the
// application.
type OutboundRequestHalf struct {
	Method        string              `json:"method"`
	Path          string              `json:"path"`
	Headers       map[string][]string `json:"headers"`
	Body          []byte              `json:"body"`
	BodyTruncated bool                `json:"body_truncated"`
	// BodyOriginalSize is the exact byte count when BodyTruncated is false.
	// When BodyTruncated is true it is a lower bound (body_cap + 1): the body
	// exceeded the configured cap and the remainder was not read or measured.
	BodyOriginalSize int    `json:"body_original_size"`
	ContentType      string `json:"content_type"`
}

// OutboundResponseHalf is the response side of an outbound call. It is nil
// when the downstream call never completed.
type OutboundResponseHalf struct {
	Status        int                 `json:"status"`
	Headers       map[string][]string `json:"headers"`
	Body          []byte              `json:"body"`
	BodyTruncated bool                `json:"body_truncated"`
	// BodyOriginalSize is the exact byte count when BodyTruncated is false.
	// When BodyTruncated is true it is a lower bound (body_cap + 1): the body
	// exceeded the configured cap and the remainder was not read or measured.
	BodyOriginalSize int    `json:"body_original_size"`
	ContentType      string `json:"content_type"`
}

// OutboundEvent carries both halves of a downstream call the application made,
// pushed via the Events API. Response is nil when the call never completed.
type OutboundEvent struct {
	ID                string                `json:"id"`
	Timestamp         time.Time             `json:"timestamp"`
	CorrelationID     string                `json:"correlation_id"`
	CorrelationSource string                `json:"correlation_source"`
	Service           string                `json:"service"`
	ServiceSource     string                `json:"service_source"`
	DurationMS        int64                 `json:"duration_ms"`
	Request           OutboundRequestHalf   `json:"request"`
	Response          *OutboundResponseHalf `json:"response"`
}

func (r *OutboundEvent) Kind() RecordKind            { return KindOutboundEvent }
func (r *OutboundEvent) RecordTimestamp() time.Time  { return r.Timestamp }
func (r *OutboundEvent) RecordService() string       { return r.Service }
func (r *OutboundEvent) RecordCorrelationID() string { return r.CorrelationID }
func (r *OutboundEvent) RecordID() string            { return r.ID }
