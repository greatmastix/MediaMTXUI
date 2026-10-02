package auth

import (
	"math"
	"strings"
	"sync"
	"time"
)

// maxKeys bounds the memory of the limiters below. Beyond it, stale entries are swept; if that is not enough, new
// keys are refused, which errs on the side of rejecting requests during a flood from very many addresses.
const maxKeys = 100_000

// RateLimiter is a token bucket per key (e.g. per client IP).
type RateLimiter struct {
	mu      sync.Mutex
	perSec  float64
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewRateLimiter allows perMinute requests per key, with bursts of up to burst.
func NewRateLimiter(perMinute, burst int) *RateLimiter {
	return &RateLimiter{perSec: float64(perMinute) / 60, burst: float64(burst), buckets: map[string]*bucket{}, now: time.Now}
}

// Allow takes a token for key. When none is left it returns false and how long until one is.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxKeys && !l.sweep(now) {
			return false, time.Minute
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.perSec)
	b.at = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / l.perSec * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// sweep drops buckets that have refilled completely; they carry no information. It reports whether room was made.
func (l *RateLimiter) sweep(now time.Time) bool {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*l.perSec >= l.burst {
			delete(l.buckets, k)
		}
	}
	return len(l.buckets) < maxKeys
}

// Lockout locks a key (a normalized username) after threshold failures within the lock duration. Keys are counted
// whether or not such a user exists, so a lock reveals nothing about which usernames are real.
type Lockout struct {
	mu        sync.Mutex
	threshold int
	duration  time.Duration
	entries   map[string]*lockEntry
	now       func() time.Time
}

type lockEntry struct {
	failures    int
	first       time.Time
	lockedUntil time.Time
}

// NewLockout locks a key for duration after threshold failures within duration.
func NewLockout(threshold int, duration time.Duration) *Lockout {
	return &Lockout{threshold: threshold, duration: duration, entries: map[string]*lockEntry{}, now: time.Now}
}

// LockKey normalizes a username into a lockout key.
func LockKey(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

// Locked reports whether key is locked and for how much longer.
func (l *Lockout) Locked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return false, 0
	}
	if left := e.lockedUntil.Sub(l.now()); left > 0 {
		return true, left
	}
	return false, 0
}

// Fail records a failure and reports whether key is now locked.
func (l *Lockout) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= maxKeys {
			l.sweep(now)
		}
		e = &lockEntry{}
		l.entries[key] = e
	}
	if e.failures == 0 || now.Sub(e.first) > l.duration {
		e.failures, e.first = 0, now
	}
	e.failures++
	if e.failures >= l.threshold {
		e.lockedUntil = now.Add(l.duration)
		e.failures = 0
		return true
	}
	return false
}

// Succeed clears key's failures.
func (l *Lockout) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *Lockout) sweep(now time.Time) {
	for k, e := range l.entries {
		if now.After(e.lockedUntil) && now.Sub(e.first) > l.duration {
			delete(l.entries, k)
		}
	}
}
