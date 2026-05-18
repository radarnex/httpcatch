package sinks

import (
	"context"

	"github.com/radarnex/httpcatch/internal/capture"
)

// Sink is a storage destination for captured records.
// Implementations must be safe for concurrent calls from a worker pool.
type Sink interface {
	Name() string
	Write(ctx context.Context, r *capture.CapturedRecord) error
}
