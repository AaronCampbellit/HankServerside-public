package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestAgentCredentialRotationOverlapsThenRevokesOldCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_credential_rotation", Email: "credential-rotation@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_credential_rotation", UserID: user.ID, Name: "Credential Rotation", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "linux_credential_rotation", HomeID: home.ID, Name: "Linux", Status: domain.AgentStatusOffline, AgentType: "worker", CreatedAt: now, UpdatedAt: now}
	oldHash := strings.Repeat("1", 64)
	newHash := strings.Repeat("2", 64)
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))
	mustStore(t, db.UpsertAgent(ctx, agent))
	mustStore(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_rotation_old", HomeID: home.ID, AgentID: agent.ID, TokenHash: oldHash, Generation: 1, ActivatedAt: &now, ConfirmedAt: &now, CreatedAt: now}))

	state, err := db.BeginAgentCredentialRotation(ctx, BeginAgentCredentialRotationInput{
		HomeID: home.ID, AgentID: agent.ID, CurrentTokenID: "agtok_rotation_old", ReplacementTokenID: "agtok_rotation_new", ReplacementHash: newHash, Now: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.CredentialID != "agtok_rotation_new" || state.Generation != 2 || state.ReplacesCredentialID != "agtok_rotation_old" || state.ConfirmBy == nil || !state.ConfirmBy.Equal(now.Add(25*time.Hour)) {
		t.Fatalf("rotation state = %#v", state)
	}
	if _, err := db.ValidateAgentToken(ctx, oldHash); err != nil {
		t.Fatalf("old credential not valid during overlap: %v", err)
	}
	newRecord, err := db.ValidateAgentToken(ctx, newHash)
	if err != nil || newRecord.Token.ID != "agtok_rotation_new" || newRecord.Token.Generation != 2 || newRecord.Token.ReplacesTokenID != "agtok_rotation_old" || newRecord.Token.ActivatedAt == nil {
		t.Fatalf("replacement credential = %#v, err=%v", newRecord.Token, err)
	}

	confirmed, err := db.ConfirmAgentCredentialRotation(ctx, home.ID, agent.ID, "agtok_rotation_new", "agtok_rotation_new", now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.ConfirmedAt == nil || !confirmed.ConfirmedAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("confirmed state = %#v", confirmed)
	}
	if _, err := db.ValidateAgentToken(ctx, oldHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old credential after confirmation = %v, want ErrNotFound", err)
	}
	if _, err := db.ValidateAgentToken(ctx, newHash); err != nil {
		t.Fatalf("replacement after confirmation = %v", err)
	}
}

func TestAgentCredentialRotationIsIdempotentOnlyForSameReplacementHash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_credential_retry", Email: "credential-retry@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_credential_retry", UserID: user.ID, Name: "Credential Retry", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "linux_credential_retry", HomeID: home.ID, Name: "Linux", Status: domain.AgentStatusOffline, AgentType: "worker", CreatedAt: now, UpdatedAt: now}
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))
	mustStore(t, db.UpsertAgent(ctx, agent))
	mustStore(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_retry_old", HomeID: home.ID, AgentID: agent.ID, TokenHash: strings.Repeat("3", 64), Generation: 1, ActivatedAt: &now, ConfirmedAt: &now, CreatedAt: now}))
	input := BeginAgentCredentialRotationInput{HomeID: home.ID, AgentID: agent.ID, CurrentTokenID: "agtok_retry_old", ReplacementTokenID: "agtok_retry_new", ReplacementHash: strings.Repeat("4", 64), Now: now.Add(time.Hour)}
	first, err := db.BeginAgentCredentialRotation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.ReplacementTokenID = "agtok_retry_different_id"
	second, err := db.BeginAgentCredentialRotation(ctx, input)
	if err != nil || second.CredentialID != first.CredentialID {
		t.Fatalf("same-hash retry = %#v, err=%v", second, err)
	}
	input.ReplacementHash = strings.Repeat("5", 64)
	if _, err := db.BeginAgentCredentialRotation(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("different pending replacement = %v, want ErrConflict", err)
	}
}

func TestAgentCredentialRotationRequestAndRevocationAreDurable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_credential_admin", Email: "credential-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_credential_admin", UserID: user.ID, Name: "Credential Admin", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "linux_credential_admin", HomeID: home.ID, Name: "Linux", Status: domain.AgentStatusOffline, AgentType: "worker", CreatedAt: now, UpdatedAt: now}
	hash := strings.Repeat("6", 64)
	mustStore(t, db.CreateUser(ctx, user))
	mustStore(t, db.CreateHome(ctx, home))
	mustStore(t, db.UpsertAgent(ctx, agent))
	mustStore(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_admin_current", HomeID: home.ID, AgentID: agent.ID, TokenHash: hash, Generation: 1, ActivatedAt: &now, ConfirmedAt: &now, CreatedAt: now}))

	requested, err := db.RequestAgentCredentialRotation(ctx, home.ID, agent.ID, now.Add(time.Hour))
	if err != nil || requested.RotationRequestedAt == nil || !requested.RotationRequestedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("requested state = %#v, err=%v", requested, err)
	}
	status, err := db.GetAgentCredentialState(ctx, home.ID, agent.ID, "agtok_admin_current", now.Add(2*time.Hour))
	if err != nil || !status.RotationRequested || status.RotationDue {
		t.Fatalf("credential status = %#v, err=%v", status, err)
	}
	status, err = db.GetAgentCredentialState(ctx, home.ID, agent.ID, "agtok_admin_current", now.Add(31*24*time.Hour))
	if err != nil || !status.RotationDue || !status.RotationDueAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("due credential status = %#v, err=%v", status, err)
	}
	if err := db.RevokeAgentCredentials(ctx, home.ID, agent.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ValidateAgentToken(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("credential after revocation = %v, want ErrNotFound", err)
	}
}
