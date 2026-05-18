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

func TestStdoutSink_EmitsCanonicalJSONLine(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewWriterSink(&buf)

	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	rec := &capture.CapturedRecord{
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

	var decoded capture.CapturedRecord
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\nline: %s", err, out)
	}
	if decoded.ID != rec.ID || decoded.Method != rec.Method || decoded.Path != rec.Path {
		t.Errorf("field mismatch: got %+v", decoded)
	}
	if string(decoded.Body) != "hello" {
		t.Errorf("body: got %q want %q", decoded.Body, "hello")
	}
	if !decoded.Timestamp.Equal(ts) {
		t.Errorf("timestamp: got %v want %v", decoded.Timestamp, ts)
	}
	if decoded.ServiceSource != capture.ServiceSourceHeader {
		t.Errorf("service_source: got %q want %q", decoded.ServiceSource, capture.ServiceSourceHeader)
	}
	if decoded.CorrelationSource != capture.CorrelationSourceTraceparent {
		t.Errorf("correlation_source: got %q want %q", decoded.CorrelationSource, capture.CorrelationSourceTraceparent)
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
			_ = sink.Write(context.Background(), &capture.CapturedRecord{ID: "x", Method: "GET"})
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
		var r capture.CapturedRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", i, err, line)
		}
	}
}
