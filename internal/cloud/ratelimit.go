package cloud

import (
	"sync"
	"time"
)

type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		hits: make(map[string][]time.Time),
	}
}

func (l *rateLimiter) Allow(key string, limit int, window time.Duration) bool {
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()

	existing := l.hits[key][:0]
	for _, hit := range l.hits[key] {
		if now.Sub(hit) < window {
			existing = append(existing, hit)
		}
	}

	if len(existing) >= limit {
		l.hits[key] = existing
		return false
	}

	l.hits[key] = append(existing, now)
	return true
}
