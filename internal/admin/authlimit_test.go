package admin_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
)

// newTestLimiter returns a limiter backed by the given clock function.
func newTestLimiter(now func() time.Time) *admin.AuthLimiter {
	return admin.NewAuthLimiterWithClock(now)
}

func TestAuthLimiter_PerIPBucketDepletes(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newTestLimiter(func() time.Time { return now })

	ip := "203.0.113.1"

	// 5 failures should be allowed.
	for i := range 5 {
		if !lim.Allowed(ip) {
			t.Fatalf("failure %d: Allowed returned false before exhaustion", i+1)
		}
		lim.RegisterFailure(ip)
	}

	// 6th attempt must be blocked.
	if lim.Allowed(ip) {
		t.Fatal("Allowed returned true after 5 failures — bucket should be exhausted")
	}
}

func TestAuthLimiter_PerIPRefillsOverWindow(t *testing.T) {
	t.Parallel()

	var wallNow time.Time
	lim := newTestLimiter(func() time.Time { return wallNow })

	ip := "203.0.113.2"
	wallNow = time.Now()

	// Exhaust the bucket.
	for range 5 {
		lim.RegisterFailure(ip)
	}
	if lim.Allowed(ip) {
		t.Fatal("bucket should be exhausted after 5 failures")
	}

	// Advance clock by the full per-IP window; bucket must be fully replenished.
	wallNow = wallNow.Add(admin.AuthLimitPerIPWindow)

	if !lim.Allowed(ip) {
		t.Fatal("Allowed returned false after full window elapsed — bucket should be full")
	}
}

func TestAuthLimiter_LoopbackRateLimited(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newTestLimiter(func() time.Time { return now })

	ip := "127.0.0.1"
	for range 5 {
		if !lim.Allowed(ip) {
			t.Fatal("loopback should be allowed before the bucket is exhausted")
		}
		lim.RegisterFailure(ip)
	}
	if lim.Allowed(ip) {
		t.Fatal("loopback should be rate-limited after repeated failures")
	}
}

func TestAuthLimiter_GlobalCap(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newTestLimiter(func() time.Time { return now })

	// 20 distinct IPs each contributing one failure exhaust the global bucket.
	for i := range 20 {
		ip := fmt.Sprintf("203.0.113.%d", i+1)
		if !lim.Allowed(ip) {
			t.Fatalf("ip=%s: Allowed false before global exhaustion (failure %d)", ip, i+1)
		}
		lim.RegisterFailure(ip)
	}

	// A 21st distinct IP (no per-IP failures yet) must be blocked by global cap.
	freshIP := "198.51.100.1"
	if lim.Allowed(freshIP) {
		t.Fatal("fresh IP was allowed after global bucket exhausted")
	}
}

func TestAuthLimiter_SuccessfulAuthDoesNotConsume(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newTestLimiter(func() time.Time { return now })

	ip := "203.0.113.3"

	// Calling Allowed repeatedly without RegisterFailure must not consume budget.
	for i := range 100 {
		if !lim.Allowed(ip) {
			t.Fatalf("call %d: Allowed returned false without any RegisterFailure call", i+1)
		}
	}
}

func TestAuthLimiter_RetryAfterReasonable(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newTestLimiter(func() time.Time { return now })

	ip := "203.0.113.4"

	// Exhaust the per-IP bucket.
	for range 5 {
		lim.RegisterFailure(ip)
	}

	d := lim.RetryAfter(ip)
	if d <= 0 {
		t.Fatalf("RetryAfter returned %v; want > 0", d)
	}
	// Must be at most the full per-IP window.
	if d > admin.AuthLimitPerIPWindow {
		t.Fatalf("RetryAfter returned %v; want <= %v", d, admin.AuthLimitPerIPWindow)
	}
}

func TestAuthLimiter_ClientIP_StripsPort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		remoteAddr string
		wantIP     string
	}{
		{"203.0.113.5:4321", "203.0.113.5"},
		{"[::1]:12345", "::1"},
		{"127.0.0.1:0", "127.0.0.1"},
	}
	for _, c := range cases {
		got := admin.ClientIP(c.remoteAddr)
		if got != c.wantIP {
			t.Errorf("ClientIP(%q) = %q; want %q", c.remoteAddr, got, c.wantIP)
		}
	}
}

func TestAuthLimiter_ClientIP_NoPort(t *testing.T) {
	t.Parallel()

	// When no port is present, raw value is returned unchanged.
	raw := "203.0.113.6"
	got := admin.ClientIP(raw)
	if got != raw {
		t.Errorf("ClientIP(%q) = %q; want %q", raw, got, raw)
	}
}

func TestAuthLimiter_CSRFBlockedCounter(t *testing.T) {
	t.Parallel()

	lim := admin.NewAuthLimiter()
	lim.RecordCSRFBlocked()
	lim.RecordCSRFBlocked()
	lim.RecordCSRFBlocked()

	if got := lim.CSRFBlockedTotal(); got != 3 {
		t.Errorf("CSRFBlockedTotal: got %d want 3", got)
	}
}
