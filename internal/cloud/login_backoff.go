package cloud

import (
	"strings"
	"sync"
	"time"
)

type loginBackoffEntry struct {
	failures int
	blocked  time.Time
}

type loginBackoffRegistry struct {
	mu      sync.Mutex
	entries map[string]loginBackoffEntry
}

func newLoginBackoffRegistry() *loginBackoffRegistry {
	return &loginBackoffRegistry{entries: make(map[string]loginBackoffEntry)}
}

func (r *loginBackoffRegistry) Blocked(email string) (time.Duration, bool) {
	key := loginBackoffKey(email)
	if key == "" {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[key]
	if entry.blocked.IsZero() {
		return 0, false
	}
	now := time.Now().UTC()
	if !now.Before(entry.blocked) {
		delete(r.entries, key)
		return 0, false
	}
	return time.Until(entry.blocked), true
}

func (r *loginBackoffRegistry) RecordFailure(email string) {
	key := loginBackoffKey(email)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[key]
	entry.failures++
	if entry.failures >= 5 {
		delay := time.Duration(1<<(min(entry.failures, 10)-5)) * time.Minute
		entry.blocked = time.Now().UTC().Add(delay)
	}
	r.entries[key] = entry
}

func (r *loginBackoffRegistry) RecordSuccess(email string) {
	key := loginBackoffKey(email)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, key)
}

func loginBackoffKey(email string) string {
	return strings.TrimSpace(strings.ToLower(email))
}
