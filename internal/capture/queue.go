package capture

import "sync/atomic"

// Queue owns the drop-on-full policy so callers cannot accidentally bypass it.
type Queue struct {
	c       chan Record
	dropped atomic.Uint64
}

func NewQueue(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{c: make(chan Record, capacity)}
}

func (q *Queue) Enqueue(r Record) bool {
	select {
	case q.c <- r:
		return true
	default:
		q.dropped.Add(1)
		return false
	}
}

func (q *Queue) Receive() <-chan Record { return q.c }

func (q *Queue) Capacity() int { return cap(q.c) }

func (q *Queue) Len() int { return len(q.c) }

func (q *Queue) DroppedTotal() uint64 { return q.dropped.Load() }

func (q *Queue) Close() { close(q.c) }
