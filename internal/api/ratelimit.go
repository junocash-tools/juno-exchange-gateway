package api

import (
	"sync"
	"time"
)

type bucket struct {
	tokens  float64
	updated time.Time
	seen    time.Time
}
type limiter struct {
	mu          sync.Mutex
	rps         float64
	burst       float64
	buckets     map[string]bucket
	lastCleanup time.Time
}

func newLimiter(rps float64, burst int) *limiter {
	return &limiter{rps: rps, burst: float64(burst), buckets: map[string]bucket{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = bucket{tokens: l.burst, updated: now}
	}
	b.tokens += now.Sub(b.updated).Seconds() * l.rps
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.updated, b.seen = now, now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.buckets[key] = b
	if now.Sub(l.lastCleanup) > 5*time.Minute {
		for k, v := range l.buckets {
			if now.Sub(v.seen) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastCleanup = now
	}
	return allowed
}
