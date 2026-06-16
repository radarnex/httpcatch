package admin

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Token-bucket parameters for admin auth rate limiting.
const (
	// authLimitPerIPCapacity is the maximum number of failures allowed from a
	// single IP before that IP is blocked.
	authLimitPerIPCapacity = 5
	// AuthLimitPerIPWindow is the time over which a full per-IP bucket
	// replenishes. Exported so tests can reference it without duplicating the value.
	AuthLimitPerIPWindow = 30 * time.Second

	// authLimitGlobalCapacity is the maximum total failures across all IPs
	// before the global limit engages.
	authLimitGlobalCapacity = 20
	// authLimitGlobalWindow is the time over which the global bucket refills.
	authLimitGlobalWindow = time.Second
)

// perIPRefillRate is tokens-per-second for per-IP buckets.
var perIPRefillRate = float64(authLimitPerIPCapacity) / AuthLimitPerIPWindow.Seconds()

// globalRefillRate is tokens-per-second for the global bucket.
var globalRefillRate = float64(authLimitGlobalCapacity) / authLimitGlobalWindow.Seconds()

// authBucket is a per-IP token bucket.
type authBucket struct {
	tokens   float64
	lastSeen time.Time
}

// AuthLimiter enforces per-IP and global token-bucket rate limiting on
// failed authentication attempts.
type AuthLimiter struct {
	mu    sync.Mutex
	perIP map[string]*authBucket
	// global is the shared bucket across all non-loopback IPs.
	global authBucket
	now    func() time.Time

	failuresInvalidToken atomic.Uint64
	failuresRateLimited  atomic.Uint64
	failuresCSRFBlocked  atomic.Uint64
}

// NewAuthLimiter constructs an AuthLimiter with the production wall clock.
func NewAuthLimiter() *AuthLimiter {
	return NewAuthLimiterWithClock(time.Now)
}

// NewAuthLimiterWithClock constructs an AuthLimiter that uses the supplied
// clock function. Intended for deterministic tests.
func NewAuthLimiterWithClock(now func() time.Time) *AuthLimiter {
	l := &AuthLimiter{
		perIP: make(map[string]*authBucket),
		now:   now,
	}
	l.global.tokens = authLimitGlobalCapacity
	return l
}

// ClientIP extracts the host portion from a RemoteAddr string (host:port).
// If the address contains no port or cannot be split, the raw value is returned.
func ClientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// refillTokens returns the number of tokens in b after applying elapsed-time
// refill clamped to capacity, without mutating b.
func refillTokens(b *authBucket, capacity, refillPerSec float64, now time.Time) float64 {
	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	t := b.tokens + elapsed*refillPerSec
	if t > capacity {
		t = capacity
	}
	return t
}

// Allowed returns true when the request from ip is permitted to attempt
// authentication. Both the per-IP and global buckets must be non-empty for the
// call to return true. Calling Allowed does not consume any tokens — only
// RegisterFailure does.
func (l *AuthLimiter) Allowed(ip string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Refill global bucket and check.
	gt := refillTokens(&l.global, authLimitGlobalCapacity, globalRefillRate, now)
	if gt < 1 {
		return false
	}

	// Refill per-IP bucket. An unseen IP gets a full bucket and is allowed.
	b, ok := l.perIP[ip]
	if !ok {
		l.perIP[ip] = &authBucket{tokens: authLimitPerIPCapacity, lastSeen: now}
		return true
	}
	bt := refillTokens(b, authLimitPerIPCapacity, perIPRefillRate, now)
	return bt >= 1
}

// RegisterFailure consumes one token from both the per-IP and global buckets.
// Call this after a confirmed auth failure.
func (l *AuthLimiter) RegisterFailure(ip string) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Update and debit global bucket.
	gt := refillTokens(&l.global, authLimitGlobalCapacity, globalRefillRate, now)
	if gt > 0 {
		gt--
	}
	l.global.tokens = gt
	l.global.lastSeen = now

	// Update and debit per-IP bucket.
	b, ok := l.perIP[ip]
	if !ok {
		b = &authBucket{tokens: authLimitPerIPCapacity}
		l.perIP[ip] = b
	}
	bt := refillTokens(b, authLimitPerIPCapacity, perIPRefillRate, now)
	if bt > 0 {
		bt--
	}
	b.tokens = bt
	b.lastSeen = now
}

// RetryAfter returns the minimum duration the caller must wait before at least
// one token is available across both the per-IP and global buckets. The result
// is rounded up to the nearest whole second with a minimum of 1 second.
func (l *AuthLimiter) RetryAfter(ip string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Seconds needed to accumulate one global token.
	var globalWait float64
	gt := refillTokens(&l.global, authLimitGlobalCapacity, globalRefillRate, now)
	if gt < 1 && globalRefillRate > 0 {
		globalWait = (1 - gt) / globalRefillRate
	}

	// Seconds needed to accumulate one per-IP token.
	var perIPWait float64
	if b, ok := l.perIP[ip]; ok {
		bt := refillTokens(b, authLimitPerIPCapacity, perIPRefillRate, now)
		if bt < 1 && perIPRefillRate > 0 {
			perIPWait = (1 - bt) / perIPRefillRate
		}
	}

	waitSecs := max(globalWait, perIPWait)

	d := time.Duration(waitSecs * float64(time.Second))
	d = ((d + time.Second - 1) / time.Second) * time.Second
	return max(d, time.Second)
}

// RecordInvalidToken increments the invalid-token failure counter.
func (l *AuthLimiter) RecordInvalidToken() {
	l.failuresInvalidToken.Add(1)
}

// RecordRateLimited increments the rate-limited failure counter.
func (l *AuthLimiter) RecordRateLimited() {
	l.failuresRateLimited.Add(1)
}

// InvalidTokenTotal returns the cumulative count of invalid-token auth failures.
func (l *AuthLimiter) InvalidTokenTotal() uint64 {
	return l.failuresInvalidToken.Load()
}

// RateLimitedTotal returns the cumulative count of rate-limited auth attempts.
func (l *AuthLimiter) RateLimitedTotal() uint64 {
	return l.failuresRateLimited.Load()
}

// RecordCSRFBlocked increments the CSRF-blocked failure counter.
func (l *AuthLimiter) RecordCSRFBlocked() {
	l.failuresCSRFBlocked.Add(1)
}

// CSRFBlockedTotal returns the cumulative count of cross-site requests blocked by the origin check.
func (l *AuthLimiter) CSRFBlockedTotal() uint64 {
	return l.failuresCSRFBlocked.Load()
}

// StartSweeper launches a background goroutine that periodically evicts per-IP
// buckets idle for more than 10 minutes. It stops when ctx is cancelled.
func (l *AuthLimiter) StartSweeper(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.sweep()
			}
		}
	}()
}

func (l *AuthLimiter) sweep() {
	const idleEvict = 10 * time.Minute
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, b := range l.perIP {
		if now.Sub(b.lastSeen) > idleEvict {
			delete(l.perIP, ip)
		}
	}
}
