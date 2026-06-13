package pipeline

import (
	"sync/atomic"

	"github.com/radarnex/httpcatch/internal/sinks"
)

// SinkErrorCounters tracks per-sink write failures. Workers increment by name;
// the metrics handler reads via the exported totals.
type SinkErrorCounters struct {
	memory atomic.Uint64
	sqlite atomic.Uint64
	stdout atomic.Uint64
}

func NewSinkErrorCounters() *SinkErrorCounters { return &SinkErrorCounters{} }

// IncBySinkName increments the counter matching the supplied sink name. Unknown
// names are ignored — adding a sink without wiring its counter is a programming
// error and silently dropped here rather than panicking at runtime.
func (c *SinkErrorCounters) IncBySinkName(name string) {
	switch name {
	case sinks.NameMemory:
		c.memory.Add(1)
	case sinks.NameSQLite:
		c.sqlite.Add(1)
	case sinks.NameStdout:
		c.stdout.Add(1)
	}
}

func (c *SinkErrorCounters) MemoryErrorsTotal() uint64 { return c.memory.Load() }
func (c *SinkErrorCounters) SQLiteErrorsTotal() uint64 { return c.sqlite.Load() }
func (c *SinkErrorCounters) StdoutErrorsTotal() uint64 { return c.stdout.Load() }
