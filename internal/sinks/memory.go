package sinks

import (
	"context"
	"sync"

	"github.com/radarnex/httpcatch/internal/capture"
)

const NameMemory = "memory"

// MemorySink is a bounded ring buffer of recently written records. Eviction
// on full insert is a property of the ring and is not counted — it is not a
// drop. Reads return a snapshot consistent at call time without removing
// records from the ring.
//
// An auxiliary map from correlation_id to the ring position of the most
// recently written record for that id provides O(1) lookup for later slices
// (orphan detection, detail-by-correlation). The map is not bounded
// independently; it shrinks naturally when positions are evicted from the ring.
type MemorySink struct {
	mu       sync.Mutex
	buf      []capture.Record
	head     int
	size     int
	capacity int
	// corrIdx maps correlation_id to the ring index of the record most recently
	// written for that id. Used by later slices for O(1) orphan detection.
	corrIdx map[string]int
}

func NewMemorySink(capacity int) *MemorySink {
	if capacity < 1 {
		capacity = 1
	}
	return &MemorySink{
		buf:      make([]capture.Record, capacity),
		capacity: capacity,
		corrIdx:  make(map[string]int),
	}
}

func (s *MemorySink) Name() string { return NameMemory }

func (s *MemorySink) Capacity() int { return s.capacity }

func (s *MemorySink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

func (s *MemorySink) Write(_ context.Context, r capture.Record) error {
	s.mu.Lock()
	pos := s.head
	// If this position is being evicted, clean up any stale corrIdx entry
	// that points at it so the index stays accurate.
	if s.size == s.capacity {
		if old := s.buf[pos]; old != nil {
			cid := old.RecordCorrelationID()
			if s.corrIdx[cid] == pos {
				delete(s.corrIdx, cid)
			}
		}
	}
	s.buf[pos] = r
	s.corrIdx[r.RecordCorrelationID()] = pos
	s.head = (s.head + 1) % s.capacity
	if s.size < s.capacity {
		s.size++
	}
	s.mu.Unlock()
	return nil
}

// Recent returns up to n most recent records in newest-first order. The
// returned slice is a fresh snapshot; subsequent writes do not mutate it.
func (s *MemorySink) Recent(n int) []capture.Record {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > s.size {
		n = s.size
	}
	out := make([]capture.Record, n)
	idx := (s.head - 1 + s.capacity) % s.capacity
	for i := 0; i < n; i++ {
		out[i] = s.buf[idx]
		idx = (idx - 1 + s.capacity) % s.capacity
	}
	return out
}

// ByCorrelationID returns the most recently written record for the given
// correlation_id, or nil if none is present. O(1) lookup via the auxiliary
// index.
func (s *MemorySink) ByCorrelationID(correlationID string) capture.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos, ok := s.corrIdx[correlationID]
	if !ok {
		return nil
	}
	return s.buf[pos]
}
