package sinks

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/radarnex/httpcatch/internal/capture"
)

// StdoutSink emits one JSON object per line. The encoder appends a newline.
// Writes are serialized so concurrent workers cannot interleave bytes.
type StdoutSink struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewStdoutSink() *StdoutSink {
	return NewWriterSink(os.Stdout)
}

func NewWriterSink(w io.Writer) *StdoutSink {
	return &StdoutSink{enc: json.NewEncoder(w)}
}

func (s *StdoutSink) Name() string { return "stdout" }

func (s *StdoutSink) Write(_ context.Context, r *capture.CapturedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(r)
}
