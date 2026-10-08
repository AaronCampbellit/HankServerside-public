package cloud

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/desktopcrypto"
	"github.com/dropfile/HankServerside/internal/domain"
)

func TestDesktopTrustCertificateValidation(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	identityKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&identityKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	claims := desktopcrypto.IdentityCertificateClaims{
		CertificateVersion: "desktop.v1", HomeID: "home_0001", IdentityID: "did_operator_0001",
		IdentityType: domain.DesktopIdentityOperatorDevice, UserID: "usr_0001", DeviceID: "device_0001",
		PublicKeySPKI: spki, Capabilities: append([]string(nil), desktopRequiredAdminCapabilities...),
		TrustRootGeneration: 1, CreatedAtUnixMS: now.UnixMilli(), ExpiresAtUnixMS: now.Add(365 * 24 * time.Hour).UnixMilli(),
	}
	encoded, err := desktopcrypto.EncodeIdentityCertificate(claims)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	signature, err := ecdsa.SignASN1(rand.Reader, signer, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, _ := json.Marshal(desktopSignedCertificateEnvelope{
		Claims: base64.RawURLEncoding.EncodeToString(encoded), Signature: base64.RawURLEncoding.EncodeToString(signature),
	})
	request := desktopOperatorDeviceRequest{
		IdentityID: claims.IdentityID, DeviceID: claims.DeviceID,
		PublicKeySPKI: base64.RawURLEncoding.EncodeToString(spki),
		Certificate:   base64.RawURLEncoding.EncodeToString(envelopeJSON),
		Capabilities:  claims.Capabilities, ExpiresAt: time.UnixMilli(claims.ExpiresAtUnixMS),
	}
	certificate, err := validateDesktopOperatorRequest(request, claims.HomeID, claims.UserID, 1, now, true)
	if err != nil {
		t.Fatalf("validateDesktopOperatorRequest: %v", err)
	}
	if err := desktopcrypto.VerifyP256Signature(&signer.PublicKey, certificate.SignedClaims, certificate.Signature); err != nil {
		t.Fatalf("certificate signature: %v", err)
	}

	request.ExpiresAt = request.ExpiresAt.Add(time.Second)
	if _, err := validateDesktopOperatorRequest(request, claims.HomeID, claims.UserID, 1, now, true); err == nil {
		t.Fatal("mismatched expiry accepted")
	}
}

func TestDesktopCertificateWithIssuerBuildsVerifiableDelegatedChain(t *testing.T) {
	leaf := validatedDesktopCertificate{Identity: domain.DesktopIdentity{Certificate: []byte(`{"claims":"bGVhZg","signature":"c2ln"}`)}}
	issuer := []byte(`{"claims":"aXNzdWVy","signature":"cm9vdC1zaWc"}`)
	chained, err := certificateWithIssuer(leaf, issuer)
	if err != nil {
		t.Fatal(err)
	}
	var envelope desktopSignedCertificateEnvelope
	if err := json.Unmarshal(chained.Identity.Certificate, &envelope); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(envelope.IssuerCertificate)
	if err != nil || string(decoded) != string(issuer) {
		t.Fatalf("issuer chain = %q, err=%v", decoded, err)
	}
}

func TestDesktopIdentityPublicSnapshotReturnsInstalledCertificateChain(t *testing.T) {
	certificate := []byte(`{"claims":"bGVhZg","signature":"c2ln","issuer_certificate":"aXNzdWVy"}`)
	publicKey := []byte("endpoint-public-key")
	snapshot := desktopIdentityPublicSnapshot(domain.DesktopIdentity{
		ID:            "did_endpoint_0001",
		IdentityType:  domain.DesktopIdentityEndpoint,
		AgentID:       "agent_0001",
		PublicKeySPKI: publicKey,
		Certificate:   certificate,
	})

	if got, want := snapshot["certificate"], base64.RawURLEncoding.EncodeToString(certificate); got != want {
		t.Fatalf("certificate = %v, want installed delegated chain", got)
	}
	if got, want := snapshot["public_key_spki"], base64.RawURLEncoding.EncodeToString(publicKey); got != want {
		t.Fatalf("public_key_spki = %v, want %v", got, want)
	}
}

func TestDesktopCertificateChainActivityRejectsRevokedIssuer(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	makeCertificate := func(claims desktopcrypto.IdentityCertificateClaims, issuer []byte) []byte {
		encoded, err := desktopcrypto.EncodeIdentityCertificate(claims)
		if err != nil {
			t.Fatal(err)
		}
		envelope := desktopSignedCertificateEnvelope{Claims: base64.RawURLEncoding.EncodeToString(encoded), Signature: base64.RawURLEncoding.EncodeToString([]byte("signature"))}
		if issuer != nil {
			envelope.IssuerCertificate = base64.RawURLEncoding.EncodeToString(issuer)
		}
		value, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	issuerClaims := desktopcrypto.IdentityCertificateClaims{CertificateVersion: "desktop.v1", HomeID: "home_1", IdentityID: "did_issuer_1",
		IdentityType: domain.DesktopIdentityOperatorDevice, UserID: "usr_issuer", DeviceID: "device_issuer", PublicKeySPKI: []byte("issuer-key"),
		Capabilities: []string{"operator.approve"}, TrustRootGeneration: 1, CreatedAtUnixMS: now.Add(-time.Hour).UnixMilli(), ExpiresAtUnixMS: now.Add(time.Hour).UnixMilli()}
	issuerCertificate := makeCertificate(issuerClaims, nil)
	leafClaims := desktopcrypto.IdentityCertificateClaims{CertificateVersion: "desktop.v1", HomeID: "home_1", IdentityID: "did_leaf_1",
		IdentityType: domain.DesktopIdentityOperatorDevice, UserID: "usr_leaf", DeviceID: "device_leaf", PublicKeySPKI: []byte("leaf-key"),
		Capabilities: []string{"endpoint.approve"}, TrustRootGeneration: 1, CreatedAtUnixMS: now.Add(-time.Hour).UnixMilli(), ExpiresAtUnixMS: now.Add(time.Hour).UnixMilli()}
	leafCertificate := makeCertificate(leafClaims, issuerCertificate)
	identities := []domain.DesktopIdentity{
		{ID: issuerClaims.IdentityID, HomeID: "home_1", IdentityType: domain.DesktopIdentityOperatorDevice, PublicKeySPKI: issuerClaims.PublicKeySPKI,
			Certificate: issuerCertificate, TrustRootGeneration: 1, ExpiresAt: now.Add(time.Hour)},
		{ID: leafClaims.IdentityID, HomeID: "home_1", IdentityType: domain.DesktopIdentityOperatorDevice, PublicKeySPKI: leafClaims.PublicKeySPKI,
			Certificate: leafCertificate, TrustRootGeneration: 1, ExpiresAt: now.Add(time.Hour)},
	}
	if !desktopCertificateChainIsActive(leafCertificate, "home_1", 1, now, identities) {
		t.Fatal("active delegated chain rejected")
	}
	revokedAt := now.Add(-time.Minute)
	identities[0].RevokedAt = &revokedAt
	if desktopCertificateChainIsActive(leafCertificate, "home_1", 1, now, identities) {
		t.Fatal("revoked issuer accepted")
	}
}

func TestDesktopTrustKeyValidationRejectsWrongCurve(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeDesktopP256SPKI(base64.RawURLEncoding.EncodeToString(spki)); err == nil {
		t.Fatal("P-384 key accepted")
	}
}

func TestDesktopPendingEnrollmentValidationKeepsOnlyPublicEndpointMaterial(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	request := desktopEndpointApprovalRequest{
		IdentityType: domain.DesktopIdentityEndpoint, IdentityID: "dep_pending", AgentID: "agent_0001",
		PublicKeySPKI: base64.RawURLEncoding.EncodeToString(spki), Capabilities: []string{"desktop.view", "desktop.control"},
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Platform: "macos",
	}
	stored, err := validateDesktopPendingEnrollmentRequest(request, "agent_0001", now)
	if err != nil || string(stored) != string(spki) {
		t.Fatalf("valid pending enrollment rejected: key=%x err=%v", stored, err)
	}
	request.Platform = "windows"
	if stored, err = validateDesktopPendingEnrollmentRequest(request, "agent_0001", now); err != nil || string(stored) != string(spki) {
		t.Fatalf("valid Windows pending enrollment rejected: key=%x err=%v", stored, err)
	}
	request.Platform = "linux"
	if _, err := validateDesktopPendingEnrollmentRequest(request, "agent_0001", now); err == nil {
		t.Fatal("unsupported pending enrollment platform accepted")
	}
	request.Platform = "macos"
	request.AgentID = "agent_other"
	if _, err := validateDesktopPendingEnrollmentRequest(request, "agent_0001", now); err == nil {
		t.Fatal("cross-agent pending enrollment accepted")
	}
	request.AgentID, request.ExpiresAt = "agent_0001", now.Add(-time.Second)
	if _, err := validateDesktopPendingEnrollmentRequest(request, "agent_0001", now); err == nil {
		t.Fatal("expired pending enrollment accepted")
	}
	request.ExpiresAt, request.CreatedAt = now.Add(time.Hour), now.Add(-6*time.Minute)
	if _, err := validateDesktopPendingEnrollmentRequest(request, "agent_0001", now); err == nil {
		t.Fatal("stale pending enrollment accepted")
	}
}

func TestDesktopTrustValidationRejectsMalformedOversizedAndUnsafeValues(t *testing.T) {
	if _, _, err := decodeDesktopP256SPKI("not-base64url!"); err == nil {
		t.Fatal("invalid base64url accepted")
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSPKI, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeDesktopP256SPKI(base64.RawURLEncoding.EncodeToString(rsaSPKI)); err == nil {
		t.Fatal("RSA key accepted")
	}
	oversized := base64.RawURLEncoding.EncodeToString(make([]byte, desktopTrustMaxBlobBytes+1))
	if _, err := decodeDesktopBlob(oversized); err == nil {
		t.Fatal("oversized trust blob accepted")
	}
	if err := validateDesktopCapabilities(domain.DesktopIdentityOperatorDevice, []string{"operator.approve"}, true); err == nil {
		t.Fatal("dead-end replacement operator accepted")
	}
	if validDesktopReasonCode("clipboard content") || validDesktopReasonCode(string(make([]byte, 129))) {
		t.Fatal("unsafe reason code accepted")
	}
}

func TestDesktopAuditMetadataNeverIncludesSensitivePayloads(t *testing.T) {
	encoded, err := json.Marshal(desktopAuditMetadata("operator_device", "did_0001", "fingerprint", 1, []string{"operator.approve"}, "approved"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_key", "recovery_secret", "recovery_envelope", "join_credential", "certificate", "signature", "challenge"} {
		if string(encoded) == "" || containsFold(string(encoded), forbidden) {
			t.Fatalf("audit metadata contains %q: %s", forbidden, encoded)
		}
	}
}
