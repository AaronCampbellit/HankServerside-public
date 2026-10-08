package store

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/desktopcrypto"
	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) CreateDesktopEnrollmentChallenge(ctx context.Context, value domain.DesktopEnrollmentChallenge) error {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.HomeID) == "" || strings.TrimSpace(value.UserID) == "" ||
		strings.TrimSpace(value.SessionID) == "" || (value.Purpose != "browser_operator" && value.Purpose != "mac_agent") ||
		strings.TrimSpace(value.InstallationID) == "" || len(value.ChallengeHash) != 32 || value.CreatedAt.IsZero() || !value.ExpiresAt.After(value.CreatedAt) {
		return errors.New("invalid desktop enrollment challenge")
	}
	_, err := s.exec(ctx, `INSERT INTO desktop_enrollment_challenges
		(id, home_id, user_id, session_id, purpose, installation_id, challenge_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.HomeID, value.UserID, value.SessionID, value.Purpose, value.InstallationID, value.ChallengeHash, value.ExpiresAt, value.CreatedAt)
	return mapDesktopStoreError(err)
}

func (s *Store) ConsumeDesktopEnrollmentChallenge(ctx context.Context, id, homeID, userID, sessionID, purpose, installationID string, challengeHash []byte, now time.Time) error {
	if strings.TrimSpace(id) == "" || len(challengeHash) != 32 {
		return errors.New("invalid desktop enrollment challenge")
	}
	result, err := s.exec(ctx, `UPDATE desktop_enrollment_challenges SET consumed_at = ?
		WHERE id = ? AND home_id = ? AND user_id = ? AND session_id = ? AND purpose = ? AND installation_id = ?
		AND challenge_hash = ? AND consumed_at IS NULL AND expires_at > ?`, now, id, homeID, userID, sessionID, purpose, installationID, challengeHash, now)
	if err != nil {
		return mapDesktopStoreError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// EnsureServerManagedDesktopTrust creates the per-home server signing key or
// atomically replaces legacy client-managed trust. The private key is encrypted
// with the configured Store secret box before it is persisted.
func (s *Store) EnsureServerManagedDesktopTrust(ctx context.Context, homeID string, now time.Time) (domain.DesktopTrustRoot, *ecdsa.PrivateKey, bool, error) {
	if strings.TrimSpace(homeID) == "" {
		return domain.DesktopTrustRoot{}, nil, false, errors.New("desktop home is required")
	}
	if existing, err := s.GetDesktopTrustRoot(ctx, homeID); err == nil && existing.AuthorityMode == "server_managed" {
		encoded, err := s.decryptSecret(existing.EncryptedPrivateKey)
		if err != nil {
			return domain.DesktopTrustRoot{}, nil, false, err
		}
		key, err := x509.ParseECPrivateKey([]byte(encoded))
		if err != nil || key.Curve != elliptic.P256() {
			return domain.DesktopTrustRoot{}, nil, false, errors.New("stored desktop authority key is invalid")
		}
		return existing, key, false, nil
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return domain.DesktopTrustRoot{}, nil, false, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return domain.DesktopTrustRoot{}, nil, false, err
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return domain.DesktopTrustRoot{}, nil, false, err
	}
	privateDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return domain.DesktopTrustRoot{}, nil, false, err
	}
	encrypted, err := s.encryptSecret(string(privateDER))
	if err != nil || encrypted == string(privateDER) {
		return domain.DesktopTrustRoot{}, nil, false, errors.New("desktop authority encryption is unavailable")
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.DesktopTrustRoot{}, nil, false, err
	}
	defer tx.Rollback()
	var generation int
	var mode string
	err = tx.QueryRowContext(ctx, `SELECT generation, authority_mode FROM desktop_trust_roots WHERE home_id = ? FOR UPDATE`, homeID).Scan(&generation, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		generation = 1
		_, err = tx.ExecContext(ctx, `INSERT INTO desktop_trust_roots
			(home_id, generation, algorithm, public_key_spki, fingerprint, recovery_envelope, created_at, authority_mode, encrypted_private_key)
			VALUES (?, ?, ?, ?, ?, NULL, ?, 'server_managed', ?)`, homeID, generation, domain.DesktopTrustAlgorithm, spki, desktopcrypto.FingerprintSPKI(spki), now, encrypted)
	} else if err == nil && mode == "server_managed" {
		// Another browser or Mac may have completed the migration after the
		// optimistic read above. Do not rotate its new authority again.
		if err := tx.Commit(); err != nil {
			return domain.DesktopTrustRoot{}, nil, false, mapDesktopStoreError(err)
		}
		root, err := s.GetDesktopTrustRoot(ctx, homeID)
		if err != nil {
			return domain.DesktopTrustRoot{}, nil, false, err
		}
		encoded, err := s.decryptSecret(root.EncryptedPrivateKey)
		if err != nil {
			return domain.DesktopTrustRoot{}, nil, false, err
		}
		storedKey, err := x509.ParseECPrivateKey([]byte(encoded))
		if err != nil || storedKey.Curve != elliptic.P256() {
			return domain.DesktopTrustRoot{}, nil, false, errors.New("stored desktop authority key is invalid")
		}
		return root, storedKey, false, nil
	} else if err == nil {
		generation++
		// Switching authority deliberately terminates only Desktop sessions and
		// revokes their identities; ordinary Hank agent connectivity is untouched.
		if _, err = tx.ExecContext(ctx, `UPDATE desktop_join_credentials SET revoked_at = ? WHERE revoked_at IS NULL AND session_id IN
			(SELECT id FROM desktop_sessions WHERE home_id = ? AND state IN ('requested','offered','agent_ready','joining','active','reconnecting'))`, now, homeID); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE desktop_sessions SET state = 'terminated', terminated_at = ?, termination_reason = 'trust_authority_migrated'
				WHERE home_id = ? AND state IN ('requested','offered','agent_ready','joining','active','reconnecting')`, now, homeID)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE desktop_identities SET revoked_at = ?, revocation_reason = 'trust_authority_migrated'
				WHERE home_id = ? AND revoked_at IS NULL`, now, homeID)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE desktop_trust_roots SET generation = ?, algorithm = ?, public_key_spki = ?, fingerprint = ?,
				recovery_envelope = NULL, recovery_challenge_hash = NULL, recovery_challenge_expires_at = NULL, recovery_challenge_consumed_at = NULL,
				rotated_at = ?, authority_mode = 'server_managed', encrypted_private_key = ? WHERE home_id = ?`, generation, domain.DesktopTrustAlgorithm, spki, desktopcrypto.FingerprintSPKI(spki), now, encrypted, homeID)
		}
	}
	if err != nil {
		return domain.DesktopTrustRoot{}, nil, false, mapDesktopStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.DesktopTrustRoot{}, nil, false, mapDesktopStoreError(err)
	}
	return domain.DesktopTrustRoot{HomeID: homeID, Generation: generation, Algorithm: domain.DesktopTrustAlgorithm, PublicKeySPKI: spki, Fingerprint: desktopcrypto.FingerprintSPKI(spki), CreatedAt: now, AuthorityMode: "server_managed", EncryptedPrivateKey: encrypted}, key, true, nil
}
