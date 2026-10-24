// Package ratelimit keeps token buckets per station key and per client address in memory.
//
// In memory is enough for one server process: limits only need to hold back a misbehaving client, not survive a
// restart. Several processes behind a load balancer would each count on their own.
package ratelimit

import (
	"sync"
	"time"
)

// maxKeys bounds memory: beyond it, buckets that are full again are dropped, so random keys cannot fill it.
const maxKeys = 50_000

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter allows capacity requests per period and key, refilled continuously.
type Limiter struct {
	capacity float64
	rate     float64 // tokens per second
	mu       sync.Mutex
	buckets  map[string]bucket
}

// New returns a limiter for capacity requests per period.
func New(capacity int, period time.Duration) *Limiter {
	return &Limiter{capacity: float64(capacity), rate: float64(capacity) / period.Seconds(),
		buckets: map[string]bucket{}}
}

// Allow takes one token for key if there is one.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = bucket{tokens: l.capacity, last: now}
	}
	b.tokens = min(l.capacity, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.buckets[key] = b
	if len(l.buckets) > maxKeys {
		for k, x := range l.buckets {
			if x.tokens+now.Sub(x.last).Seconds()*l.rate >= l.capacity {
				delete(l.buckets, k)
			}
		}
	}
	return allowed
}
