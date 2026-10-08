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

func TestNormalizeUserDisplayName(t *testing.T) {
	t.Parallel()

	got, err := normalizeUserDisplayName("  Aaron Campbell  ")
	if err != nil || got != "Aaron Campbell" {
		t.Fatalf("normalize trimmed name = %q, %v", got, err)
	}

	eighty := strings.Repeat("界", 80)
	got, err = normalizeUserDisplayName(eighty)
	if err != nil || got != eighty {
		t.Fatalf("normalize 80-code-point name = %q, %v", got, err)
	}

	if _, err := normalizeUserDisplayName(strings.Repeat("界", 81)); err == nil {
		t.Fatal("normalize 81-code-point name succeeded, want error")
	}
}

func TestHomeMemberDisplayNameAuthorization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	owner := domain.User{ID: "usr_display_owner", Email: "display-owner@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_display_member", Email: "display-member@example.com", PasswordHash: string(mustPasswordHash(t, "member-password")), CreatedAt: now, UpdatedAt: now}
	other := domain.User{ID: "usr_display_other", Email: "display-other@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	outsider := domain.User{ID: "usr_display_outsider", Email: "display-outsider@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_display_names", UserID: owner.ID, Name: "Display Names Home", CreatedAt: now, UpdatedAt: now}

	for _, user := range []domain.User{owner, member, other, outsider} {
		must(t, db.CreateUser(ctx, user))
	}
	must(t, db.CreateHome(ctx, home))
	for _, user := range []domain.User{member, other} {
		must(t, db.AddHomeMembership(ctx, domain.HomeMembership{
			HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now,
		}))
	}
	for _, session := range []struct {
		id, token, userID string
	}{
		{id: "sess_display_owner", token: "display-owner-token", userID: owner.ID},
		{id: "sess_display_member", token: "display-member-token", userID: member.ID},
	} {
		must(t, db.CreateSession(ctx, domain.AppSession{
			ID: session.id, UserID: session.userID, TokenHash: hashToken(session.token), ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		}))
	}

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var updated domain.HomeMember
	requestJSON(t, testServer, "display-member-token", http.MethodPut, "/v1/home/members/"+member.ID+"/display-name", map[string]any{
		"display_name": "  Member Name  ",
	}, &updated)
	if updated.UserID != member.ID || updated.DisplayName != "Member Name" || updated.Email != member.Email {
		t.Fatalf("self-updated member = %#v", updated)
	}

	requestJSON(t, testServer, "display-owner-token", http.MethodPut, "/v1/home/members/"+other.ID+"/display-name", map[string]any{
		"display_name": "Other Name",
	}, &updated)
	if updated.UserID != other.ID || updated.DisplayName != "Other Name" || updated.Email != other.Email {
		t.Fatalf("admin-updated member = %#v", updated)
	}

	loginResponse := requestJSONStatus(t, testServer, "", http.MethodPost, "/v1/auth/login", map[string]any{
		"email": member.Email, "password": "member-password",
	}, http.StatusOK)
	loginResponse.Body.Close()
	loginResponse = requestJSONStatus(t, testServer, "", http.MethodPost, "/v1/auth/login", map[string]any{
		"email": "Member Name", "password": "member-password",
	}, http.StatusUnauthorized)
	loginResponse.Body.Close()

	response := requestJSONStatus(t, testServer, "display-member-token", http.MethodPut, "/v1/home/members/"+other.ID+"/display-name", map[string]any{
		"display_name": "Forbidden",
	}, http.StatusForbidden)
	response.Body.Close()

	response = requestJSONStatus(t, testServer, "display-owner-token", http.MethodPut, "/v1/home/members/"+outsider.ID+"/display-name", map[string]any{
		"display_name": "Missing",
	}, http.StatusNotFound)
	response.Body.Close()

	response = requestJSONStatus(t, testServer, "display-member-token", http.MethodPut, "/v1/home/members/"+member.ID+"/display-name", map[string]any{
		"display_name": strings.Repeat("界", 81),
	}, http.StatusBadRequest)
	response.Body.Close()

	requestJSON(t, testServer, "display-member-token", http.MethodPut, "/v1/home/members/"+member.ID+"/display-name", map[string]any{
		"display_name": "",
	}, &updated)
	if updated.DisplayName != "" || updated.Email != member.Email {
		t.Fatalf("cleared member = %#v", updated)
	}

	request, err := http.NewRequest(http.MethodPut, testServer.URL+"/v1/home/members/"+member.ID+"/display-name", strings.NewReader(`{"display_name":`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer display-member-token")
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("malformed display-name status = %d, want 400 body=%s", response.StatusCode, string(data))
	}

	var auditPayload struct {
		Events []struct {
			EventType string `json:"event_type"`
			TargetID  string `json:"target_id"`
			Metadata  any    `json:"metadata"`
		} `json:"events"`
	}
	requestJSON(t, testServer, "display-owner-token", http.MethodGet, "/v1/home/audit-events?event_type=user.display_name_updated", nil, &auditPayload)
	if len(auditPayload.Events) < 3 {
		t.Fatalf("display-name audit events = %#v, want self, admin, and clear events", auditPayload.Events)
	}
	for _, event := range auditPayload.Events {
		if event.EventType != "user.display_name_updated" || event.TargetID == "" {
			t.Fatalf("display-name audit event = %#v", event)
		}
	}
}
