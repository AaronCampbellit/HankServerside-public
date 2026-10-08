package desktopcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type testVectors struct {
	Version          string            `json:"version"`
	ValidInitialJoin testInitialJoin   `json:"valid_initial_join"`
	Identities       testIdentities    `json:"identity_certificates"`
	RecoveryEnroll   testRecoveryProof `json:"recovery_enrollment"`
	RecoveryEnvelope testRecovery      `json:"recovery_envelope"`
	RootRotation     testRootRotation  `json:"root_rotation"`
	InvalidRecords   []string          `json:"invalid_records"`
	InvalidTrust     []string          `json:"invalid_trust"`
}

type testInitialJoin struct {
	Transcript testTranscript `json:"transcript"`
	HKDF       testHKDF       `json:"hkdf"`
	Record     testRecord     `json:"record"`
}

type testTranscript struct {
	Label                           string   `json:"label"`
	HomeID                          string   `json:"home_id"`
	SessionID                       string   `json:"session_id"`
	AgentID                         string   `json:"agent_id"`
	OperatorUserID                  string   `json:"operator_user_id"`
	OperatorDeviceID                string   `json:"operator_device_id"`
	Permissions                     []string `json:"permissions"`
	BrowserEphemeralPublicKeyBase64 string   `json:"browser_ephemeral_public_key_base64url"`
	JoinExpiresAtUnixMS             int64    `json:"join_expires_at_unix_ms"`
	HardExpiresAtUnixMS             int64    `json:"hard_expires_at_unix_ms"`
	KeyEpoch                        uint32   `json:"key_epoch"`
	EncodedBase64                   string   `json:"encoded_base64url"`
	SHA256Base64                    string   `json:"sha256_base64url"`
}

type testHKDF struct {
	SharedSecretBase64       string `json:"shared_secret_base64url"`
	BrowserToAgentKeyBase64  string `json:"browser_to_agent_key_base64url"`
	AgentToBrowserKeyBase64  string `json:"agent_to_browser_key_base64url"`
	BrowserNoncePrefixBase64 string `json:"browser_nonce_prefix_base64url"`
	AgentNoncePrefixBase64   string `json:"agent_nonce_prefix_base64url"`
}

type testRecord struct {
	Direction        byte   `json:"direction"`
	Sequence         uint64 `json:"sequence"`
	PlaintextBase64  string `json:"plaintext_base64url"`
	HeaderBase64     string `json:"header_base64url"`
	CiphertextBase64 string `json:"ciphertext_base64url"`
}

type testRecovery struct {
	ContextBase64       string `json:"context_base64url"`
	SaltSHA256Base64    string `json:"salt_sha256_base64url"`
	SecretBase64        string `json:"secret_base64url"`
	KeyBase64           string `json:"key_base64url"`
	NonceBase64         string `json:"nonce_base64url"`
	PrivateKeyBase64    string `json:"root_private_key_pkcs8_base64url"`
	CiphertextTagBase64 string `json:"ciphertext_and_tag_base64url"`
}

type testIdentities struct {
	RootPublicKeyBase64 string       `json:"root_public_key_spki_base64url"`
	Operator            testIdentity `json:"operator_device"`
	Endpoint            testIdentity `json:"endpoint"`
}

type testIdentity struct {
	PublicKeyBase64 string `json:"public_key_spki_base64url"`
	EncodedBase64   string `json:"encoded_base64url"`
	SignatureBase64 string `json:"signature_base64url"`
}

type testRecoveryProof struct {
	ChallengeBase64      string `json:"challenge_base64url"`
	NewOperatorKeyBase64 string `json:"new_operator_public_key_spki_base64url"`
	EncodedBase64        string `json:"encoded_base64url"`
	RootSignatureBase64  string `json:"root_signature_base64url"`
}

type testRootRotation struct {
	NewRootKeyBase64       string `json:"new_root_public_key_spki_base64url"`
	NewEnvelopeHashBase64  string `json:"new_recovery_envelope_sha256_base64url"`
	EncodedBase64          string `json:"encoded_base64url"`
	OldRootSignatureBase64 string `json:"old_root_signature_base64url"`
}

func TestHandshakeTranscriptMatchesGoldenVector(t *testing.T) {
	vector := loadVectors(t).ValidInitialJoin.Transcript
	if vector.Label != HandshakeLabel {
		t.Fatalf("fixture label = %q, want %q", vector.Label, HandshakeLabel)
	}
	encoded, err := EncodeHandshakeTranscript(HandshakeTranscript{
		HomeID:                    vector.HomeID,
		SessionID:                 vector.SessionID,
		AgentID:                   vector.AgentID,
		OperatorUserID:            vector.OperatorUserID,
		OperatorDeviceID:          vector.OperatorDeviceID,
		Permissions:               vector.Permissions,
		BrowserEphemeralPublicKey: decodeBase64(t, vector.BrowserEphemeralPublicKeyBase64),
		JoinExpiresAtUnixMS:       vector.JoinExpiresAtUnixMS,
		HardExpiresAtUnixMS:       vector.HardExpiresAtUnixMS,
		KeyEpoch:                  vector.KeyEpoch,
	})
	if err != nil {
		t.Fatalf("EncodeHandshakeTranscript: %v", err)
	}
	if got := base64.RawURLEncoding.EncodeToString(encoded); got != vector.EncodedBase64 {
		t.Fatalf("encoded transcript = %s, want %s", got, vector.EncodedBase64)
	}
	digest := sha256.Sum256(encoded)
	if got := base64.RawURLEncoding.EncodeToString(digest[:]); got != vector.SHA256Base64 {
		t.Fatalf("transcript digest = %s, want %s", got, vector.SHA256Base64)
	}
}

func TestDirectionalKeysAndRecordMatchGoldenVector(t *testing.T) {
	vector := loadVectors(t).ValidInitialJoin
	transcriptDigest := decodeBase64(t, vector.Transcript.SHA256Base64)
	keys, err := DeriveDirectionalKeys(decodeBase64(t, vector.HKDF.SharedSecretBase64), transcriptDigest)
	if err != nil {
		t.Fatalf("DeriveDirectionalKeys: %v", err)
	}
	assertBase64(t, "browser key", keys.BrowserToAgent[:], vector.HKDF.BrowserToAgentKeyBase64)
	assertBase64(t, "agent key", keys.AgentToBrowser[:], vector.HKDF.AgentToBrowserKeyBase64)
	assertBase64(t, "browser nonce prefix", keys.BrowserNoncePrefix[:], vector.HKDF.BrowserNoncePrefixBase64)
	assertBase64(t, "agent nonce prefix", keys.AgentNoncePrefix[:], vector.HKDF.AgentNoncePrefixBase64)

	header := RecordHeader{
		Version:          RecordVersion,
		Direction:        vector.Record.Direction,
		KeyEpoch:         vector.Transcript.KeyEpoch,
		Sequence:         vector.Record.Sequence,
		CiphertextLength: uint32(len(decodeBase64(t, vector.Record.CiphertextBase64))),
	}
	headerBytes, err := header.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	assertBase64(t, "record header", headerBytes, vector.Record.HeaderBase64)

	aead := mustAEAD(t, keys.BrowserToAgent[:])
	nonce := Nonce(keys.BrowserNoncePrefix, header.Sequence)
	ciphertext := aead.Seal(nil, nonce[:], decodeBase64(t, vector.Record.PlaintextBase64), headerBytes)
	assertBase64(t, "record ciphertext", ciphertext, vector.Record.CiphertextBase64)
}

func TestRecordRejectsWrongEpochDirectionSequenceAndTag(t *testing.T) {
	vector := loadVectors(t).ValidInitialJoin
	keys, err := DeriveDirectionalKeys(
		decodeBase64(t, vector.HKDF.SharedSecretBase64),
		decodeBase64(t, vector.Transcript.SHA256Base64),
	)
	if err != nil {
		t.Fatalf("DeriveDirectionalKeys: %v", err)
	}
	aead := mustAEAD(t, keys.BrowserToAgent[:])
	validHeader := RecordHeader{
		Version: RecordVersion, Direction: DirectionBrowserToAgent,
		KeyEpoch: vector.Transcript.KeyEpoch, Sequence: vector.Record.Sequence,
		CiphertextLength: uint32(len(decodeBase64(t, vector.Record.CiphertextBase64))),
	}
	ciphertext := decodeBase64(t, vector.Record.CiphertextBase64)

	for _, mutation := range []string{"epoch", "direction", "sequence", "tag"} {
		t.Run(mutation, func(t *testing.T) {
			header := validHeader
			candidate := append([]byte(nil), ciphertext...)
			switch mutation {
			case "epoch":
				header.KeyEpoch++
			case "direction":
				header.Direction = DirectionAgentToBrowser
			case "sequence":
				header.Sequence++
			case "tag":
				candidate[len(candidate)-1] ^= 1
			}
			headerBytes, marshalErr := header.MarshalBinary()
			if marshalErr != nil {
				t.Fatalf("MarshalBinary: %v", marshalErr)
			}
			nonce := Nonce(keys.BrowserNoncePrefix, header.Sequence)
			_, openErr := aead.Open(nil, nonce[:], candidate, headerBytes)
			if openErr == nil {
				t.Fatalf("%s mutation accepted", mutation)
			}
		})
	}
}

func TestRecoveryContextAndKeyMatchGoldenVector(t *testing.T) {
	vector := loadVectors(t).RecoveryEnvelope
	context, err := EncodeRecoveryContext("home_fixture", 1)
	if err != nil {
		t.Fatalf("EncodeRecoveryContext: %v", err)
	}
	assertBase64(t, "recovery context", context, vector.ContextBase64)
	salt := sha256.Sum256(context)
	assertBase64(t, "recovery salt", salt[:], vector.SaltSHA256Base64)
	key, err := DeriveRecoveryKey(decodeBase64(t, vector.SecretBase64), context)
	if err != nil {
		t.Fatalf("DeriveRecoveryKey: %v", err)
	}
	assertBase64(t, "recovery key", key[:], vector.KeyBase64)

	aead := mustAEAD(t, key[:])
	plaintext, err := aead.Open(
		nil,
		decodeBase64(t, vector.NonceBase64),
		decodeBase64(t, vector.CiphertextTagBase64),
		context,
	)
	if err != nil {
		t.Fatalf("decrypt recovery envelope: %v", err)
	}
	assertBase64(t, "recovery plaintext", plaintext, vector.PrivateKeyBase64)
}

func TestIdentityCertificatesAndSignaturesMatchGoldenVectors(t *testing.T) {
	vectors := loadVectors(t)
	rootKey := parseP256PublicKey(t, decodeBase64(t, vectors.Identities.RootPublicKeyBase64))
	operatorKey := parseP256PublicKey(t, decodeBase64(t, vectors.Identities.Operator.PublicKeyBase64))

	operatorEncoded, err := EncodeIdentityCertificate(IdentityCertificateClaims{
		CertificateVersion:  "desktop.v1",
		HomeID:              "home_fixture",
		IdentityID:          "did_operator_fixture",
		IdentityType:        "operator_device",
		UserID:              "usr_fixture",
		DeviceID:            "device_fixture",
		PublicKeySPKI:       decodeBase64(t, vectors.Identities.Operator.PublicKeyBase64),
		Capabilities:        []string{"endpoint.approve", "trust.recover"},
		TrustRootGeneration: 1,
		CreatedAtUnixMS:     1784678400000,
		ExpiresAtUnixMS:     1816214400000,
	})
	if err != nil {
		t.Fatalf("EncodeIdentityCertificate operator: %v", err)
	}
	assertBase64(t, "operator certificate", operatorEncoded, vectors.Identities.Operator.EncodedBase64)
	if err := VerifyP256Signature(rootKey, operatorEncoded, decodeBase64(t, vectors.Identities.Operator.SignatureBase64)); err != nil {
		t.Fatalf("verify operator certificate: %v", err)
	}

	endpointEncoded, err := EncodeIdentityCertificate(IdentityCertificateClaims{
		CertificateVersion:  "desktop.v1",
		HomeID:              "home_fixture",
		IdentityID:          "did_endpoint_fixture",
		IdentityType:        "endpoint",
		AgentID:             "agent_fixture",
		PublicKeySPKI:       decodeBase64(t, vectors.Identities.Endpoint.PublicKeyBase64),
		Capabilities:        []string{"desktop.view", "desktop.control"},
		TrustRootGeneration: 1,
		CreatedAtUnixMS:     1784678400000,
		ExpiresAtUnixMS:     1816214400000,
	})
	if err != nil {
		t.Fatalf("EncodeIdentityCertificate endpoint: %v", err)
	}
	assertBase64(t, "endpoint certificate", endpointEncoded, vectors.Identities.Endpoint.EncodedBase64)
	if err := VerifyP256Signature(operatorKey, endpointEncoded, decodeBase64(t, vectors.Identities.Endpoint.SignatureBase64)); err != nil {
		t.Fatalf("verify endpoint certificate: %v", err)
	}

	if got, want := FingerprintSPKI(decodeBase64(t, vectors.Identities.RootPublicKeyBase64)), "K5hbrfG6yCk_a5LowKzBE5vFpey7-Xpicdzw1PbGivY"; got != want {
		t.Fatalf("root fingerprint = %s, want %s", got, want)
	}
	badSignature := append([]byte(nil), decodeBase64(t, vectors.Identities.Operator.SignatureBase64)...)
	badSignature[len(badSignature)-1] ^= 1
	if err := VerifyP256Signature(rootKey, operatorEncoded, badSignature); err == nil {
		t.Fatal("bad operator signature accepted")
	}
}

func TestDecodeIdentityCertificateRoundTripsGoldenVector(t *testing.T) {
	vectors := loadVectors(t)
	encoded := decodeBase64(t, vectors.Identities.Operator.EncodedBase64)
	claims, err := DecodeIdentityCertificate(encoded)
	if err != nil {
		t.Fatalf("DecodeIdentityCertificate: %v", err)
	}
	if claims.HomeID != "home_fixture" || claims.IdentityID != "did_operator_fixture" ||
		claims.UserID != "usr_fixture" || claims.DeviceID != "device_fixture" ||
		claims.TrustRootGeneration != 1 || len(claims.Capabilities) != 2 {
		t.Fatalf("unexpected claims: %#v", claims)
	}
	if got, err := EncodeIdentityCertificate(claims); err != nil || string(got) != string(encoded) {
		t.Fatalf("round trip mismatch: err=%v", err)
	}
	if _, err := DecodeIdentityCertificate(append(encoded, 0)); err == nil {
		t.Fatal("trailing certificate data accepted")
	}
}

func TestRecoveryEnrollmentAndRootRotationMatchGoldenVectors(t *testing.T) {
	vectors := loadVectors(t)
	rootKey := parseP256PublicKey(t, decodeBase64(t, vectors.Identities.RootPublicKeyBase64))

	recoveryEncoded, err := EncodeRecoveryEnrollmentProof(RecoveryEnrollmentClaims{
		Label:                    "recover_operator_device",
		HomeID:                   "home_fixture",
		TrustRootGeneration:      1,
		NewOperatorIdentityID:    "did_operator_recovered",
		NewOperatorDeviceID:      "recovered_device",
		NewOperatorPublicKeySPKI: decodeBase64(t, vectors.RecoveryEnroll.NewOperatorKeyBase64),
		IssuedAtUnixMS:           1784678400000,
		Challenge:                decodeBase64(t, vectors.RecoveryEnroll.ChallengeBase64),
	})
	if err != nil {
		t.Fatalf("EncodeRecoveryEnrollmentProof: %v", err)
	}
	assertBase64(t, "recovery enrollment", recoveryEncoded, vectors.RecoveryEnroll.EncodedBase64)
	if err := VerifyP256Signature(rootKey, recoveryEncoded, decodeBase64(t, vectors.RecoveryEnroll.RootSignatureBase64)); err != nil {
		t.Fatalf("verify recovery enrollment: %v", err)
	}

	rotationEncoded, err := EncodeRootRotationProof(RootRotationClaims{
		HomeID:                        "home_fixture",
		OldGeneration:                 1,
		NewGeneration:                 2,
		NewRootPublicKeySPKI:          decodeBase64(t, vectors.RootRotation.NewRootKeyBase64),
		NewRecoveryEnvelopeHash:       decodeBase64(t, vectors.RootRotation.NewEnvelopeHashBase64),
		ReplacementOperatorIdentityID: "did_operator_rotated",
		IssuedAtUnixMS:                1784678400000,
	})
	if err != nil {
		t.Fatalf("EncodeRootRotationProof: %v", err)
	}
	assertBase64(t, "root rotation", rotationEncoded, vectors.RootRotation.EncodedBase64)
	if err := VerifyP256Signature(rootKey, rotationEncoded, decodeBase64(t, vectors.RootRotation.OldRootSignatureBase64)); err != nil {
		t.Fatalf("verify root rotation: %v", err)
	}
}

func TestIdentityCertificateRequiresTypeSpecificScope(t *testing.T) {
	base := IdentityCertificateClaims{
		CertificateVersion:  "desktop.v1",
		HomeID:              "home_fixture",
		IdentityID:          "did_fixture",
		PublicKeySPKI:       []byte("public-key"),
		TrustRootGeneration: 1,
		CreatedAtUnixMS:     1784678400000,
		ExpiresAtUnixMS:     1816214400000,
	}
	for _, claims := range []IdentityCertificateClaims{
		func() IdentityCertificateClaims { value := base; value.IdentityType = "unknown"; return value }(),
		func() IdentityCertificateClaims { value := base; value.IdentityType = "operator_device"; return value }(),
		func() IdentityCertificateClaims { value := base; value.IdentityType = "endpoint"; return value }(),
	} {
		if _, err := EncodeIdentityCertificate(claims); err == nil {
			t.Fatalf("identity without valid type-specific scope accepted: %#v", claims)
		}
	}
}

func TestEncodingRejectsInvalidInputs(t *testing.T) {
	if _, err := EncodeRecoveryContext("", 1); err == nil {
		t.Fatal("empty recovery home accepted")
	}
	if _, err := DeriveRecoveryKey(make([]byte, 31), []byte("context")); err == nil {
		t.Fatal("short recovery secret accepted")
	}
	if _, err := EncodeRootRotationProof(RootRotationClaims{HomeID: "home_fixture", OldGeneration: 1, NewGeneration: 3}); err == nil {
		t.Fatal("non-sequential root rotation accepted")
	}
	if _, err := (RecordHeader{Version: 2, Direction: DirectionBrowserToAgent, KeyEpoch: 1}).MarshalBinary(); err == nil {
		t.Fatal("unknown record version accepted")
	}
	if _, err := DecodeRawBase64URL(""); err == nil {
		t.Fatal("empty base64url accepted")
	}
	if _, err := DecodeRawBase64URL("YQ=="); err == nil {
		t.Fatal("padded base64url accepted")
	}
}

func loadVectors(t *testing.T) testVectors {
	t.Helper()
	path := filepath.Join("..", "..", "schemas", "desktop", "v1", "test-vectors.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var vectors testVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if vectors.Version != "desktop.v1" || len(vectors.InvalidRecords) == 0 || len(vectors.InvalidTrust) == 0 {
		t.Fatalf("incomplete fixture: %#v", vectors)
	}
	return vectors
}

func decodeBase64(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode base64url: %v", err)
	}
	return decoded
}

func assertBase64(t *testing.T, label string, got []byte, want string) {
	t.Helper()
	if encoded := base64.RawURLEncoding.EncodeToString(got); encoded != want {
		t.Fatalf("%s = %s, want %s", label, encoded, want)
	}
}

func mustAEAD(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}
	return aead
}

func parseP256PublicKey(t *testing.T, spki []byte) *ecdsa.PublicKey {
	t.Helper()
	parsed, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		t.Fatalf("ParsePKIXPublicKey: %v", err)
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("SPKI contains %T, want *ecdsa.PublicKey", parsed)
	}
	return publicKey
}
