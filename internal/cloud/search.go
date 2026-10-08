package cloud

// Global search endpoint backing the dashboard top-bar search box.
//
// Route: GET /v1/home/search?q=<query>[&limit=N]
// Dispatched from handleHomeSubroutes so it inherits the same auth,
// singleton-home, and membership resolution as other /v1/home/* handlers.
//
// Searches content the cloud already holds locally (no agent round-trip):
//   - navigation pages (static catalog)
//   - profile notes (current user)
//   - quick links
//   - installed apps      (admin-gated)
//   - home members        (admin-gated)
// File metadata is indexed in PostgreSQL by the background file crawler.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func dashboardSearchURL(path string, key string, value string) string {
	params := url.Values{}
	params.Set(key, value)
	return path + "?" + params.Encode()
}

const (
	maxSearchResults = 24
	maxSearchQuery   = 120
)

type searchResult struct {
	Type     string `json:"type"` // page | note | quick_link | app | member
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	URL      string `json:"url"`                // navigation target (or external href)
	External bool   `json:"external,omitempty"` // open in a new tab instead of in-app nav
	score    int
}

type searchResponse struct {
	Query           string         `json:"query"`
	Results         []searchResult `json:"results"`
	FileIndexStatus string         `json:"file_index_status,omitempty"`
}

// Static catalog of navigable pages. Mirrors routes in web/dashboard/src/App.tsx.
var searchPageCatalog = []searchResult{
	{Type: "page", Title: "Home", Subtitle: "Dashboard", URL: "/dashboard"},
	{Type: "page", Title: "Hank", Subtitle: "Assistant chat", URL: "/dashboard/hank"},
	{Type: "page", Title: "Home Assistant", Subtitle: "Devices & entities", URL: "/dashboard/home-assistant"},
	{Type: "page", Title: "Notes", Subtitle: "Profile notes", URL: "/dashboard/profile-notes"},
	{Type: "page", Title: "File Server", Subtitle: "Browse shares", URL: "/dashboard/file-server"},
	{Type: "page", Title: "Settings", URL: "/dashboard/settings"},
	{Type: "page", Title: "Home & Agent Setup", Subtitle: "Settings", URL: "/dashboard/settings/home"},
	{Type: "page", Title: "Quick Links", Subtitle: "Settings", URL: "/dashboard/settings/quick-links"},
	{Type: "page", Title: "Notifications", Subtitle: "Settings", URL: "/dashboard/settings/notifications"},
	{Type: "page", Title: "People", Subtitle: "Settings", URL: "/dashboard/settings/people"},
	{Type: "page", Title: "Connections", Subtitle: "Settings", URL: "/dashboard/settings/connections"},
	{Type: "page", Title: "AI & MCP", Subtitle: "Settings", URL: "/dashboard/settings/ai"},
	{Type: "page", Title: "Apps", Subtitle: "Settings", URL: "/dashboard/settings/apps"},
	{Type: "page", Title: "Backups & Storage", Subtitle: "Settings", URL: "/dashboard/settings/backups"},
	{Type: "page", Title: "Recovery", Subtitle: "Settings", URL: "/dashboard/settings/recovery"},
	{Type: "page", Title: "Logs", Subtitle: "Settings", URL: "/dashboard/settings/logs"},
	{Type: "page", Title: "Setup Guide", Subtitle: "Docs", URL: "/docs/deployment"},
}

func (s *Server) handleHomeSearch(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 1 || parts[0] != "search" {
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > maxSearchQuery {
		query = query[:maxSearchQuery]
	}
	results := []searchResult{}
	if query == "" {
		writeJSON(w, http.StatusOK, searchResponse{Query: query, Results: results})
		return true
	}
	needle := strings.ToLower(query)
	isAdmin := canEditHomeQuickLinks(home, membership) // admin or home owner

	// 1) pages
	for _, page := range searchPageCatalog {
		if score := matchScore(needle, page.Title, page.Subtitle); score > 0 {
			page.score = score + 30 // pages rank slightly above content
			results = append(results, page)
		}
	}

	// 2) notes (current user)
	if notes, err := s.store.ListProfileNotes(r.Context(), auth.User.ID, false); err == nil {
		for _, note := range notes {
			title := note.Title
			if strings.TrimSpace(title) == "" {
				title = "Untitled note"
			}
			if score := matchScore(needle, title, note.Content); score > 0 {
				results = append(results, searchResult{
					Type:     "note",
					Title:    title,
					Subtitle: noteSnippet(note),
					URL:      dashboardSearchURL("/dashboard/profile-notes", "note", note.NoteID),
					score:    score + 10,
				})
			}
			if normalizePageType(note.PageType) == protocol.NotePageTypeKanban && note.BoardJSON != "" {
				var board protocol.KanbanBoard
				if json.Unmarshal([]byte(note.BoardJSON), &board) == nil {
					for _, column := range board.Columns {
						for _, card := range column.Cards {
							if score := matchScore(needle, card.Text, column.Title+" "+note.Title); score > 0 {
								results = append(results, searchResult{Type: "kanban_card", Title: card.Text,
									Subtitle: note.Title + " · " + column.Title,
									URL:      dashboardSearchURL("/dashboard/profile-notes", "note", note.NoteID), score: score})
							}
						}
					}
				}
			}
		}
	}

	if agents, err := s.store.ListAgentsByHome(r.Context(), home.ID); err == nil {
		for _, agent := range agents {
			if score := matchScore(needle, agent.Name, agent.Platform+" "+agent.Status); score > 0 {
				results = append(results, searchResult{Type: "machine", Title: agent.Name,
					Subtitle: "Machine · " + agent.Status,
					URL:      dashboardSearchURL("/dashboard/settings/home", "agent", agent.ID), score: score})
			}
		}
	}

	// 3) quick links
	if links, err := s.store.ListHomeQuickLinks(r.Context(), home.ID); err == nil {
		for _, link := range links {
			if score := matchScore(needle, link.Title, link.Description+" "+link.URL); score > 0 {
				results = append(results, searchResult{
					Type:     "quick_link",
					Title:    link.Title,
					Subtitle: link.URL,
					URL:      link.URL,
					External: true,
					score:    score,
				})
			}
		}
	}

	// 4) apps + 5) members - admin-gated (these tabs are admin-only)
	if isAdmin {
		if apps, err := s.store.ListHomeApps(r.Context(), home.ID); err == nil {
			for _, app := range apps {
				if score := matchScore(needle, app.Name, app.AppID); score > 0 {
					state := "disabled"
					if app.Enabled {
						state = "enabled"
					}
					results = append(results, searchResult{
						Type:     "app",
						Title:    app.Name,
						Subtitle: "App - " + state,
						URL:      dashboardSearchURL("/dashboard/settings/apps", "app", app.AppID),
						score:    score,
					})
				}
			}
		}
		if members, err := s.store.ListHomeMembers(r.Context(), home.ID); err == nil {
			for _, member := range members {
				if score := matchScore(needle, member.Email, member.Role); score > 0 {
					results = append(results, searchResult{
						Type:     "member",
						Title:    member.Email,
						Subtitle: "Member - " + member.Role,
						URL:      dashboardSearchURL("/dashboard/settings/people", "member", member.Email),
						score:    score,
					})
				}
			}
		}
	}

	// File results retain the exact agent and source so similarly named shares
	// cannot navigate to one another. Authorization is checked for every query.
	fileIndexStatus := ""
	if len([]rune(query)) >= 2 {
		files, err := s.catalogFileMatches(r.Context(), home, membership, auth, "", "", query, 12, true)
		if err == nil {
			fileIndexStatus = files.Status
			for _, item := range files.Items {
				results = append(results, searchResult{
					Type: "file", Title: item.Name,
					Subtitle: item.SourceName + " · " + item.Path,
					URL:      fileSearchURL(item),
					score:    matchScore(needle, item.Name, item.Path),
				})
			}
		}
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].score > results[j].score })
	if len(results) > maxSearchResults {
		results = results[:maxSearchResults]
	}
	writeJSON(w, http.StatusOK, searchResponse{Query: query, Results: results, FileIndexStatus: fileIndexStatus})
	return true
}

// matchScore returns 0 for no match; higher = better. Title prefix beats title
// substring beats body substring.
func matchScore(needle, title, body string) int {
	t := strings.ToLower(title)
	switch {
	case strings.HasPrefix(t, needle):
		return 100
	case strings.Contains(t, needle):
		return 60
	case strings.Contains(strings.ToLower(body), needle):
		return 25
	default:
		return 0
	}
}

func noteSnippet(note domain.UserNote) string {
	body := strings.TrimSpace(note.Content)
	body = strings.ReplaceAll(body, "\n", " ")
	if len(body) > 90 {
		body = strings.TrimSpace(body[:90]) + "..."
	}
	kind := note.PageType
	if kind == "" {
		kind = "text"
	}
	if body == "" {
		return "Note - " + kind
	}
	return body
}
