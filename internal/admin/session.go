package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// Session represents an authenticated admin session with an opaque ID and an
// expiry time. The ID is a 32-byte random value encoded as base64-url (no
// padding); it is never logged or echoed in responses.
type Session struct {
	ID        string
	ExpiresAt time.Time
}

// SessionStore holds all active sessions in memory. Sessions are lost on
// process restart by design — no persistence is required.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
	now      func() time.Time
}

// NewSessionStore constructs an empty store. The now function is injected so
// that tests can advance time deterministically without sleeping.
func NewSessionStore(now func() time.Time) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]Session),
		now:      now,
	}
}

// Create allocates a new session with the given TTL, stores it, and returns it.
// An error is returned only if crypto/rand fails, which is a security signal.
func (s *SessionStore) Create(ttl time.Duration) (Session, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Session{}, fmt.Errorf("session: generate id: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	sess := Session{
		ID:        id,
		ExpiresAt: s.now().Add(ttl),
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return sess, nil
}

// Validate returns true when id exists in the store and has not yet expired.
func (s *SessionStore) Validate(id string) bool {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	return ok && sess.ExpiresAt.After(s.now())
}

// Revoke removes the session with the given id. Revoking an unknown id is a
// no-op.
func (s *SessionStore) Revoke(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// Sweep removes all sessions whose expiry time has passed. It is exported so
// tests can invoke it synchronously without relying on timing.
func (s *SessionStore) Sweep() {
	now := s.now()
	s.mu.Lock()
	for id, sess := range s.sessions {
		if sess.ExpiresAt.Before(now) {
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
}

// StartSweeper spawns a goroutine that calls Sweep at the given interval until
// ctx is cancelled. The goroutine exits cleanly when ctx.Done() is closed.
func (s *SessionStore) StartSweeper(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.Sweep()
			case <-ctx.Done():
				return
			}
		}
	}()
}
