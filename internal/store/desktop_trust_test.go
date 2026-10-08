package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestDesktopIdentitySessionScopeAcceptsNullableOperatorAgentID(t *testing.T) {
	scope, scopeID, err := desktopIdentitySessionScope(domain.DesktopIdentityOperatorDevice, "did_operator", sql.NullString{})
	if err != nil || scope != "operator_device_identity_id = ?" || scopeID != "did_operator" {
		t.Fatalf("operator scope = %q %q, err=%v", scope, scopeID, err)
	}
	if _, _, err := desktopIdentitySessionScope(domain.DesktopIdentityEndpoint, "did_endpoint", sql.NullString{}); err == nil {
		t.Fatal("endpoint identity without agent scope accepted")
	}
}

func TestBootstrapDesktopTrustPersistsServerManagedAuthority(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	home, user, _ := seedDesktopOwnerAgent(t, db, "server-managed")
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := testDesktopTrustRoot(home.ID, 1, now)
	root.RecoveryEnvelope = nil
	root.AuthorityMode = "server_managed"
	root.EncryptedPrivateKey = "encrypted-private-key"
	if err := db.BootstrapDesktopTrust(ctx, root, testDesktopOperator(home.ID, user.ID, "server-managed-browser", 1, now)); err != nil {
		t.Fatalf("BootstrapDesktopTrust: %v", err)
	}
	stored, err := db.GetDesktopTrustRoot(ctx, home.ID)
	if err != nil {
		t.Fatalf("GetDesktopTrustRoot: %v", err)
	}
	if stored.AuthorityMode != root.AuthorityMode || stored.EncryptedPrivateKey != root.EncryptedPrivateKey {
		t.Fatalf("stored authority = mode:%q encrypted:%t", stored.AuthorityMode, stored.EncryptedPrivateKey != "")
	}
}

func TestDesktopTrustLifecycleIsHomeScopedAndResetRevokesIdentities(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	homeA, userA, agentA := seedDesktopOwnerAgent(t, db, "a")
	homeB, _, _ := seedDesktopOwnerAgent(t, db, "b")
	now := time.Now().UTC().Truncate(time.Microsecond)

	root := testDesktopTrustRoot(homeA.ID, 1, now)
	operator := testDesktopOperator(homeA.ID, userA.ID, "device-a", 1, now)
	if err := db.BootstrapDesktopTrust(ctx, root, operator); err != nil {
		t.Fatalf("BootstrapDesktopTrust: %v", err)
	}
	endpoint := testDesktopEndpoint(homeA.ID, agentA.ID, 1, now)
	if err := db.CreateDesktopIdentity(ctx, endpoint); err != nil {
		t.Fatalf("CreateDesktopIdentity endpoint: %v", err)
	}
	pending := domain.DesktopPendingEnrollment{HomeID: homeA.ID, AgentID: agentA.ID, RequestJSON: []byte(`{"identity_id":"dep_pending"}`), Fingerprint: "fp-pending", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.UpsertDesktopPendingEnrollment(ctx, pending); err != nil {
		t.Fatalf("UpsertDesktopPendingEnrollment: %v", err)
	}
	if _, err := db.GetActiveDesktopEndpointIdentity(ctx, homeB.ID, agentA.ID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-home identity = %v, want ErrNotFound", err)
	}

	resetAt := now.Add(time.Minute)
	resetRoot := testDesktopTrustRoot(homeA.ID, 2, resetAt)
	replacement := testDesktopOperator(homeA.ID, userA.ID, "device-a-2", 2, resetAt)
	if err := db.ResetDesktopTrust(ctx, resetRoot, replacement, resetAt, "cryptographic_reset"); err != nil {
		t.Fatalf("ResetDesktopTrust: %v", err)
	}
	if _, err := db.GetActiveDesktopOperatorIdentity(ctx, homeA.ID, userA.ID, "device-a", resetAt.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("operator survived reset: %v", err)
	}
	if _, err := db.GetActiveDesktopEndpointIdentity(ctx, homeA.ID, agentA.ID, resetAt.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("endpoint survived reset: %v", err)
	}
	if _, err := db.GetActiveDesktopOperatorIdentity(ctx, homeA.ID, userA.ID, "device-a-2", resetAt.Add(time.Minute)); err != nil {
		t.Fatalf("replacement operator missing: %v", err)
	}
	if _, err := db.GetDesktopPendingEnrollment(ctx, homeA.ID, agentA.ID, resetAt.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending enrollment survived reset: %v", err)
	}

	storedRoot, err := db.GetDesktopTrustRoot(ctx, homeA.ID)
	if err != nil {
		t.Fatalf("GetDesktopTrustRoot: %v", err)
	}
	if storedRoot.Generation != 2 || storedRoot.Fingerprint != resetRoot.Fingerprint {
		t.Fatalf("root after reset = %#v", storedRoot)
	}
}

func TestEnsureServerManagedDesktopTrustIsIdempotentAfterLegacyMigration(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	if err := db.ConfigureSecretEncryption("desktop-auto-enrollment-test-key"); err != nil {
		t.Fatalf("ConfigureSecretEncryption: %v", err)
	}
	ctx := context.Background()
	home, user, _ := seedDesktopOwnerAgent(t, db, "server-managed")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.BootstrapDesktopTrust(ctx, testDesktopTrustRoot(home.ID, 1, now), testDesktopOperator(home.ID, user.ID, "legacy-browser", 1, now)); err != nil {
		t.Fatalf("BootstrapDesktopTrust: %v", err)
	}

	first, firstKey, migrated, err := db.EnsureServerManagedDesktopTrust(ctx, home.ID, now.Add(time.Second))
	if err != nil || !migrated || first.AuthorityMode != "server_managed" || first.Generation != 2 || firstKey == nil {
		t.Fatalf("first server-managed authority = %#v migrated=%v key=%v err=%v", first, migrated, firstKey != nil, err)
	}
	second, secondKey, migrated, err := db.EnsureServerManagedDesktopTrust(ctx, home.ID, now.Add(2*time.Second))
	if err != nil || migrated || second.AuthorityMode != "server_managed" || second.Generation != first.Generation || second.Fingerprint != first.Fingerprint || secondKey == nil {
		t.Fatalf("second server-managed authority = %#v migrated=%v key=%v err=%v", second, migrated, secondKey != nil, err)
	}
}

func TestDesktopTrustRecoveryChallengeIsSingleUse(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	home, user, _ := seedDesktopOwnerAgent(t, db, "recovery")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.BootstrapDesktopTrust(ctx, testDesktopTrustRoot(home.ID, 1, now), testDesktopOperator(home.ID, user.ID, "device-original", 1, now)); err != nil {
		t.Fatalf("BootstrapDesktopTrust: %v", err)
	}

	challengeHash := []byte("challenge-hash")
	if err := db.IssueDesktopRecoveryChallenge(ctx, home.ID, challengeHash, now.Add(time.Minute)); err != nil {
		t.Fatalf("IssueDesktopRecoveryChallenge: %v", err)
	}
	recovered := testDesktopOperator(home.ID, user.ID, "device-recovered", 1, now.Add(time.Second))
	if err := db.ConsumeDesktopRecoveryChallengeAndCreateOperator(ctx, home.ID, 1, challengeHash, now.Add(time.Second), recovered); err != nil {
		t.Fatalf("ConsumeDesktopRecoveryChallengeAndCreateOperator: %v", err)
	}
	replayed := testDesktopOperator(home.ID, user.ID, "device-replayed", 1, now.Add(2*time.Second))
	if err := db.ConsumeDesktopRecoveryChallengeAndCreateOperator(ctx, home.ID, 1, challengeHash, now.Add(2*time.Second), replayed); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed challenge = %v, want ErrConflict", err)
	}
}

func TestDesktopIdentityValidationAndTrustConflicts(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	home, user, agent := seedDesktopOwnerAgent(t, db, "negative")
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := testDesktopTrustRoot(home.ID, 1, now)
	operator := testDesktopOperator(home.ID, user.ID, "device-negative", 1, now)
	if err := db.BootstrapDesktopTrust(ctx, root, operator); err != nil {
		t.Fatalf("BootstrapDesktopTrust: %v", err)
	}

	malformedOperator := testDesktopOperator(home.ID, user.ID, "device-malformed", 1, now)
	malformedOperator.AgentID = agent.ID
	if err := db.CreateDesktopIdentity(ctx, malformedOperator); err == nil {
		t.Fatal("operator with agent scope accepted")
	}
	malformedEndpoint := testDesktopEndpoint(home.ID, agent.ID, 1, now)
	malformedEndpoint.UserID = user.ID
	if err := db.CreateDesktopIdentity(ctx, malformedEndpoint); err == nil {
		t.Fatal("endpoint with user scope accepted")
	}

	duplicate := testDesktopEndpoint(home.ID, agent.ID, 1, now)
	duplicate.Fingerprint = operator.Fingerprint
	if err := db.CreateDesktopIdentity(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate fingerprint = %v, want ErrConflict", err)
	}

	if changed, sessions, err := db.RevokeDesktopIdentity(ctx, "home-wrong", operator.ID, "test_revoke", now.Add(time.Minute)); err != nil || changed || len(sessions) != 0 {
		t.Fatalf("wrong-home revoke changed=%v err=%v", changed, err)
	}
	if _, err := db.GetActiveDesktopOperatorIdentity(ctx, home.ID, user.ID, operator.DeviceID, operator.ExpiresAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired identity lookup = %v, want ErrNotFound", err)
	}
	current, err := db.GetCurrentDesktopOperatorIdentity(ctx, home.ID, operator.DeviceID)
	if err != nil || current.ID != operator.ID {
		t.Fatalf("current expired identity = %#v, err=%v", current, err)
	}
	if err := db.ReplaceDesktopRecoveryEnvelope(ctx, home.ID, 2, []byte("replacement"), now.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-generation envelope replacement = %v, want ErrConflict", err)
	}

	skippedRoot := testDesktopTrustRoot(home.ID, 3, now.Add(time.Minute))
	skippedOperator := testDesktopOperator(home.ID, user.ID, "device-skipped", 3, now.Add(time.Minute))
	if err := db.RotateDesktopTrust(ctx, skippedRoot, skippedOperator, now.Add(time.Minute), "trust_rotated"); !errors.Is(err, ErrConflict) {
		t.Fatalf("generation skip = %v, want ErrConflict", err)
	}
}

func seedDesktopOwnerAgent(t *testing.T, db *Store, suffix string) (domain.Home, domain.User, domain.Agent) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_desktop_" + suffix, Email: "desktop-" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_desktop_" + suffix, UserID: user.ID, Name: "Desktop " + suffix, CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_desktop_" + suffix, HomeID: home.ID, Name: "Desktop Agent", Status: domain.AgentStatusOnline, AgentType: "primary", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}
	if err := db.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	return home, user, agent
}

func testDesktopTrustRoot(homeID string, generation int, createdAt time.Time) domain.DesktopTrustRoot {
	return domain.DesktopTrustRoot{
		HomeID:           homeID,
		Generation:       generation,
		Algorithm:        domain.DesktopTrustAlgorithm,
		PublicKeySPKI:    []byte("root-" + homeID),
		Fingerprint:      "fp-root-" + homeID + "-" + time.Duration(generation).String(),
		RecoveryEnvelope: []byte("encrypted-recovery-envelope"),
		CreatedAt:        createdAt,
	}
}

func testDesktopOperator(homeID, userID, deviceID string, generation int, createdAt time.Time) domain.DesktopIdentity {
	return domain.DesktopIdentity{
		ID:                  "did_operator_" + deviceID,
		HomeID:              homeID,
		IdentityType:        domain.DesktopIdentityOperatorDevice,
		UserID:              userID,
		DeviceID:            deviceID,
		PublicKeySPKI:       []byte("operator-public-key"),
		Certificate:         []byte("operator-certificate"),
		Fingerprint:         "fp-operator-" + deviceID,
		Capabilities:        []string{"endpoint.approve", "trust.recover"},
		TrustRootGeneration: generation,
		CreatedAt:           createdAt,
		ExpiresAt:           createdAt.AddDate(1, 0, 0),
	}
}

func testDesktopEndpoint(homeID, agentID string, generation int, createdAt time.Time) domain.DesktopIdentity {
	return domain.DesktopIdentity{
		ID:                  "did_endpoint_" + agentID,
		HomeID:              homeID,
		IdentityType:        domain.DesktopIdentityEndpoint,
		AgentID:             agentID,
		PublicKeySPKI:       []byte("endpoint-public-key"),
		Certificate:         []byte("endpoint-certificate"),
		Fingerprint:         "fp-endpoint-" + agentID,
		Capabilities:        []string{"desktop.view", "desktop.control"},
		TrustRootGeneration: generation,
		CreatedAt:           createdAt,
		ExpiresAt:           createdAt.AddDate(1, 0, 0),
	}
}
