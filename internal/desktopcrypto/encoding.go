package desktopcrypto

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	HandshakeLabel = "Hank Desktop Handshake v1"
	RecordVersion  = byte(1)

	DirectionBrowserToAgent = byte(1)
	DirectionAgentToBrowser = byte(2)
)

type HandshakeTranscript struct {
	HomeID                    string
	SessionID                 string
	AgentID                   string
	OperatorUserID            string
	OperatorDeviceID          string
	Permissions               []string
	BrowserEphemeralPublicKey []byte
	JoinExpiresAtUnixMS       int64
	HardExpiresAtUnixMS       int64
	KeyEpoch                  uint32
}

type IdentityCertificateClaims struct {
	CertificateVersion  string
	HomeID              string
	IdentityID          string
	IdentityType        string
	UserID              string
	DeviceID            string
	AgentID             string
	PublicKeySPKI       []byte
	Capabilities        []string
	TrustRootGeneration uint32
	CreatedAtUnixMS     int64
	ExpiresAtUnixMS     int64
}

type RecoveryEnrollmentClaims struct {
	Label                    string
	HomeID                   string
	TrustRootGeneration      uint32
	NewOperatorIdentityID    string
	NewOperatorDeviceID      string
	NewOperatorPublicKeySPKI []byte
	IssuedAtUnixMS           int64
	Challenge                []byte
}

type RootRotationClaims struct {
	HomeID                        string
	OldGeneration                 uint32
	NewGeneration                 uint32
	NewRootPublicKeySPKI          []byte
	NewRecoveryEnvelopeHash       []byte
	ReplacementOperatorIdentityID string
	IssuedAtUnixMS                int64
}

type DirectionalKeys struct {
	BrowserToAgent     [32]byte
	AgentToBrowser     [32]byte
	BrowserNoncePrefix [4]byte
	AgentNoncePrefix   [4]byte
}

type RecordHeader struct {
	Version          byte
	Direction        byte
	KeyEpoch         uint32
	Sequence         uint64
	CiphertextLength uint32
}

func EncodeHandshakeTranscript(value HandshakeTranscript) ([]byte, error) {
	if value.HomeID == "" || value.SessionID == "" || value.AgentID == "" ||
		value.OperatorUserID == "" || value.OperatorDeviceID == "" ||
		len(value.Permissions) == 0 || len(value.BrowserEphemeralPublicKey) == 0 ||
		value.JoinExpiresAtUnixMS <= 0 || value.HardExpiresAtUnixMS <= value.JoinExpiresAtUnixMS ||
		value.KeyEpoch == 0 {
		return nil, errors.New("invalid desktop handshake transcript")
	}

	var out bytes.Buffer
	fields := [][]byte{
		[]byte(HandshakeLabel),
		[]byte(value.HomeID),
		[]byte(value.SessionID),
		[]byte(value.AgentID),
		[]byte(value.OperatorUserID),
		[]byte(value.OperatorDeviceID),
	}
	for _, field := range fields {
		if err := writeField(&out, field); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&out, binary.BigEndian, uint32(len(value.Permissions))); err != nil {
		return nil, err
	}
	for _, permission := range value.Permissions {
		if permission == "" {
			return nil, errors.New("empty desktop permission")
		}
		if err := writeField(&out, []byte(permission)); err != nil {
			return nil, err
		}
	}
	if err := writeField(&out, value.BrowserEphemeralPublicKey); err != nil {
		return nil, err
	}
	for _, number := range []any{value.JoinExpiresAtUnixMS, value.HardExpiresAtUnixMS, value.KeyEpoch} {
		if err := binary.Write(&out, binary.BigEndian, number); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func EncodeIdentityCertificate(value IdentityCertificateClaims) ([]byte, error) {
	if value.CertificateVersion != "desktop.v1" || value.HomeID == "" || value.IdentityID == "" ||
		value.IdentityType == "" || len(value.PublicKeySPKI) == 0 || value.TrustRootGeneration == 0 ||
		value.CreatedAtUnixMS <= 0 || value.ExpiresAtUnixMS <= value.CreatedAtUnixMS {
		return nil, errors.New("invalid desktop identity certificate")
	}
	switch value.IdentityType {
	case "operator_device":
		if value.UserID == "" || value.DeviceID == "" || value.AgentID != "" {
			return nil, errors.New("operator-device identity scope is invalid")
		}
	case "endpoint":
		if value.AgentID == "" || value.UserID != "" || value.DeviceID != "" {
			return nil, errors.New("endpoint identity scope is invalid")
		}
	default:
		return nil, errors.New("unsupported desktop identity type")
	}
	if len(value.Capabilities) == 0 {
		return nil, errors.New("desktop identity capabilities are required")
	}

	var out bytes.Buffer
	for _, field := range [][]byte{
		[]byte("Hank Desktop Identity Certificate v1"),
		[]byte(value.CertificateVersion),
		[]byte(value.HomeID),
		[]byte(value.IdentityID),
		[]byte(value.IdentityType),
		[]byte(value.UserID),
		[]byte(value.DeviceID),
		[]byte(value.AgentID),
		value.PublicKeySPKI,
	} {
		if err := writeField(&out, field); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&out, binary.BigEndian, uint32(len(value.Capabilities))); err != nil {
		return nil, err
	}
	for _, capability := range value.Capabilities {
		if capability == "" {
			return nil, errors.New("empty desktop identity capability")
		}
		if err := writeField(&out, []byte(capability)); err != nil {
			return nil, err
		}
	}
	for _, number := range []any{value.TrustRootGeneration, value.CreatedAtUnixMS, value.ExpiresAtUnixMS} {
		if err := binary.Write(&out, binary.BigEndian, number); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func DecodeIdentityCertificate(encoded []byte) (IdentityCertificateClaims, error) {
	reader := bytes.NewReader(encoded)
	readString := func() (string, error) {
		value, err := readField(reader)
		return string(value), err
	}
	label, err := readString()
	if err != nil || label != "Hank Desktop Identity Certificate v1" {
		return IdentityCertificateClaims{}, errors.New("invalid desktop identity certificate label")
	}
	var claims IdentityCertificateClaims
	if claims.CertificateVersion, err = readString(); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if claims.HomeID, err = readString(); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if claims.IdentityID, err = readString(); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if claims.IdentityType, err = readString(); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if claims.UserID, err = readString(); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if claims.DeviceID, err = readString(); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if claims.AgentID, err = readString(); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if claims.PublicKeySPKI, err = readField(reader); err != nil {
		return IdentityCertificateClaims{}, err
	}
	var capabilityCount uint32
	if err := binary.Read(reader, binary.BigEndian, &capabilityCount); err != nil || capabilityCount == 0 || capabilityCount > 32 {
		return IdentityCertificateClaims{}, errors.New("invalid desktop identity capability count")
	}
	claims.Capabilities = make([]string, capabilityCount)
	for index := range claims.Capabilities {
		if claims.Capabilities[index], err = readString(); err != nil {
			return IdentityCertificateClaims{}, err
		}
	}
	if err := binary.Read(reader, binary.BigEndian, &claims.TrustRootGeneration); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if err := binary.Read(reader, binary.BigEndian, &claims.CreatedAtUnixMS); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if err := binary.Read(reader, binary.BigEndian, &claims.ExpiresAtUnixMS); err != nil {
		return IdentityCertificateClaims{}, err
	}
	if reader.Len() != 0 {
		return IdentityCertificateClaims{}, errors.New("trailing desktop identity certificate data")
	}
	if _, err := EncodeIdentityCertificate(claims); err != nil {
		return IdentityCertificateClaims{}, err
	}
	return claims, nil
}

func EncodeRecoveryEnrollmentProof(value RecoveryEnrollmentClaims) ([]byte, error) {
	if value.Label == "" || value.HomeID == "" || value.TrustRootGeneration == 0 ||
		value.NewOperatorIdentityID == "" || value.NewOperatorDeviceID == "" ||
		len(value.NewOperatorPublicKeySPKI) == 0 || value.IssuedAtUnixMS <= 0 || len(value.Challenge) == 0 {
		return nil, errors.New("invalid recovery enrollment proof")
	}

	var out bytes.Buffer
	for _, field := range [][]byte{
		[]byte("Hank Desktop Recovery Enrollment v1"),
		[]byte(value.Label),
		[]byte(value.HomeID),
	} {
		if err := writeField(&out, field); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&out, binary.BigEndian, value.TrustRootGeneration); err != nil {
		return nil, err
	}
	for _, field := range [][]byte{
		[]byte(value.NewOperatorIdentityID),
		[]byte(value.NewOperatorDeviceID),
		value.NewOperatorPublicKeySPKI,
	} {
		if err := writeField(&out, field); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&out, binary.BigEndian, value.IssuedAtUnixMS); err != nil {
		return nil, err
	}
	if err := writeField(&out, value.Challenge); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func EncodeRecoveryContext(homeID string, generation uint32) ([]byte, error) {
	if homeID == "" || generation == 0 {
		return nil, errors.New("invalid recovery context")
	}
	var out bytes.Buffer
	for _, field := range [][]byte{[]byte("Hank Desktop Root Recovery v1"), []byte(homeID)} {
		if err := writeField(&out, field); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&out, binary.BigEndian, generation); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func EncodeRootRotationProof(value RootRotationClaims) ([]byte, error) {
	if value.HomeID == "" || value.OldGeneration == 0 || value.NewGeneration != value.OldGeneration+1 ||
		len(value.NewRootPublicKeySPKI) == 0 || len(value.NewRecoveryEnvelopeHash) != sha256.Size ||
		value.ReplacementOperatorIdentityID == "" || value.IssuedAtUnixMS <= 0 {
		return nil, errors.New("invalid root rotation scope")
	}

	var out bytes.Buffer
	for _, field := range [][]byte{[]byte("Hank Desktop Root Rotation v1"), []byte(value.HomeID)} {
		if err := writeField(&out, field); err != nil {
			return nil, err
		}
	}
	for _, generation := range []uint32{value.OldGeneration, value.NewGeneration} {
		if err := binary.Write(&out, binary.BigEndian, generation); err != nil {
			return nil, err
		}
	}
	for _, field := range [][]byte{
		value.NewRootPublicKeySPKI,
		value.NewRecoveryEnvelopeHash,
		[]byte(value.ReplacementOperatorIdentityID),
	} {
		if err := writeField(&out, field); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&out, binary.BigEndian, value.IssuedAtUnixMS); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func DeriveRecoveryKey(secret, context []byte) ([32]byte, error) {
	var key [32]byte
	if len(secret) != 32 {
		return key, errors.New("recovery secret must be 32 bytes")
	}
	if len(context) == 0 {
		return key, errors.New("recovery context is required")
	}
	salt := sha256.Sum256(context)
	_, err := io.ReadFull(
		hkdf.New(sha256.New, secret, salt[:], []byte("hank-desktop-v1/root-recovery/key")),
		key[:],
	)
	return key, err
}

func VerifyP256Signature(publicKey *ecdsa.PublicKey, encoded, signature []byte) error {
	if publicKey == nil || publicKey.Curve != elliptic.P256() {
		return errors.New("P-256 public key required")
	}
	if len(encoded) == 0 || len(signature) == 0 {
		return errors.New("encoded value and signature are required")
	}
	digest := sha256.Sum256(encoded)
	if !ecdsa.VerifyASN1(publicKey, digest[:], signature) {
		return errors.New("invalid ECDSA signature")
	}
	return nil
}

func FingerprintSPKI(spki []byte) string {
	sum := sha256.Sum256(spki)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func DeriveDirectionalKeys(sharedSecret, transcriptDigest []byte) (DirectionalKeys, error) {
	var keys DirectionalKeys
	if len(sharedSecret) == 0 || len(transcriptDigest) != sha256.Size {
		return keys, errors.New("shared secret and SHA-256 transcript digest are required")
	}
	for label, target := range map[string][]byte{
		"hank-desktop-v1/browser-to-agent/key":   keys.BrowserToAgent[:],
		"hank-desktop-v1/agent-to-browser/key":   keys.AgentToBrowser[:],
		"hank-desktop-v1/browser-to-agent/nonce": keys.BrowserNoncePrefix[:],
		"hank-desktop-v1/agent-to-browser/nonce": keys.AgentNoncePrefix[:],
	} {
		if _, err := io.ReadFull(hkdf.New(sha256.New, sharedSecret, transcriptDigest, []byte(label)), target); err != nil {
			return DirectionalKeys{}, err
		}
	}
	return keys, nil
}

func Nonce(prefix [4]byte, sequence uint64) [12]byte {
	var nonce [12]byte
	copy(nonce[:4], prefix[:])
	binary.BigEndian.PutUint64(nonce[4:], sequence)
	return nonce
}

func (header RecordHeader) MarshalBinary() ([]byte, error) {
	if header.Version != RecordVersion ||
		(header.Direction != DirectionBrowserToAgent && header.Direction != DirectionAgentToBrowser) ||
		header.KeyEpoch == 0 {
		return nil, errors.New("invalid desktop record header")
	}
	result := make([]byte, 18)
	result[0] = header.Version
	result[1] = header.Direction
	binary.BigEndian.PutUint32(result[2:6], header.KeyEpoch)
	binary.BigEndian.PutUint64(result[6:14], header.Sequence)
	binary.BigEndian.PutUint32(result[14:18], header.CiphertextLength)
	return result, nil
}

func DecodeRawBase64URL(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("empty base64url value")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode unpadded base64url: %w", err)
	}
	return decoded, nil
}

func writeField(dst *bytes.Buffer, field []byte) error {
	if len(field) > 1<<20 {
		return errors.New("desktop transcript field exceeds 1 MiB")
	}
	if err := binary.Write(dst, binary.BigEndian, uint32(len(field))); err != nil {
		return err
	}
	_, err := dst.Write(field)
	return err
}

func readField(src *bytes.Reader) ([]byte, error) {
	var length uint32
	if err := binary.Read(src, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length > 1<<20 || uint64(length) > uint64(src.Len()) {
		return nil, errors.New("invalid desktop transcript field length")
	}
	value := make([]byte, length)
	if _, err := io.ReadFull(src, value); err != nil {
		return nil, err
	}
	return value, nil
}
