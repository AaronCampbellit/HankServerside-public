package cloud

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestProfileBackupGetReturnsNotFoundWhenMissing(t *testing.T) {
	t.Parallel()

	testServer, sessionToken := setupProfileBackupTestServer(t)
	defer testServer.Close()

	response := requestJSONStatus(t, testServer, sessionToken, http.MethodGet, "/v1/me/profile-backup", nil, http.StatusNotFound)
	defer response.Body.Close()
}

func TestProfileBackupPutAndGetRoundTrip(t *testing.T) {
	t.Parallel()

	testServer, sessionToken := setupProfileBackupTestServer(t)
	defer testServer.Close()

	snapshot := json.RawMessage(`{"schemaVersion":4,"profile":{"displayName":"Hank User"},"notes":{"ciphertext":"abc"}}`)
	var saved struct {
		Revision  int             `json:"revision"`
		UpdatedAt time.Time       `json:"updated_at"`
		Snapshot  json.RawMessage `json:"snapshot"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodPut, "/v1/me/profile-backup", map[string]any{
		"snapshot": json.RawMessage(snapshot),
	}, &saved)

	if saved.Revision != 1 {
		t.Fatalf("revision = %d, want 1", saved.Revision)
	}
	if !jsonEqual(saved.Snapshot, snapshot) {
		t.Fatalf("saved snapshot = %s, want %s", string(saved.Snapshot), string(snapshot))
	}

	var fetched struct {
		Revision  int             `json:"revision"`
		UpdatedAt time.Time       `json:"updated_at"`
		Snapshot  json.RawMessage `json:"snapshot"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/me/profile-backup", nil, &fetched)

	if fetched.Revision != 1 {
		t.Fatalf("fetched revision = %d, want 1", fetched.Revision)
	}
	if fetched.UpdatedAt.IsZero() {
		t.Fatal("expected updated_at to be set")
	}
	if !jsonEqual(fetched.Snapshot, snapshot) {
		t.Fatalf("fetched snapshot = %s, want %s", string(fetched.Snapshot), string(snapshot))
	}
}

func TestProfileBackupPutMatchingRevisionIncrementsRevision(t *testing.T) {
	t.Parallel()

	testServer, sessionToken := setupProfileBackupTestServer(t)
	defer testServer.Close()

	requestJSON(t, testServer, sessionToken, http.MethodPut, "/v1/me/profile-backup", map[string]any{
		"snapshot": json.RawMessage(`{"schemaVersion":4,"profile":{"displayName":"First"}}`),
	}, nil)

	var updated struct {
		Revision int             `json:"revision"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodPut, "/v1/me/profile-backup", map[string]any{
		"expected_revision": 1,
		"snapshot":          json.RawMessage(`{"schemaVersion":4,"profile":{"displayName":"Second"}}`),
	}, &updated)

	if updated.Revision != 2 {
		t.Fatalf("revision = %d, want 2", updated.Revision)
	}
	if !jsonEqual(updated.Snapshot, json.RawMessage(`{"schemaVersion":4,"profile":{"displayName":"Second"}}`)) {
		t.Fatalf("updated snapshot = %s", string(updated.Snapshot))
	}
}

func TestProfileBackupPutStaleRevisionReturnsConflict(t *testing.T) {
	t.Parallel()

	testServer, sessionToken := setupProfileBackupTestServer(t)
	defer testServer.Close()

	requestJSON(t, testServer, sessionToken, http.MethodPut, "/v1/me/profile-backup", map[string]any{
		"snapshot": json.RawMessage(`{"schemaVersion":4,"profile":{"displayName":"Current"}}`),
	}, nil)

	response := requestJSONStatus(t, testServer, sessionToken, http.MethodPut, "/v1/me/profile-backup", map[string]any{
		"expected_revision": 0,
		"snapshot":          json.RawMessage(`{"schemaVersion":4,"profile":{"displayName":"Stale"}}`),
	}, http.StatusConflict)
	defer response.Body.Close()

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != "conflict" {
		t.Fatalf("error = %v, want conflict", payload["error"])
	}
}

func setupProfileBackupTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	ctx := context.Background()
	db := storeForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	user := domain.User{ID: "usr_profile_backup", Email: "profile@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	sessionToken := "profile-session-token"
	session := domain.AppSession{
		ID:        "sess_profile_backup",
		UserID:    user.ID,
		TokenHash: hashToken(sessionToken),
		ExpiresAt: now.Add(time.Hour),
		CreatedAt: now,
	}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	return httptest.NewServer(server.http.Handler), sessionToken
}

func jsonEqual(left json.RawMessage, right json.RawMessage) bool {
	var leftValue any
	var rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	leftCanonical, err := json.Marshal(leftValue)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(rightValue)
	if err != nil {
		return false
	}
	return string(leftCanonical) == string(rightCanonical)
}
