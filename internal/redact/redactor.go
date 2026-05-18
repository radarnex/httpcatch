package redact

import "github.com/radarnex/httpcatch/internal/capture"

type Redactor interface {
	Redact(*capture.CapturedRecord) *capture.CapturedRecord
}

// NoOp is the seam the redaction slice will replace; wiring it triggers the
// unredacted-mode startup warning.
type NoOp struct{}

func (NoOp) Redact(r *capture.CapturedRecord) *capture.CapturedRecord { return r }
