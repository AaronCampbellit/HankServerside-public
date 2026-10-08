package cloud

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/storageops"
	"github.com/dropfile/HankServerside/internal/store"
)

const maxHomeNotifications = 12

type homeNotification struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Tone      string     `json:"tone,omitempty"`
	URL       string     `json:"url,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

func (s *Server) handleHomeNotifications(w http.ResponseWriter, r *http.Request, home domain.Home, _ authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 1 || parts[0] != "notifications" {
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}

	items := s.homeNotifications(r, home, membership)
	writeJSON(w, http.StatusOK, map[string]any{"notifications": nonNilSlice(items)})
	return true
}

func (s *Server) homeNotifications(r *http.Request, home domain.Home, membership domain.HomeMembership) []homeNotification {
	items := make([]homeNotification, 0, maxHomeNotifications)
	items = append(items, s.agentNotifications(r, home)...)
	items = append(items, s.quickLinkNotifications(r, home)...)
	if membership.Role == domain.HomeRoleAdmin {
		items = append(items, s.storageNotifications()...)
	}

	sort.SliceStable(items, func(i, j int) bool {
		left := notificationTime(items[i])
		right := notificationTime(items[j])
		return left.After(right)
	})
	if len(items) > maxHomeNotifications {
		items = items[:maxHomeNotifications]
	}
	return items
}

func (s *Server) agentNotifications(r *http.Request, home domain.Home) []homeNotification {
	agent, err := s.store.GetAgentByHomeID(r.Context(), home.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return nil
	}

	status := strings.ToLower(strings.TrimSpace(agent.Status))
	if online, ok := s.router.GetAgent(home.ID); ok {
		status = strings.ToLower(strings.TrimSpace(online.agent.Status))
	}
	if status == domain.AgentStatusOnline {
		return nil
	}

	body := fmt.Sprintf("%s is not connected.", firstNonEmpty(agent.Name, agent.ID, "Primary Hank Agent"))
	if agent.LastSeenAt != nil {
		body = fmt.Sprintf("%s has not checked in since %s.", firstNonEmpty(agent.Name, agent.ID, "Primary Hank Agent"), agent.LastSeenAt.Format(time.RFC822))
	}
	createdAt := agent.LastSeenAt
	if createdAt == nil {
		createdAt = &agent.UpdatedAt
	}
	return []homeNotification{{
		ID:        "agent:" + agent.ID + ":offline",
		Title:     "Agent offline",
		Body:      body,
		Tone:      "warning",
		URL:       "/dashboard/settings/home",
		CreatedAt: createdAt,
	}}
}

func (s *Server) quickLinkNotifications(r *http.Request, home domain.Home) []homeNotification {
	links, err := s.store.ListHomeQuickLinks(r.Context(), home.ID)
	if err != nil {
		return nil
	}
	items := make([]homeNotification, 0)
	for _, link := range links {
		if !link.HealthCheckEnabled || link.Status != domain.QuickLinkStatusDown {
			continue
		}
		body := fmt.Sprintf("%s is failing health checks.", link.Title)
		if link.StatusCode > 0 {
			body = fmt.Sprintf("%s returned HTTP %d.", link.Title, link.StatusCode)
		}
		if strings.TrimSpace(link.LastError) != "" {
			body = body + " " + strings.TrimSpace(link.LastError)
		}
		createdAt := link.LastCheckedAt
		if createdAt == nil {
			createdAt = &link.UpdatedAt
		}
		items = append(items, homeNotification{
			ID:        "quick_link:" + link.ID + ":down",
			Title:     "Quick link is down",
			Body:      body,
			Tone:      "danger",
			URL:       "/dashboard/settings/quick-links",
			CreatedAt: createdAt,
		})
	}
	return items
}

func (s *Server) storageNotifications() []homeNotification {
	if s.storage == nil {
		return nil
	}
	events, err := s.storage.Events(storageops.EventFilter{Limit: 8, FailuresOnly: true})
	if err != nil {
		return nil
	}
	items := make([]homeNotification, 0, len(events))
	for _, event := range events {
		tone := "warning"
		if event.Severity == storageops.EventSeverityError || event.Severity == storageops.EventSeverityCritical {
			tone = "danger"
		}
		when := event.Time
		items = append(items, homeNotification{
			ID:        "storage:" + event.ID,
			Title:     "Storage needs attention",
			Body:      firstNonEmpty(event.Message, event.Operation),
			Tone:      tone,
			URL:       "/dashboard/settings/backups",
			CreatedAt: &when,
		})
	}
	return items
}

func notificationTime(item homeNotification) time.Time {
	if item.CreatedAt == nil {
		return time.Time{}
	}
	return *item.CreatedAt
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
