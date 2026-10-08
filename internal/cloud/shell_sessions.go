package cloud

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/protocol"
)

const shellSessionTopicPrefix = "shell.session:"

type cloudShellSession struct {
	homeID, userID, agentID string
	touchedAt               time.Time
}

func (s *Server) authorizeShellSessionCommand(command protocol.RoutedCommand, homeID, userID, agentID string) (string, error) {
	decode := func(target interface{ Validate() error }) error {
		if err := json.Unmarshal(command.Body, target); err != nil {
			return err
		}
		return target.Validate()
	}
	switch command.Command {
	case protocol.CommandShellSessionOpen:
		request := &protocol.ShellSessionOpenRequest{}
		if err := decode(request); err != nil {
			return "", err
		}
		return request.SessionID, s.shellSessions.open(request.SessionID, homeID, userID, agentID)
	case protocol.CommandShellSessionInput:
		request := &protocol.ShellSessionInputRequest{}
		if err := decode(request); err != nil {
			return "", err
		}
		return request.SessionID, s.shellSessions.authorize(request.SessionID, homeID, userID, agentID)
	case protocol.CommandShellSessionResize:
		request := &protocol.ShellSessionResizeRequest{}
		if err := decode(request); err != nil {
			return "", err
		}
		return request.SessionID, s.shellSessions.authorize(request.SessionID, homeID, userID, agentID)
	case protocol.CommandShellSessionAttach:
		request := &protocol.ShellSessionAttachRequest{}
		if err := decode(request); err != nil {
			return "", err
		}
		return request.SessionID, s.shellSessions.authorize(request.SessionID, homeID, userID, agentID)
	case protocol.CommandShellSessionClose:
		request := &protocol.ShellSessionCloseRequest{}
		if err := decode(request); err != nil {
			return "", err
		}
		return request.SessionID, s.shellSessions.authorize(request.SessionID, homeID, userID, agentID)
	default:
		return "", errors.New("unsupported shell session operation")
	}
}

type shellSessionRegistry struct {
	mu                      sync.Mutex
	sessions                map[string]cloudShellSession
	maxPerUser, maxPerAgent int
	grace                   time.Duration
	now                     func() time.Time
}

func newShellSessionRegistry(maxPerUser, maxPerAgent int, grace time.Duration) *shellSessionRegistry {
	return &shellSessionRegistry{
		sessions: make(map[string]cloudShellSession), maxPerUser: maxPerUser,
		maxPerAgent: maxPerAgent, grace: grace, now: time.Now,
	}
}

func (r *shellSessionRegistry) open(id, homeID, userID, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	if _, exists := r.sessions[id]; exists {
		return errors.New("shell session already exists")
	}
	userCount, agentCount := 0, 0
	for _, session := range r.sessions {
		if session.homeID == homeID && session.userID == userID {
			userCount++
		}
		if session.homeID == homeID && session.agentID == agentID {
			agentCount++
		}
	}
	if userCount >= r.maxPerUser {
		return errors.New("shell session user limit reached")
	}
	if agentCount >= r.maxPerAgent {
		return errors.New("shell session agent limit reached")
	}
	r.sessions[id] = cloudShellSession{homeID: homeID, userID: userID, agentID: agentID, touchedAt: r.now()}
	return nil
}

func (r *shellSessionRegistry) authorize(id, homeID, userID, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	session, ok := r.sessions[id]
	if !ok || session.homeID != homeID || session.userID != userID || session.agentID != agentID {
		return errors.New("shell session not found")
	}
	session.touchedAt = r.now()
	r.sessions[id] = session
	return nil
}

func (r *shellSessionRegistry) close(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

func (r *shellSessionRegistry) complete(command, id string, success bool) {
	if id == "" {
		return
	}
	if command == protocol.CommandShellSessionOpen && !success ||
		command == protocol.CommandShellSessionClose && success {
		r.close(id)
	}
}

func (r *shellSessionRegistry) ownsTopic(topic, homeID, userID string) bool {
	id := strings.TrimPrefix(topic, shellSessionTopicPrefix)
	if id == topic {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	session, ok := r.sessions[id]
	return ok && session.homeID == homeID && session.userID == userID
}

func (r *shellSessionRegistry) allowsAgentEvent(topic, homeID, agentID string) bool {
	id := strings.TrimPrefix(topic, shellSessionTopicPrefix)
	if id == topic {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	session, ok := r.sessions[id]
	return ok && session.homeID == homeID && session.agentID == agentID
}

func (r *shellSessionRegistry) prune() { r.mu.Lock(); defer r.mu.Unlock(); r.pruneLocked() }
func (r *shellSessionRegistry) pruneLocked() {
	cutoff := r.now().Add(-r.grace)
	for id, session := range r.sessions {
		if session.touchedAt.Before(cutoff) {
			delete(r.sessions, id)
		}
	}
}
