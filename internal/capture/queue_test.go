package capture

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueue_ConcurrentEnqueueAndDrops(t *testing.T) {
	t.Parallel()

	const (
		producers = 16
		perProd   = 500
		capacity  = 8
	)

	q := NewQueue(capacity)

	var consumed atomic.Uint64
	consumerDone := make(chan struct{})
	stopConsumer := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-stopConsumer:
				return
			case r, ok := <-q.Receive():
				if !ok {
					return
				}
				if r == nil {
					t.Errorf("nil record received")
				}
				consumed.Add(1)
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	invariantStop := make(chan struct{})
	invariantDone := make(chan struct{})
	var capacityViolated atomic.Bool
	go func() {
		defer close(invariantDone)
		for {
			select {
			case <-invariantStop:
				return
			default:
			}
			if q.Len() > capacity {
				capacityViolated.Store(true)
				return
			}
		}
	}()

	var enqueued atomic.Uint64
	var wg sync.WaitGroup
	for range producers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perProd {
				if q.Enqueue(&CapturedRequest{ID: "test"}) {
					enqueued.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	close(invariantStop)
	<-invariantDone

	deadline := time.Now().Add(5 * time.Second)
	for consumed.Load() < enqueued.Load() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	close(stopConsumer)
	<-consumerDone

	if capacityViolated.Load() {
		t.Fatalf("queue length exceeded capacity %d at some sample", capacity)
	}

	total := uint64(producers * perProd)
	if got := enqueued.Load() + q.DroppedTotal(); got != total {
		t.Fatalf("enqueued + dropped: got %d, want %d (enqueued=%d, dropped=%d)",
			got, total, enqueued.Load(), q.DroppedTotal())
	}
	if consumed.Load() != enqueued.Load() {
		t.Fatalf("consumed=%d, enqueued=%d; expected equal after drain",
			consumed.Load(), enqueued.Load())
	}
	if q.DroppedTotal() == 0 {
		t.Logf("warning: no drops occurred; capacity=%d may be too large to force drops", capacity)
	}
}

func TestQueue_Enqueue_DropsAndCounter(t *testing.T) {
	t.Parallel()

	q := NewQueue(2)
	if !q.Enqueue(&CapturedRequest{}) {
		t.Fatal("first enqueue should succeed")
	}
	if !q.Enqueue(&CapturedRequest{}) {
		t.Fatal("second enqueue should succeed")
	}
	if q.Enqueue(&CapturedRequest{}) {
		t.Fatal("third enqueue should drop")
	}
	if got := q.DroppedTotal(); got != 1 {
		t.Fatalf("dropped: got %d want 1", got)
	}
	if got := q.Enqueue(&CapturedRequest{}); got {
		t.Fatal("fourth enqueue should also drop")
	}
	if got := q.DroppedTotal(); got != 2 {
		t.Fatalf("dropped: got %d want 2", got)
	}
}

func TestQueue_CapacityFloorAtOne(t *testing.T) {
	t.Parallel()

	q := NewQueue(0)
	if q.Capacity() != 1 {
		t.Fatalf("capacity floor: got %d want 1", q.Capacity())
	}
}

func TestQueue_AllVariantsEnqueueAndDequeue(t *testing.T) {
	t.Parallel()

	q := NewQueue(10)

	variants := []Record{
		&CapturedRequest{ID: "req-1", CorrelationID: "c1"},
		&ResponseEvent{ID: "resp-1", CorrelationID: "c2"},
		&OutboundEvent{ID: "out-1", CorrelationID: "c3"},
	}

	for _, v := range variants {
		if !q.Enqueue(v) {
			t.Fatalf("Enqueue %T: expected true", v)
		}
	}

	if q.Len() != 3 {
		t.Fatalf("Len: got %d want 3", q.Len())
	}

	wantKinds := []RecordKind{KindRequest, KindResponseEvent, KindOutboundEvent}
	for i, wantKind := range wantKinds {
		got := <-q.Receive()
		if got.Kind() != wantKind {
			t.Errorf("item %d: Kind got %q want %q", i, got.Kind(), wantKind)
		}
	}
}
