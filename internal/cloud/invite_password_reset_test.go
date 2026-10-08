package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestInviteSignupCreatesUserMembershipAndSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_invite_admin", Email: "admin-invite@example.com", PasswordHash: string(mustPasswordHash(t, "admin-password")), CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_invite_signup", UserID: admin.ID, Name: "Invite Home", CreatedAt: now, UpdatedAt: now}
	adminToken := "admin-invite-token"
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_invite_admin", UserID: admin.ID, TokenHash: hashToken(adminToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var invite struct {
		Email   string `json:"email"`
		Token   string `json:"token"`
		JoinURL string `json:"join_url"`
	}
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/home/members/invitations", map[string]any{
		"email": "new-member@example.com",
	}, &invite)
	if invite.Token == "" {
		t.Fatal("expected invite token")
	}
	if invite.JoinURL == "" {
		t.Fatal("expected join URL")
	}

	var preview struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	requestJSON(t, testServer, "", http.MethodPost, "/v1/auth/invitations/preview", map[string]any{
		"token": invite.Token,
	}, &preview)
	if preview.Email != "new-member@example.com" || preview.Role != domain.HomeRoleMember {
		t.Fatalf("preview = %#v", preview)
	}

	var signup struct {
		User         domain.User `json:"user"`
		SessionToken string      `json:"session_token"`
	}
	requestJSON(t, testServer, "", http.MethodPost, "/v1/auth/invitations/signup", map[string]any{
		"token":    invite.Token,
		"email":    "new-member@example.com",
		"password": "new-member-password",
	}, &signup)
	if signup.User.ID == "" || signup.User.Email != "new-member@example.com" {
		t.Fatalf("signup user = %#v", signup.User)
	}
	if signup.SessionToken == "" {
		t.Fatal("expected signup session token")
	}

	if _, err := db.GetHomeMembership(ctx, home.ID, signup.User.ID); err != nil {
		t.Fatalf("new user membership: %v", err)
	}
	user, err := db.GetUserByEmail(ctx, "new-member@example.com")
	if err != nil {
		t.Fatalf("new user lookup: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("new-member-password")); err != nil {
		t.Fatalf("stored password hash did not match signup password: %v", err)
	}
	if user.PasswordChangeRequired {
		t.Fatal("invite signup should not require password change")
	}

	requestJSON(t, testServer, signup.SessionToken, http.MethodGet, "/v1/home", nil, &domain.Home{})
}

func TestAdminPasswordResetRevokesSessionsAndRequiresPasswordChange(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_reset_admin", Email: "admin-reset@example.com", PasswordHash: string(mustPasswordHash(t, "admin-password")), CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_reset_member", Email: "member-reset@example.com", PasswordHash: string(mustPasswordHash(t, "old-password")), CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_reset", UserID: admin.ID, Name: "Reset Home", CreatedAt: now, UpdatedAt: now}
	adminToken := "admin-reset-token"
	memberToken := "member-reset-token"
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_reset_admin", UserID: admin.ID, TokenHash: hashToken(adminToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_reset_member", UserID: member.ID, TokenHash: hashToken(memberToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, adminToken, http.MethodPut, "/v1/home/members/"+member.ID+"/password", map[string]any{
		"temporary_password":       "temporary-password",
		"password_change_required": true,
	}, nil)

	if _, err := db.GetSessionByHash(ctx, hashToken(memberToken)); !isStoreNotFound(err) {
		t.Fatalf("old member session lookup = %v, want ErrNotFound", err)
	}
	updated, err := db.GetUserByID(ctx, member.ID)
	if err != nil {
		t.Fatalf("updated member lookup: %v", err)
	}
	if !updated.PasswordChangeRequired {
		t.Fatal("member should require password change")
	}
	if updated.PasswordResetAt == nil || updated.PasswordResetBy != admin.ID {
		t.Fatalf("reset metadata = at:%v by:%q", updated.PasswordResetAt, updated.PasswordResetBy)
	}

	login := requestJSONStatus(t, testServer, "", http.MethodPost, "/v1/auth/login", map[string]any{
		"email":    member.Email,
		"password": "temporary-password",
	}, http.StatusOK)
	defer login.Body.Close()

	var loginPayload struct {
		SessionToken string      `json:"session_token"`
		User         domain.User `json:"user"`
	}
	readJSONResponse(t, login, &loginPayload)
	if !loginPayload.User.PasswordChangeRequired {
		t.Fatal("login payload should advertise password change requirement")
	}

	response := requestJSONStatus(t, testServer, loginPayload.SessionToken, http.MethodGet, "/v1/home", nil, http.StatusForbidden)
	response.Body.Close()

	requestJSON(t, testServer, loginPayload.SessionToken, http.MethodPost, "/v1/auth/change-password", map[string]any{
		"current_password": "temporary-password",
		"new_password":     "new-member-password",
	}, nil)
	requestJSON(t, testServer, loginPayload.SessionToken, http.MethodGet, "/v1/home", nil, &domain.Home{})
}

func isStoreNotFound(err error) bool {
	return err == store.ErrNotFound
}

func readJSONResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
}
