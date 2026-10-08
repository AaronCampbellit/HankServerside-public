package cloud

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestAssistantUnsupportedExecutionVersionDoesNotCreateMessages(t *testing.T) {
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_version", Email: "version@example.invalid", PasswordHash: "disabled", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_version", UserID: user.ID, Name: "Version test", CreatedAt: now, UpdatedAt: now}
	session := domain.AssistantSession{ID: "asess_version", HomeID: home.ID, UserID: user.ID, Title: "Version test", CreatedAt: now, UpdatedAt: now, LastMessageAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateAssistantSession(ctx, session))
	s := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer s.Shutdown(ctx)
	for _, version := range []string{"2", "unknown", "1, 2", ""} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"content":"create a note called should not exist"}`))
		r.Header.Set("X-Hank-Assistant-Execution", version)
		w := httptest.NewRecorder()
		s.handleAssistantSessionMessages(w, r, home, domain.HomeMembership{Role: domain.HomeRoleAdmin}, authContext{User: user}, session.ID)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "execution_version_unavailable") {
			t.Fatalf("version %q returned %d", version, w.Code)
		}
	}
	rMultiple := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"content":"must not execute"}`))
	rMultiple.Header.Add("X-Hank-Assistant-Execution", "1")
	rMultiple.Header.Add("X-Hank-Assistant-Execution", "2")
	wMultiple := httptest.NewRecorder()
	s.handleAssistantSessionMessages(wMultiple, rMultiple, home, domain.HomeMembership{Role: domain.HomeRoleAdmin}, authContext{User: user}, session.ID)
	if wMultiple.Code != http.StatusConflict {
		t.Fatal("ambiguous execution versions were accepted")
	}
	messages, err := db.ListAssistantMessages(ctx, session.ID)
	must(t, err)
	if len(messages) != 0 {
		t.Fatal("unsupported execution stored a message")
	}
	// The version check must not reveal another user's session.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("X-Hank-Assistant-Execution", "2")
	s.handleAssistantSessionMessages(w, r, home, domain.HomeMembership{}, authContext{User: domain.User{ID: "other"}}, session.ID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign session returned %d", w.Code)
	}
}
