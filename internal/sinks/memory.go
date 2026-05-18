package sinks

import (
	"context"
	"sync"

	"github.com/radarnex/httpcatch/internal/capture"
)

const NameMemory = "memory"

// MemorySink is a bounded ring buffer of recently captured records. Eviction
// on full insert is a property of the ring and is not counted — it is not a
// drop. Reads return a snapshot consistent at call time without removing
// records from the ring.
type MemorySink struct {
	mu       sync.Mutex
	buf      []*capture.CapturedRecord
	head     int
	size     int
	capacity int
}

func NewMemorySink(capacity int) *MemorySink {
	if capacity < 1 {
		capacity = 1
	}
	return &MemorySink{
		buf:      make([]*capture.CapturedRecord, capacity),
		capacity: capacity,
	}
}

func (s *MemorySink) Name() string { return NameMemory }

func (s *MemorySink) Capacity() int { return s.capacity }

func (s *MemorySink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

func (s *MemorySink) Write(_ context.Context, r *capture.CapturedRecord) error {
	s.mu.Lock()
	s.buf[s.head] = r
	s.head = (s.head + 1) % s.capacity
	if s.size < s.capacity {
		s.size++
	}
	s.mu.Unlock()
	return nil
}

// Recent returns up to n most recent records in newest-first order. The
// returned slice is a fresh snapshot; subsequent writes do not mutate it.
func (s *MemorySink) Recent(n int) []*capture.CapturedRecord {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > s.size {
		n = s.size
	}
	out := make([]*capture.CapturedRecord, n)
	idx := (s.head - 1 + s.capacity) % s.capacity
	for i := 0; i < n; i++ {
		out[i] = s.buf[idx]
		idx = (idx - 1 + s.capacity) % s.capacity
	}
	return out
}
