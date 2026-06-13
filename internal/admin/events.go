package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/radarnex/httpcatch/internal/capture"
)

// eventType discriminates the two wire-level event kinds accepted by POST /events.
type eventType string

const (
	eventTypeResponse eventType = "response"
	eventTypeOutbound eventType = "outbound"
)

// wireResponseEvent is the JSON-decoded shape for a response event payload.
type wireResponseEvent struct {
	Type          string              `json:"type"`
	CorrelationID string              `json:"correlation_id"`
	Service       string              `json:"service"`
	Timestamp     *time.Time          `json:"timestamp"`
	Status        *int                `json:"status"`
	Headers       map[string][]string `json:"headers"`
	Body          string              `json:"body"`
	ContentType   string              `json:"content_type"`
	DurationMS    *int64              `json:"duration_ms"`
}

// wireOutboundHalf is the request or response half of an outbound event.
type wireOutboundHalf struct {
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Headers     map[string][]string `json:"headers"`
	Body        string              `json:"body"`
	ContentType string              `json:"content_type"`
	Status      *int                `json:"status"`
}

// wireOutboundEvent is the JSON-decoded shape for an outbound event payload.
type wireOutboundEvent struct {
	Type          string            `json:"type"`
	CorrelationID string            `json:"correlation_id"`
	Service       string            `json:"service"`
	Timestamp     *time.Time        `json:"timestamp"`
	Request       *wireOutboundHalf `json:"request"`
	Response      *wireOutboundHalf `json:"response"`
	DurationMS    *int64            `json:"duration_ms"`
}

// validationError describes a field-level failure at a specific batch index.
type validationError struct {
	Index   *int   `json:"index,omitempty"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type eventsErrorResponse struct {
	Errors []validationError `json:"errors"`
}

// validateResponseEvent checks that all required fields are present. It is a
// pure function with no side effects.
func validateResponseEvent(e wireResponseEvent) []error {
	var errs []error
	if service := strings.TrimSpace(e.Service); service == "" {
		// service is authoritative on the events API — no Host-header fallback.
		// This is a deliberate asymmetry with the capture port, where the host
		// header provides a fallback for misconfigured proxies. App code calling
		// the events API always knows its own service name.
		errs = append(errs, fmt.Errorf("service: required"))
	} else if capture.SanitiseServiceLabel(service) == "" {
		errs = append(errs, fmt.Errorf("service: invalid"))
	}
	if e.Status == nil {
		errs = append(errs, fmt.Errorf("status: required"))
	}
	if e.DurationMS == nil {
		errs = append(errs, fmt.Errorf("duration_ms: required"))
	}
	return errs
}

// validateOutboundEvent checks that all required fields are present. It is a
// pure function with no side effects.
func validateOutboundEvent(e wireOutboundEvent) []error {
	var errs []error
	if service := strings.TrimSpace(e.Service); service == "" {
		errs = append(errs, fmt.Errorf("service: required"))
	} else if capture.SanitiseServiceLabel(service) == "" {
		errs = append(errs, fmt.Errorf("service: invalid"))
	}
	if e.Request == nil {
		errs = append(errs, fmt.Errorf("request: required"))
	} else {
		if strings.TrimSpace(e.Request.Method) == "" {
			errs = append(errs, fmt.Errorf("request.method: required"))
		}
		if strings.TrimSpace(e.Request.Path) == "" {
			errs = append(errs, fmt.Errorf("request.path: required"))
		}
	}
	if e.DurationMS == nil {
		errs = append(errs, fmt.Errorf("duration_ms: required"))
	}
	// response may be null (call never completed); when present, status is required.
	if e.Response != nil && e.Response.Status == nil {
		errs = append(errs, fmt.Errorf("response.status: required when response is present"))
	}
	return errs
}

// maxExplicitCorrelationID is the maximum byte length accepted for a
// caller-supplied correlation_id. Values exceeding this fall through to
// header-derived or synthesized correlation.
const maxExplicitCorrelationID = 256

// isPrintableASCII returns true when every byte in s is a printable ASCII
// character (0x20–0x7E inclusive).
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// deriveCorrelation resolves the correlation id and source for an event,
// following the same precedence as the capture pipeline:
//  1. explicit correlation_id in the event body
//  2. traceparent trace-id from the event's headers
//  3. X-Request-ID from the event's headers
//  4. synthesized UUID
func deriveCorrelation(correlationID string, headers map[string][]string) (id, source string) {
	if c := strings.TrimSpace(correlationID); c != "" {
		if len(c) <= maxExplicitCorrelationID && isPrintableASCII(c) {
			return c, capture.CorrelationSourceExplicit
		}
		// malformed or oversized — fall through to header-derived / synthesized
	}
	// Build an http.Header from the event's own headers map so we can reuse
	// the capture package's correlation identifier.
	h := make(http.Header)
	for k, vs := range headers {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	return capture.IdentifyCorrelation(h)
}

// capEventBody applies the body cap to a string body, returning the capped
// bytes plus truncation metadata.
func capEventBody(body string, bodyCap int) (capped []byte, originalSize int, truncated bool) {
	if len(body) == 0 {
		return nil, 0, false
	}
	capped, originalSize, truncated, _ = capture.CapBody(strings.NewReader(body), bodyCap)
	return capped, originalSize, truncated
}

// resolveContentType returns the content type for an event half using the
// following precedence:
//  1. The explicit content_type field on the wire payload, when non-empty.
//  2. The Content-Type header from the event's headers map, when present.
//  3. Empty string (no content type declared).
func resolveContentType(wireContentType string, headers map[string][]string) string {
	if wireContentType != "" {
		return wireContentType
	}
	key := textproto.CanonicalMIMEHeaderKey("Content-Type")
	if vals, ok := headers[key]; ok && len(vals) > 0 && vals[0] != "" {
		return vals[0]
	}
	// Headers may also be stored with non-canonical casing.
	for k, vals := range headers {
		if textproto.CanonicalMIMEHeaderKey(k) == key && len(vals) > 0 && vals[0] != "" {
			return vals[0]
		}
	}
	return ""
}

// buildResponseRecord constructs a capture.ResponseEvent from the validated wire shape.
func buildResponseRecord(e wireResponseEvent, bodyCap int, now time.Time) *capture.ResponseEvent {
	ts := now
	if e.Timestamp != nil {
		ts = *e.Timestamp
	}
	corrID, corrSource := deriveCorrelation(e.CorrelationID, e.Headers)
	body, originalSize, truncated := capEventBody(e.Body, bodyCap)
	return &capture.ResponseEvent{
		ID:                uuid.NewString(),
		Timestamp:         ts,
		CorrelationID:     corrID,
		CorrelationSource: corrSource,
		Service:           capture.SanitiseServiceLabel(e.Service),
		ServiceSource:     "explicit",
		Status:            *e.Status,
		Headers:           e.Headers,
		Body:              body,
		BodyTruncated:     truncated,
		BodyOriginalSize:  originalSize,
		ContentType:       resolveContentType(e.ContentType, e.Headers),
		DurationMS:        *e.DurationMS,
	}
}

// buildOutboundRecord constructs a capture.OutboundEvent from the validated wire shape.
func buildOutboundRecord(e wireOutboundEvent, bodyCap int, now time.Time) *capture.OutboundEvent {
	ts := now
	if e.Timestamp != nil {
		ts = *e.Timestamp
	}
	// Derive correlation from the request headers when no explicit id is given.
	var headers map[string][]string
	if e.Request != nil {
		headers = e.Request.Headers
	}
	corrID, corrSource := deriveCorrelation(e.CorrelationID, headers)

	reqBody, reqOrigSize, reqTruncated := capEventBody(e.Request.Body, bodyCap)
	req := capture.OutboundRequestHalf{
		Method:           e.Request.Method,
		Path:             e.Request.Path,
		Headers:          e.Request.Headers,
		Body:             reqBody,
		BodyTruncated:    reqTruncated,
		BodyOriginalSize: reqOrigSize,
		ContentType:      resolveContentType(e.Request.ContentType, e.Request.Headers),
	}

	var resp *capture.OutboundResponseHalf
	if e.Response != nil {
		respBody, respOrigSize, respTruncated := capEventBody(e.Response.Body, bodyCap)
		resp = &capture.OutboundResponseHalf{
			Status:           *e.Response.Status,
			Headers:          e.Response.Headers,
			Body:             respBody,
			BodyTruncated:    respTruncated,
			BodyOriginalSize: respOrigSize,
			ContentType:      resolveContentType(e.Response.ContentType, e.Response.Headers),
		}
	}

	return &capture.OutboundEvent{
		ID:                uuid.NewString(),
		Timestamp:         ts,
		CorrelationID:     corrID,
		CorrelationSource: corrSource,
		Service:           capture.SanitiseServiceLabel(e.Service),
		ServiceSource:     "explicit",
		DurationMS:        *e.DurationMS,
		Request:           req,
		Response:          resp,
	}
}

// writeEventsError encodes a list of validation errors as JSON and sets the
// given HTTP status. When isBatch is false, the index field is omitted.
func writeEventsError(w http.ResponseWriter, status int, errs []validationError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(eventsErrorResponse{Errors: errs})
}

// singleFieldError returns a []validationError with one element and no index.
func singleFieldError(field, message string) []validationError {
	return []validationError{{Field: field, Message: message}}
}

// fieldErrors converts a slice of "field: message" errors to validationError
// values. When idx is non-nil the index is set on each element (batch path);
// when nil the index field is omitted (single-object path).
func fieldErrors(idx *int, errs []error) []validationError {
	out := make([]validationError, len(errs))
	for i, e := range errs {
		field, msg, _ := strings.Cut(e.Error(), ": ")
		out[i] = validationError{Index: idx, Field: field, Message: msg}
	}
	return out
}

// parseAndBuild parses a single raw JSON item, validates it, and constructs
// the corresponding capture.Record. idx is set on any returned validation
// errors (pass nil for single-object payloads). Returns nil errs on success.
func parseAndBuild(item json.RawMessage, idx *int, bodyCap int, now time.Time, counters *EventsCounters) (capture.Record, []validationError) {
	// Unmarshal into the response wire shape first; it contains the type
	// discriminator and all response-event fields. For outbound events we
	// re-unmarshal below — the extra JSON decode is negligible versus the
	// allocation budget of the record itself.
	var re wireResponseEvent
	if err := json.Unmarshal(item, &re); err != nil {
		incRejectedSafe(counters, RejectReasonInvalidJSON)
		return nil, []validationError{{Index: idx, Field: "body", Message: err.Error()}}
	}

	switch eventType(re.Type) {
	case "":
		incRejectedSafe(counters, RejectReasonMissingType)
		return nil, []validationError{{Index: idx, Field: "type", Message: "required"}}
	case eventTypeResponse:
		if errs := validateResponseEvent(re); len(errs) > 0 {
			incRejectedSafe(counters, RejectReasonMissingRequiredField)
			return nil, fieldErrors(idx, errs)
		}
		if bodyCap > 0 && len(re.Body) > bodyCap {
			incRejectedSafe(counters, RejectReasonBodyTooLarge)
			return nil, []validationError{{Index: idx, Field: "body", Message: "body too large"}}
		}
		return buildResponseRecord(re, bodyCap, now), nil
	case eventTypeOutbound:
		var oe wireOutboundEvent
		if err := json.Unmarshal(item, &oe); err != nil {
			incRejectedSafe(counters, RejectReasonInvalidJSON)
			return nil, []validationError{{Index: idx, Field: "body", Message: err.Error()}}
		}
		if errs := validateOutboundEvent(oe); len(errs) > 0 {
			incRejectedSafe(counters, RejectReasonMissingRequiredField)
			return nil, fieldErrors(idx, errs)
		}
		if bodyCap > 0 && oe.Request != nil && len(oe.Request.Body) > bodyCap {
			incRejectedSafe(counters, RejectReasonBodyTooLarge)
			return nil, []validationError{{Index: idx, Field: "request.body", Message: "body too large"}}
		}
		if bodyCap > 0 && oe.Response != nil && len(oe.Response.Body) > bodyCap {
			incRejectedSafe(counters, RejectReasonBodyTooLarge)
			return nil, []validationError{{Index: idx, Field: "response.body", Message: "body too large"}}
		}
		return buildOutboundRecord(oe, bodyCap, now), nil
	default:
		incRejectedSafe(counters, RejectReasonUnknownType)
		return nil, []validationError{{Index: idx, Field: "type", Message: fmt.Sprintf("unknown type %q", re.Type)}}
	}
}

// incRejectedSafe increments a rejection counter, ignoring a nil receiver.
func incRejectedSafe(c *EventsCounters, reason string) {
	if c != nil {
		c.incRejected(reason)
	}
}

// incIngestedSafe increments the appropriate ingested counter based on the
// concrete record type, ignoring a nil receiver.
func incIngestedSafe(c *EventsCounters, rec capture.Record) {
	if c == nil {
		return
	}
	switch rec.(type) {
	case *capture.ResponseEvent:
		c.incIngestedResponse()
	case *capture.OutboundEvent:
		c.incIngestedOutbound()
	}
}

// incDroppedQueueFullSafe increments the queue-full drop counter, ignoring a nil receiver.
func incDroppedQueueFullSafe(c *EventsCounters) {
	if c != nil {
		c.incDroppedQueueFull()
	}
}

// eventsHandler returns an http.HandlerFunc for POST /events. queue may be nil
// (the handler returns 503); counters may be nil (increments are skipped).
// maxBatch limits the number of items in an array batch; zero disables the count cap.
func eventsHandler(queue *capture.Queue, bodyCap, maxPayload, maxBatch int, counters *EventsCounters) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Payload size cap: reject before reading the body further.
		if maxPayload > 0 && r.ContentLength > int64(maxPayload) {
			incRejectedSafe(counters, RejectReasonPayloadTooLarge)
			writeEventsError(w, http.StatusRequestEntityTooLarge, singleFieldError("body", "payload too large"))
			return
		}

		// Read body up to maxPayload+1 to detect over-size even when Content-Length
		// is absent or inaccurate.
		bodyReader := io.Reader(r.Body)
		if maxPayload > 0 {
			bodyReader = http.MaxBytesReader(w, r.Body, int64(maxPayload))
		}

		rawBody, err := io.ReadAll(bodyReader)
		if err != nil {
			incRejectedSafe(counters, RejectReasonPayloadTooLarge)
			writeEventsError(w, http.StatusRequestEntityTooLarge, singleFieldError("body", "payload too large"))
			return
		}

		if queue == nil {
			http.Error(w, "events pipeline not configured", http.StatusServiceUnavailable)
			return
		}

		now := time.Now()

		// Detect batch (array) vs single-object by finding the first non-whitespace byte.
		isBatch := false
		for _, b := range rawBody {
			if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
				continue
			}
			isBatch = b == '['
			break
		}

		if isBatch {
			var rawItems []json.RawMessage
			if err := json.Unmarshal(rawBody, &rawItems); err != nil {
				incRejectedSafe(counters, RejectReasonInvalidJSON)
				writeEventsError(w, http.StatusBadRequest, singleFieldError("body", err.Error()))
				return
			}
			if len(rawItems) == 0 {
				incRejectedSafe(counters, RejectReasonEmptyBatch)
				writeEventsError(w, http.StatusBadRequest, singleFieldError("body", "empty batch"))
				return
			}
			if maxBatch > 0 && len(rawItems) > maxBatch {
				incRejectedSafe(counters, RejectReasonBatchTooLarge)
				writeEventsError(w, http.StatusRequestEntityTooLarge, singleFieldError("body", "batch too large"))
				return
			}
			// First pass: validate all. Nothing enqueues until all pass.
			records := make([]capture.Record, 0, len(rawItems))
			for i, item := range rawItems {
				idx := i
				rec, verrs := parseAndBuild(item, &idx, bodyCap, now, counters)
				if verrs != nil {
					writeEventsError(w, http.StatusBadRequest, verrs)
					return
				}
				records = append(records, rec)
			}
			accepted, dropped := 0, 0
			for _, rec := range records {
				if queue.Enqueue(rec) {
					incIngestedSafe(counters, rec)
					accepted++
				} else {
					incDroppedQueueFullSafe(counters)
					dropped++
				}
			}
			switch {
			case dropped == 0:
				w.WriteHeader(http.StatusAccepted)
			case accepted > 0:
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				_ = json.NewEncoder(w).Encode(struct {
					Accepted int `json:"accepted"`
					Dropped  int `json:"dropped"`
				}{Accepted: accepted, Dropped: dropped})
			default:
				writeEventsError(w, http.StatusServiceUnavailable, singleFieldError("queue", "queue full"))
			}
		} else {
			rec, verrs := parseAndBuild(rawBody, nil, bodyCap, now, counters)
			if verrs != nil {
				writeEventsError(w, http.StatusBadRequest, verrs)
				return
			}
			if queue.Enqueue(rec) {
				incIngestedSafe(counters, rec)
				w.WriteHeader(http.StatusAccepted)
			} else {
				incDroppedQueueFullSafe(counters)
				writeEventsError(w, http.StatusServiceUnavailable, singleFieldError("queue", "queue full"))
			}
		}
	}
}
