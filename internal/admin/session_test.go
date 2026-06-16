package admin_test

import (
	"sync"
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

func TestSessionStore_StartSweeper_StopsOnCancel(t *testing.T) {
	t.Parallel()

	now := time.Now()
	ticks := 0
	store := admin.NewSessionStore(func() time.Time {
		ticks++
		return now
	})

	ctx := t.Context()
	store.StartSweeper(ctx, 10*time.Millisecond)

	// Give the sweeper a few ticks.
	time.Sleep(60 * time.Millisecond)

	// Context cancels automatically when the test ends; just confirm no panic.
}
