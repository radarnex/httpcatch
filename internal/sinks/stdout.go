package sinks

import (
	"context"
	"encoding/json"
	"io"
	"sync"

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

func (s *StdoutSink) Write(_ context.Context, r *capture.CapturedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(r)
}
