// Package ratelimit is a small in-memory token-bucket limiter keyed by string.
// It is per process: with several instances each enforces its own budget, which
// is still enough to blunt password guessing and argon2 CPU abuse.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   float64
	perSec  float64
	now     func() time.Time
	sweepAt time.Time
}

// New allows bursts of up to burst events and refills one every `every`.
func New(burst int, every time.Duration) *Limiter {
	return &Limiter{
		buckets: map[string]*bucket{}, burst: float64(burst), perSec: 1 / every.Seconds(), now: time.Now,
	}
}

// Allow consumes one token for key. When it refuses, retryAfter says how long until one is available.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSec)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.perSec * float64(time.Second))
}

// sweep drops buckets that have fully refilled (they carry no information),
// at most once a minute, so memory stays bounded by recently active keys.
func (l *Limiter) sweep(now time.Time) {
	if now.Before(l.sweepAt) {
		return
	}
	l.sweepAt = now.Add(time.Minute)
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.perSec >= l.burst {
			delete(l.buckets, k)
		}
	}
}
