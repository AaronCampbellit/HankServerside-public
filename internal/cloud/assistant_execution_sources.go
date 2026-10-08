package cloud

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
)

type assistantExecutionSource struct {
	Title string `json:"title"`
	URI   string `json:"uri"`
	Href  string `json:"href"`
}

func executionCitations(task domain.AssistantTask, messages []assistant.Message, text string) []assistantExecutionSource {
	sources := []assistantExecutionSource{}
	seen := map[string]bool{}
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		message := messages[messageIndex]
		if message.Result == nil || message.Result.Outcome != "confirmed" {
			continue
		}
		var data executionData
		if json.Unmarshal(message.Result.Data, &data) != nil {
			continue
		}
		for index, item := range data.Items {
			if item.URI == "" || seen[item.URI] || len(sources) >= 20 {
				continue
			}
			// Include observed source links even if the provider omitted inline links.
			originTask := task.ID
			if message.OriginTaskID != "" {
				originTask = message.OriginTaskID
			}
			href := "/v1/home/assistant/tasks/" + url.PathEscape(originTask) + "/source?" + url.Values{"call_id": {message.Result.CallID}, "item": {strconv.Itoa(index)}}.Encode()
			if strings.HasPrefix(item.URI, "hank://notes/") {
				if noteID, err := url.PathUnescape(strings.TrimPrefix(item.URI, "hank://notes/")); err == nil {
					href = "/dashboard/profile-notes?" + url.Values{"note": {noteID}}.Encode()
				}
			}
			if strings.HasPrefix(item.URI, "/dashboard/") {
				href = item.URI
			}
			if strings.HasPrefix(item.URI, "hank://assistant/sessions/") {
				href = "/dashboard/hank?" + url.Values{"session": {item.ID}}.Encode()
			}
			sources = append(sources, assistantExecutionSource{Title: item.Title, URI: item.URI, Href: href})
			seen[item.URI] = true
		}
	}
	return sources
}
func (s *Server) handleAssistantExecutionSource(w http.ResponseWriter, r *http.Request, task domain.AssistantTask) {
	index, err := strconv.Atoi(r.URL.Query().Get("item"))
	if err != nil || index < 0 || index >= 50 {
		http.Error(w, "invalid source", 400)
		return
	}
	step, err := s.store.GetAssistantTaskStep(r.Context(), task.HomeID, task.UserID, task.ID, r.URL.Query().Get("call_id"))
	if err != nil {
		assistantTaskHTTPError(w, r, err)
		return
	}
	var result assistant.Result
	var data executionData
	if json.Unmarshal(step.Result, &result) != nil || json.Unmarshal(result.Data, &data) != nil || index >= len(data.Items) {
		http.NotFound(w, r)
		return
	}
	if !s.authorizeExecutionContext(r.Context(), task, assistantExecutionCheckpoint{Messages: []assistant.Message{{Role: "tool", Result: &result}}}) {
		http.NotFound(w, r)
		return
	}
	item := data.Items[index]
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(item.Title + "\nID: " + item.ID + "\nDevice: " + item.DeviceID + "\nCalendar: " + item.CalendarID + "\nAgent: " + item.AgentID + "\nSource ID: " + item.SourceID + "\n\n" + item.Text + "\n" + item.Path + "\n" + item.State + "\n" + item.StartsAt + "\n" + item.EndsAt + "\n\nSource: " + item.URI))
}
