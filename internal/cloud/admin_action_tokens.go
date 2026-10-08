package cloud

import (
	"errors"
	"sync"
	"time"
)

type adminActionToken struct {
	UserID    string
	Action    string
	ExpiresAt time.Time
}

type adminActionTokenRegistry struct {
	mu     sync.Mutex
	tokens map[string]adminActionToken
}

func newAdminActionTokenRegistry() *adminActionTokenRegistry {
	return &adminActionTokenRegistry{tokens: make(map[string]adminActionToken)}
}

func (r *adminActionTokenRegistry) Issue(userID string, action string, ttl time.Duration) (string, time.Time) {
	raw := newToken()
	expiresAt := time.Now().UTC().Add(ttl)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[hashToken(raw)] = adminActionToken{UserID: userID, Action: action, ExpiresAt: expiresAt}
	return raw, expiresAt
}

func (r *adminActionTokenRegistry) Consume(raw string, userID string, action string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := hashToken(raw)
	token, ok := r.tokens[key]
	if !ok {
		return errors.New("admin action token not found")
	}
	delete(r.tokens, key)
	if token.UserID != userID || token.Action != action || time.Now().UTC().After(token.ExpiresAt) {
		return errors.New("admin action token invalid")
	}
	return nil
}
