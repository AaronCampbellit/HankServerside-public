package cloud

import (
	"errors"
	"sync"
	"time"
)

var errAppTicketNotFound = errors.New("app websocket ticket not found")

type appWebSocketTicket struct {
	SessionID string
	UserID    string
	ExpiresAt time.Time
}

type appWebSocketTicketRegistry struct {
	mu      sync.Mutex
	tickets map[string]appWebSocketTicket
}

func newAppWebSocketTicketRegistry() *appWebSocketTicketRegistry {
	return &appWebSocketTicketRegistry{
		tickets: make(map[string]appWebSocketTicket),
	}
}

func (r *appWebSocketTicketRegistry) Issue(sessionID string, userID string, ttl time.Duration) (string, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	r.pruneExpiredLocked(now)

	rawToken := newToken()
	expiresAt := now.Add(ttl)
	r.tickets[hashToken(rawToken)] = appWebSocketTicket{
		SessionID: sessionID,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}
	return rawToken, expiresAt
}

func (r *appWebSocketTicketRegistry) Consume(rawToken string) (appWebSocketTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	r.pruneExpiredLocked(now)

	key := hashToken(rawToken)
	ticket, ok := r.tickets[key]
	if !ok {
		return appWebSocketTicket{}, errAppTicketNotFound
	}
	delete(r.tickets, key)
	return ticket, nil
}

func (r *appWebSocketTicketRegistry) pruneExpiredLocked(now time.Time) {
	for key, ticket := range r.tickets {
		if !ticket.ExpiresAt.After(now) {
			delete(r.tickets, key)
		}
	}
}
