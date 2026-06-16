package sinks

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
)

// NameStdout is the canonical identifier for the stdout sink across config,
// metrics, and logs.
const NameStdout = "stdout"

// StdoutSink serializes Write calls so concurrent worker writes cannot
// interleave bytes on the underlying writer.
type StdoutSink struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewWriterSink(w io.Writer) *StdoutSink {
	return &StdoutSink{enc: json.NewEncoder(w)}
}

func (s *StdoutSink) Name() string { return NameStdout }

func (s *StdoutSink) Write(_ context.Context, r capture.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Each variant is emitted as a flat JSON object with a top-level "kind"
	// discriminator. The envelope structs mirror the canonical wire shape.
	switch v := r.(type) {
	case *capture.CapturedRequest:
		return s.enc.Encode(requestEnvelope(v))
	case *capture.ResponseEvent:
		return s.enc.Encode(responseEnvelope(v))
	case *capture.OutboundEvent:
		return s.enc.Encode(outboundEnvelope(v))
	default:
		return s.enc.Encode(r)
	}
}

// requestEnvelopeT is the stable JSON shape for the request variant.
type requestEnvelopeT struct {
	Kind              capture.RecordKind  `json:"kind"`
	ID                string              `json:"id"`
	Timestamp         time.Time           `json:"timestamp"`
	Method            string              `json:"method"`
	Path              string              `json:"path"`
	Query             map[string][]string `json:"query"`
	Headers           map[string][]string `json:"headers"`
	Cookies           []capture.Cookie    `json:"cookies"`
	Body              string              `json:"body"`
	BodyTruncated     bool                `json:"body_truncated"`
	BodyOriginalSize  int                 `json:"body_original_size"`
	ContentType       string              `json:"content_type"`
	SourceIP          string              `json:"source_ip"`
	Service           string              `json:"service"`
	ServiceSource     string              `json:"service_source"`
	CorrelationID     string              `json:"correlation_id"`
	CorrelationSource string              `json:"correlation_source"`
}

func requestEnvelope(r *capture.CapturedRequest) requestEnvelopeT {
	return requestEnvelopeT{
		Kind:              capture.KindRequest,
		ID:                r.ID,
		Timestamp:         r.Timestamp,
		Method:            r.Method,
		Path:              r.Path,
		Query:             r.Query,
		Headers:           r.Headers,
		Cookies:           r.Cookies,
		Body:              string(r.Body),
		BodyTruncated:     r.BodyTruncated,
		BodyOriginalSize:  r.BodyOriginalSize,
		ContentType:       r.ContentType,
		SourceIP:          r.SourceIP,
		Service:           r.Service,
		ServiceSource:     r.ServiceSource,
		CorrelationID:     r.CorrelationID,
		CorrelationSource: r.CorrelationSource,
	}
}

// responseEnvelopeT is the stable JSON shape for the response_event variant.
type responseEnvelopeT struct {
	Kind              capture.RecordKind  `json:"kind"`
	ID                string              `json:"id"`
	Timestamp         time.Time           `json:"timestamp"`
	CorrelationID     string              `json:"correlation_id"`
	CorrelationSource string              `json:"correlation_source"`
	Service           string              `json:"service"`
	ServiceSource     string              `json:"service_source"`
	Status            int                 `json:"status"`
	Headers           map[string][]string `json:"headers"`
	Body              string              `json:"body"`
	BodyTruncated     bool                `json:"body_truncated"`
	BodyOriginalSize  int                 `json:"body_original_size"`
	ContentType       string              `json:"content_type"`
	DurationMS        int64               `json:"duration_ms"`
}

func responseEnvelope(r *capture.ResponseEvent) responseEnvelopeT {
	return responseEnvelopeT{
		Kind:              capture.KindResponseEvent,
		ID:                r.ID,
		Timestamp:         r.Timestamp,
		CorrelationID:     r.CorrelationID,
		CorrelationSource: r.CorrelationSource,
		Service:           r.Service,
		ServiceSource:     r.ServiceSource,
		Status:            r.Status,
		Headers:           r.Headers,
		Body:              string(r.Body),
		BodyTruncated:     r.BodyTruncated,
		BodyOriginalSize:  r.BodyOriginalSize,
		ContentType:       r.ContentType,
		DurationMS:        r.DurationMS,
	}
}

type outboundRequestHalfEnvT struct {
	Method           string              `json:"method"`
	Path             string              `json:"path"`
	Headers          map[string][]string `json:"headers"`
	Body             string              `json:"body"`
	BodyTruncated    bool                `json:"body_truncated"`
	BodyOriginalSize int                 `json:"body_original_size"`
	ContentType      string              `json:"content_type"`
}

type outboundResponseHalfEnvT struct {
	Status           int                 `json:"status"`
	Headers          map[string][]string `json:"headers"`
	Body             string              `json:"body"`
	BodyTruncated    bool                `json:"body_truncated"`
	BodyOriginalSize int                 `json:"body_original_size"`
	ContentType      string              `json:"content_type"`
}

// outboundEnvelopeT is the stable JSON shape for the outbound_event variant.
type outboundEnvelopeT struct {
	Kind              capture.RecordKind        `json:"kind"`
	ID                string                    `json:"id"`
	Timestamp         time.Time                 `json:"timestamp"`
	CorrelationID     string                    `json:"correlation_id"`
	CorrelationSource string                    `json:"correlation_source"`
	Service           string                    `json:"service"`
	ServiceSource     string                    `json:"service_source"`
	DurationMS        int64                     `json:"duration_ms"`
	Request           outboundRequestHalfEnvT   `json:"request"`
	Response          *outboundResponseHalfEnvT `json:"response"`
}

func outboundEnvelope(r *capture.OutboundEvent) outboundEnvelopeT {
	env := outboundEnvelopeT{
		Kind:              capture.KindOutboundEvent,
		ID:                r.ID,
		Timestamp:         r.Timestamp,
		CorrelationID:     r.CorrelationID,
		CorrelationSource: r.CorrelationSource,
		Service:           r.Service,
		ServiceSource:     r.ServiceSource,
		DurationMS:        r.DurationMS,
		Request: outboundRequestHalfEnvT{
			Method:           r.Request.Method,
			Path:             r.Request.Path,
			Headers:          r.Request.Headers,
			Body:             string(r.Request.Body),
			BodyTruncated:    r.Request.BodyTruncated,
			BodyOriginalSize: r.Request.BodyOriginalSize,
			ContentType:      r.Request.ContentType,
		},
	}
	if r.Response != nil {
		env.Response = &outboundResponseHalfEnvT{
			Status:           r.Response.Status,
			Headers:          r.Response.Headers,
			Body:             string(r.Response.Body),
			BodyTruncated:    r.Response.BodyTruncated,
			BodyOriginalSize: r.Response.BodyOriginalSize,
			ContentType:      r.Response.ContentType,
		}
	}
	return env
}
