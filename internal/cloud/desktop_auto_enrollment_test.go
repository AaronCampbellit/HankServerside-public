package cloud

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/desktopcrypto"
	"github.com/dropfile/HankServerside/internal/domain"
)

func TestSameDesktopIdentityEnrollmentRequiresSameKeyAndAuthSession(t *testing.T) {
	existing := domain.DesktopIdentity{
		HomeID: "home_1", IdentityType: domain.DesktopIdentityOperatorDevice,
		UserID: "user_1", DeviceID: "browser_1", AuthSessionID: "session_1",
		TrustRootGeneration: 2, Fingerprint: "fingerprint_1", PublicKeySPKI: []byte("public-key-1"),
	}
	if !sameDesktopIdentityEnrollment(existing, existing) {
		t.Fatal("identical enrollment was not reusable")
	}

	differentSession := existing
	differentSession.AuthSessionID = "session_2"
	if sameDesktopIdentityEnrollment(existing, differentSession) {
		t.Fatal("enrollment from another auth session was reused")
	}

	differentKey := existing
	differentKey.PublicKeySPKI = []byte("public-key-2")
	if sameDesktopIdentityEnrollment(existing, differentKey) {
		t.Fatal("enrollment with another public key was reused")
	}

	endpoint := existing
	endpoint.IdentityType = domain.DesktopIdentityEndpoint
	endpoint.UserID = ""
	endpoint.DeviceID = ""
	endpoint.AgentID = "agent_1"
	endpoint.AuthSessionID = ""
	if sameDesktopIdentityEnrollment(endpoint, endpoint) {
		t.Fatal("endpoint renewal was treated as a duplicate browser enrollment")
	}
}

func TestInstallServerDesktopIdentityRefreshesExpiredBrowserIdentity(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_expired_browser", Email: "expired-browser@example.com", PasswordHash: "hash", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}
	home := domain.Home{ID: "home_expired_browser", UserID: user.ID, Name: "Expired Browser", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}
	for _, sessionID := range []string{"session_expired", "session_current"} {
		must(t, db.CreateSession(ctx, domain.AppSession{ID: sessionID, UserID: user.ID, TokenHash: hashToken(sessionID), ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-2 * time.Hour)}))
	}
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootSPKI, err := x509.MarshalPKIXPublicKey(&rootKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	browserKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	browserSPKI, err := x509.MarshalPKIXPublicKey(&browserKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	root := domain.DesktopTrustRoot{HomeID: home.ID, Generation: 1, Algorithm: domain.DesktopTrustAlgorithm, PublicKeySPKI: rootSPKI, Fingerprint: desktopcrypto.FingerprintSPKI(rootSPKI), AuthorityMode: "server_managed", EncryptedPrivateKey: "encrypted-private-key", CreatedAt: now.Add(-2 * time.Hour)}
	expired := domain.DesktopIdentity{ID: "dop_expired_browser", HomeID: home.ID, IdentityType: domain.DesktopIdentityOperatorDevice, UserID: user.ID, DeviceID: "browser-expired-session", PublicKeySPKI: browserSPKI, Certificate: []byte("expired-certificate"), Fingerprint: desktopcrypto.FingerprintSPKI(browserSPKI), Capabilities: append([]string(nil), desktopRequiredAdminCapabilities...), TrustRootGeneration: 1, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), AuthSessionID: "session_expired"}
	if err := db.BootstrapDesktopTrust(ctx, root, expired); err != nil {
		t.Fatalf("BootstrapDesktopTrust: %v", err)
	}

	server := &Server{store: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest("POST", "/v1/home/desktop-trust/operator-devices/auto-enroll", nil)
	replacement := domain.DesktopIdentity{ID: "dop_replacement_browser", HomeID: home.ID, IdentityType: domain.DesktopIdentityOperatorDevice, UserID: user.ID, DeviceID: expired.DeviceID, PublicKeySPKI: browserSPKI, Capabilities: append([]string(nil), desktopRequiredAdminCapabilities...), CreatedAt: now, ExpiresAt: now.Add(time.Hour), AuthSessionID: "session_current"}
	installed, err := server.installServerDesktopIdentity(request, home, authContext{User: user}, root, rootKey, replacement, "desktop.identity.auto_enrolled")
	if err != nil {
		t.Fatalf("installServerDesktopIdentity: %v", err)
	}
	if installed.ID != expired.ID || installed.AuthSessionID != replacement.AuthSessionID {
		t.Fatalf("installed identity = %#v, want replacement session", installed)
	}
	active, err := db.GetActiveDesktopOperatorIdentity(ctx, home.ID, user.ID, expired.DeviceID, now)
	if err != nil {
		t.Fatalf("GetActiveDesktopOperatorIdentity: %v", err)
	}
	if active.ID != expired.ID {
		t.Fatalf("active identity ID = %q, want refreshed %q", active.ID, expired.ID)
	}
}
