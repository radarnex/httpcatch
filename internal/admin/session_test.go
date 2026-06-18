package admin_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
)

func TestSessionStore_CreateReturnsUniqueIDs(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		sess, err := store.Create(time.Hour)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, dup := seen[sess.ID]; dup {
			t.Fatalf("duplicate session ID: %s", sess.ID)
		}
		seen[sess.ID] = struct{}{}
	}
}

func TestSessionStore_ValidateFreshSession(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	sess, err := store.Create(time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !store.Validate(sess.ID) {
		t.Error("Validate: expected true for a fresh session")
	}
}

func TestSessionStore_ValidateUnknownID(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	if store.Validate("does-not-exist") {
		t.Error("Validate: expected false for an unknown ID")
	}
}

func TestSessionStore_ValidateExpiredSession(t *testing.T) {
	t.Parallel()

	now := time.Now()
	clock := &now
	store := admin.NewSessionStore(func() time.Time { return *clock })

	sess, err := store.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !store.Validate(sess.ID) {
		t.Fatal("Validate: expected true before expiry")
	}

	// Advance the clock past expiry.
	future := now.Add(2 * time.Minute)
	clock = &future

	if store.Validate(sess.ID) {
		t.Error("Validate: expected false after expiry")
	}
}

func TestSessionStore_SweepRemovesExpired(t *testing.T) {
	t.Parallel()

	now := time.Now()
	clock := &now
	store := admin.NewSessionStore(func() time.Time { return *clock })

	expired, err := store.Create(time.Minute)
	if err != nil {
		t.Fatalf("Create expired: %v", err)
	}

	future := now.Add(10 * time.Minute)
	clock = &future

	// Create an active session with a long TTL relative to the new clock.
	active, err := store.Create(time.Hour)
	if err != nil {
		t.Fatalf("Create active: %v", err)
	}

	store.Sweep()

	if store.Validate(expired.ID) {
		t.Error("Sweep: expired session still validates after sweep")
	}
	if !store.Validate(active.ID) {
		t.Error("Sweep: active session was wrongly removed")
	}
}

func TestSessionStore_RevokeUnknownIDIsNoOp(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	// Should not panic.
	store.Revoke("ghost-id")
}

func TestSessionStore_RevokeRemovesSession(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	sess, err := store.Create(time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store.Revoke(sess.ID)
	if store.Validate(sess.ID) {
		t.Error("Validate: expected false after Revoke")
	}
}

func TestSessionStore_ConcurrentOps(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	const goroutines = 20
	const ops = 50

	var wg sync.WaitGroup
	ids := make(chan string, goroutines*ops)

	for range goroutines {
		wg.Go(func() {
			for range ops {
				sess, err := store.Create(time.Hour)
				if err != nil {
					return
				}
				ids <- sess.ID
				store.Validate(sess.ID)
			}
		})
	}

	wg.Wait()
	close(ids)

	for id := range ids {
		store.Revoke(id)
	}
}

// TestSessionStore_StartSweeper_Ticks asserts that StartSweeper launches a
// goroutine that calls Sweep on schedule. It deliberately does NOT assert the
// goroutine exits on cancel — proving exit deterministically needs a
// production-side signal (see the plan's Deferred section). cancel() here is a
// smoke check that cancellation does not panic, not a leak assertion.
func TestSessionStore_StartSweeper_Ticks(t *testing.T) {
	t.Parallel()

	now := time.Now()
	// ticks counts sweeper iterations: the sweeper calls now() once per Sweep,
	// and no other call path touches now() in this test. It is written by the
	// sweeper goroutine and read by the test, so it must be atomic.
	var ticks atomic.Int64
	store := admin.NewSessionStore(func() time.Time {
		ticks.Add(1)
		return now
	})

	ctx, cancel := context.WithCancel(t.Context())
	store.StartSweeper(ctx, time.Millisecond)

	// Assert the sweeper actually runs: poll (bounded) until the first tick lands
	// instead of sleeping a fixed duration and hoping. Fails fast if the sweeper
	// goroutine never ticks.
	deadline := time.Now().Add(2 * time.Second)
	for ticks.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("sweeper did not tick within 2s; StartSweeper goroutine never ran")
		}
		time.Sleep(time.Millisecond)
	}

	// Smoke check: cancelling must not panic. This does not prove the goroutine
	// returns (see Deferred); it only exercises the cancel call.
	cancel()
}
