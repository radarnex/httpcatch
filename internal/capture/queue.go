package capture

import "sync/atomic"

// Queue is a bounded, non-blocking buffered channel for CapturedRecords.
// When the queue is full, Enqueue drops the record and increments the
// dropped counter. Callers do not implement the drop policy; the queue owns it.
type Queue struct {
	c        chan *CapturedRecord
	capacity int
	dropped  atomic.Uint64
}

func NewQueue(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{
		c:        make(chan *CapturedRecord, capacity),
		capacity: capacity,
	}
}

// Enqueue attempts a non-blocking send. Returns true on success.
// On a full queue, increments dropped_total and returns false.
func (q *Queue) Enqueue(r *CapturedRecord) bool {
	select {
	case q.c <- r:
		return true
	default:
		q.dropped.Add(1)
		return false
	}
}

func (q *Queue) Receive() <-chan *CapturedRecord { return q.c }

func (q *Queue) Capacity() int { return q.capacity }

func (q *Queue) Len() int { return len(q.c) }

func (q *Queue) DroppedTotal() uint64 { return q.dropped.Load() }

// Close signals consumers that no further records will arrive.
func (q *Queue) Close() { close(q.c) }
