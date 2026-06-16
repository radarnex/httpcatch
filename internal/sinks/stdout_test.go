package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
)

func TestStdoutSink_EmitsRequestWithKind(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewWriterSink(&buf)

	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	rec := &capture.CapturedRequest{
		ID:                "rec-1",
		Timestamp:         ts,
		Method:            "POST",
		Path:              "/api/x",
		Query:             map[string][]string{"k": {"v"}},
		Headers:           map[string][]string{"Content-Type": {"text/plain"}},
		Cookies:           []capture.Cookie{{Name: "session", Value: "abc"}},
		Body:              []byte("hello"),
		BodyTruncated:     false,
		BodyOriginalSize:  5,
		ContentType:       "text/plain",
		SourceIP:          "10.0.0.1",
		Service:           "orders",
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationID:     "corr-1",
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}

	if err := sink.Write(context.Background(), rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("expected trailing newline, got %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("expected exactly one line, got %d", strings.Count(out, "\n"))
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\nline: %s", err, out)
	}
	var kind string
	if err := json.Unmarshal(decoded["kind"], &kind); err != nil {
		t.Fatalf("unmarshal kind: %v", err)
	}
	if kind != string(capture.KindRequest) {
		t.Errorf("kind: got %q want %q", kind, capture.KindRequest)
	}
	var id string
	if err := json.Unmarshal(decoded["id"], &id); err != nil {
		t.Fatalf("unmarshal id: %v", err)
	}
	if id != "rec-1" {
		t.Errorf("id: got %q want %q", id, "rec-1")
	}
}

func TestStdoutSink_EmitsResponseEventWithKind(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewWriterSink(&buf)

	rec := &capture.ResponseEvent{
		ID:            "evt-1",
		Timestamp:     time.Now().UTC(),
		CorrelationID: "corr-1",
		Service:       "users",
		ServiceSource: capture.ServiceSourceHeader,
		Status:        200,
		Headers:       map[string][]string{"Content-Type": {"application/json"}},
		Body:          []byte(`{"ok":true}`),
		DurationMS:    42,
	}

	if err := sink.Write(context.Background(), rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimRight(buf.String(), "\n")), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var kind string
	_ = json.Unmarshal(decoded["kind"], &kind)
	if kind != string(capture.KindResponseEvent) {
		t.Errorf("kind: got %q want %q", kind, capture.KindResponseEvent)
	}
}

func TestStdoutSink_EmitsOutboundEventWithKind(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewWriterSink(&buf)

	rec := &capture.OutboundEvent{
		ID:            "out-1",
		Timestamp:     time.Now().UTC(),
		CorrelationID: "corr-1",
		Service:       "users",
		ServiceSource: capture.ServiceSourceHeader,
		DurationMS:    10,
		Request: capture.OutboundRequestHalf{
			Method: "POST",
			Path:   "/payments",
		},
	}

	if err := sink.Write(context.Background(), rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimRight(buf.String(), "\n")), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var kind string
	_ = json.Unmarshal(decoded["kind"], &kind)
	if kind != string(capture.KindOutboundEvent) {
		t.Errorf("kind: got %q want %q", kind, capture.KindOutboundEvent)
	}
}

func TestStdoutSink_ConcurrentWritesNotInterleaved(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewWriterSink(&buf)

	const n = 200
	done := make(chan struct{}, n)
	for range n {
		go func() {
			_ = sink.Write(context.Background(), &capture.CapturedRequest{
				ID:            "x",
				Method:        "GET",
				CorrelationID: "c",
			})
			done <- struct{}{}
		}()
	}
	for range n {
		<-done
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("lines: got %d want %d", len(lines), n)
	}
	for i, line := range lines {
		var r map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", i, err, line)
		}
		if _, ok := r["kind"]; !ok {
			t.Errorf("line %d missing 'kind' field", i)
		}
	}
}
