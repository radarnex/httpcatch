package sinks

import (
	"context"

	"github.com/radarnex/httpcatch/internal/capture"
)

// Sink implementations must be safe for concurrent Write from the worker pool.
type Sink interface {
	Name() string
	Write(ctx context.Context, r capture.Record) error
}
