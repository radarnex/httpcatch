package redact

import "github.com/radarnex/httpcatch/internal/capture"

// Redactor transforms a captured record before sink fan-out.
// Implementations may mutate and return the same record or return a new one.
type Redactor interface {
	Redact(*capture.CapturedRecord) *capture.CapturedRecord
}

// NoOp returns each record unchanged. It signals unredacted mode and the
// process must emit a prominent startup warning when this redactor is wired.
type NoOp struct{}

func (NoOp) Redact(r *capture.CapturedRecord) *capture.CapturedRecord { return r }
