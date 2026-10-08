package cloud

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/desktopcrypto"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const desktopEnrollmentChallengeTTL = 5 * time.Minute

type desktopAutoChallengeRequest struct {
	InstallationID string `json:"installation_id"`
}

type desktopAutoEnrollmentProof struct {
	ChallengeID    string `json:"challenge_id"`
	Challenge      string `json:"challenge"`
	InstallationID string `json:"installation_id"`
	PublicKeySPKI  string `json:"public_key_spki"`
	Signature      string `json:"signature"`
}

type desktopBrowserAutoEnrollmentRequest struct {
	desktopAutoEnrollmentProof
	DeviceID string `json:"device_id"`
}

type desktopMacAutoEnrollmentRequest struct {
	desktopAutoEnrollmentProof
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
}

func (s *Server) handleDesktopAutoChallenge(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, purpose string) {
	var body desktopAutoChallengeRequest
	if err := parseJSON(w, r, &body); err != nil || !validDesktopInstallationID(body.InstallationID) {
		http.Error(w, "invalid device enrollment request", http.StatusBadRequest)
		return
	}
	if purpose == "mac_agent" && home.AgentEnrollmentPolicy != "all_users" {
		membership, err := s.store.GetHomeMembership(r.Context(), home.ID, auth.User.ID)
		if err != nil || membership.Role != domain.HomeRoleAdmin {
			http.Error(w, "agent enrollment requires a home administrator", http.StatusForbidden)
			return
		}
	}
	now := time.Now().UTC()
	raw := newToken()
	hash := sha256.Sum256([]byte(raw))
	challenge := domain.DesktopEnrollmentChallenge{ID: newID("denroll"), HomeID: home.ID, UserID: auth.User.ID, SessionID: auth.Session.ID,
		Purpose: purpose, InstallationID: strings.TrimSpace(body.InstallationID), ChallengeHash: hash[:], CreatedAt: now, ExpiresAt: now.Add(desktopEnrollmentChallengeTTL)}
	if err := s.store.CreateDesktopEnrollmentChallenge(r.Context(), challenge); err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"challenge_id": challenge.ID,
		"challenge":    raw,
		"session_id":   auth.Session.ID,
		"home_id":      home.ID,
		"user_id":      auth.User.ID,
		"expires_at":   challenge.ExpiresAt,
	})
}

func (s *Server) validateDesktopAutoEnrollment(r *http.Request, home domain.Home, auth authContext, purpose string, proof desktopAutoEnrollmentProof) ([]byte, error) {
	if !validDesktopInstallationID(proof.InstallationID) || strings.TrimSpace(proof.ChallengeID) == "" || strings.TrimSpace(proof.Challenge) == "" {
		return nil, errors.New("invalid device enrollment scope")
	}
	key, spki, err := decodeDesktopP256SPKI(proof.PublicKeySPKI)
	if err != nil {
		return nil, err
	}
	signature, err := desktopcrypto.DecodeRawBase64URL(proof.Signature)
	if err != nil {
		return nil, err
	}
	encoded := desktopAutoEnrollmentTranscript(home.ID, auth.User.ID, auth.Session.ID, purpose, proof.ChallengeID, proof.Challenge, proof.InstallationID, spki)
	if purpose == "mac_agent" {
		encoded = desktopMacAutoEnrollmentTranscript(proof.ChallengeID, proof.Challenge, proof.InstallationID, spki)
	}
	if desktopcrypto.VerifyP256Signature(key, encoded, signature) != nil {
		return nil, errors.New("device enrollment signature is invalid")
	}
	hash := sha256.Sum256([]byte(proof.Challenge))
	if err := s.store.ConsumeDesktopEnrollmentChallenge(r.Context(), proof.ChallengeID, home.ID, auth.User.ID, auth.Session.ID, purpose, proof.InstallationID, hash[:], time.Now().UTC()); err != nil {
		return nil, err
	}
	return spki, nil
}

func desktopAutoEnrollmentTranscript(homeID, userID, sessionID, purpose, challengeID, challenge, installationID string, spki []byte) []byte {
	values := []string{"Hank Desktop Device Enrollment v1", homeID, userID, sessionID, purpose, challengeID, challenge, installationID, base64.RawURLEncoding.EncodeToString(spki)}
	return []byte(strings.Join(values, "\n"))
}

// Mac endpoint enrollment signs only its opaque one-time challenge context.
// The server still binds that challenge to the authenticated home, user, and
// session when it is created and consumed. This avoids requiring the native
// app to reconstruct account scope from independently refreshed UI state.
func desktopMacAutoEnrollmentTranscript(challengeID, challenge, installationID string, spki []byte) []byte {
	values := []string{"Hank Mac Desktop Device Enrollment v1", challengeID, challenge, installationID, base64.RawURLEncoding.EncodeToString(spki)}
	return []byte(strings.Join(values, "\n"))
}

func validDesktopInstallationID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 16 || len(value) > 160 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func (s *Server) ensureServerDesktopAuthority(r *http.Request, home domain.Home, auth authContext) (domain.DesktopTrustRoot, *ecdsa.PrivateKey, bool, error) {
	root, key, migrated, err := s.store.EnsureServerManagedDesktopTrust(r.Context(), home.ID, time.Now().UTC())
	if err == nil && migrated {
		s.audit(r.Context(), "desktop.trust.server_managed", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "desktop_trust", home.ID, map[string]any{"generation": root.Generation})
	}
	return root, key, migrated, err
}

func signServerDesktopIdentity(key *ecdsa.PrivateKey, root domain.DesktopTrustRoot, identity domain.DesktopIdentity) (domain.DesktopIdentity, error) {
	claims, err := desktopcrypto.EncodeIdentityCertificate(desktopcrypto.IdentityCertificateClaims{
		CertificateVersion: "desktop.v1", HomeID: identity.HomeID, IdentityID: identity.ID, IdentityType: identity.IdentityType,
		UserID: identity.UserID, DeviceID: identity.DeviceID, AgentID: identity.AgentID, PublicKeySPKI: identity.PublicKeySPKI,
		Capabilities: identity.Capabilities, TrustRootGeneration: uint32(root.Generation), CreatedAtUnixMS: identity.CreatedAt.UnixMilli(), ExpiresAtUnixMS: identity.ExpiresAt.UnixMilli(),
	})
	if err != nil {
		return domain.DesktopIdentity{}, err
	}
	digest := sha256.Sum256(claims)
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return domain.DesktopIdentity{}, err
	}
	certificate, err := json.Marshal(desktopSignedCertificateEnvelope{Claims: base64.RawURLEncoding.EncodeToString(claims), Signature: base64.RawURLEncoding.EncodeToString(signature)})
	if err != nil {
		return domain.DesktopIdentity{}, err
	}
	identity.Certificate = certificate
	identity.Fingerprint = desktopcrypto.FingerprintSPKI(identity.PublicKeySPKI)
	identity.TrustRootGeneration = root.Generation
	return identity, nil
}

func (s *Server) installServerDesktopIdentity(r *http.Request, home domain.Home, auth authContext, root domain.DesktopTrustRoot, key *ecdsa.PrivateKey, identity domain.DesktopIdentity, event string) (domain.DesktopIdentity, error) {
	identity.Fingerprint = desktopcrypto.FingerprintSPKI(identity.PublicKeySPKI)
	identity.TrustRootGeneration = root.Generation
	var existing domain.DesktopIdentity
	var err error
	if identity.IdentityType == domain.DesktopIdentityOperatorDevice {
		existing, err = s.store.GetCurrentDesktopOperatorIdentity(r.Context(), home.ID, identity.DeviceID)
	} else {
		existing, err = s.store.GetCurrentDesktopEndpointIdentity(r.Context(), home.ID, identity.AgentID)
	}
	if err == nil {
		if sameDesktopIdentityEnrollment(existing, identity) {
			return existing, nil
		}
		sameKey := existing.Fingerprint == identity.Fingerprint && bytes.Equal(existing.PublicKeySPKI, identity.PublicKeySPKI)
		if sameKey {
			identity.ID = existing.ID
		}
		signed, signErr := signServerDesktopIdentity(key, root, identity)
		if signErr != nil {
			return domain.DesktopIdentity{}, signErr
		}
		var sessions []string
		var replaceErr error
		if sameKey {
			sessions, replaceErr = s.store.RefreshDesktopIdentity(r.Context(), existing.ID, signed, time.Now().UTC(), "identity_refreshed")
		} else {
			sessions, replaceErr = s.store.ReplaceDesktopIdentity(r.Context(), existing.ID, signed, time.Now().UTC(), "identity_refreshed")
		}
		if replaceErr != nil {
			return domain.DesktopIdentity{}, replaceErr
		}
		s.revokeDesktopRelays(sessions, "identity_refreshed")
		s.auditDesktopIdentity(r, auth, home.ID, event, signed, "server_managed")
		return signed, nil
	} else if errors.Is(err, store.ErrNotFound) {
		signed, signErr := signServerDesktopIdentity(key, root, identity)
		if signErr != nil {
			return domain.DesktopIdentity{}, signErr
		}
		if err := s.store.CreateDesktopIdentity(r.Context(), signed); err != nil {
			// Browser bootstrap and the viewer can both ask for enrollment while
			// the page is loading. A concurrent request with the same protected
			// key is already the identity we need; it is not a trust conflict.
			if errors.Is(err, store.ErrConflict) {
				if current, lookupErr := s.getActiveDesktopIdentity(r.Context(), home.ID, signed); lookupErr == nil && current.Fingerprint == signed.Fingerprint {
					return current, nil
				}
			}
			return domain.DesktopIdentity{}, err
		}
		s.auditDesktopIdentity(r, auth, home.ID, event, signed, "server_managed")
		return signed, nil
	} else {
		return domain.DesktopIdentity{}, err
	}
}

func sameDesktopIdentityEnrollment(existing, candidate domain.DesktopIdentity) bool {
	return existing.IdentityType == domain.DesktopIdentityOperatorDevice &&
		candidate.IdentityType == domain.DesktopIdentityOperatorDevice &&
		existing.HomeID == candidate.HomeID &&
		existing.IdentityType == candidate.IdentityType &&
		existing.UserID == candidate.UserID &&
		existing.DeviceID == candidate.DeviceID &&
		existing.AgentID == candidate.AgentID &&
		existing.AuthSessionID == candidate.AuthSessionID &&
		existing.TrustRootGeneration == candidate.TrustRootGeneration &&
		existing.Fingerprint == candidate.Fingerprint &&
		bytes.Equal(existing.PublicKeySPKI, candidate.PublicKeySPKI)
}

func (s *Server) getActiveDesktopIdentity(ctx context.Context, homeID string, identity domain.DesktopIdentity) (domain.DesktopIdentity, error) {
	if identity.IdentityType == domain.DesktopIdentityOperatorDevice {
		return s.store.GetActiveDesktopOperatorIdentity(ctx, homeID, identity.UserID, identity.DeviceID, time.Now().UTC())
	}
	return s.store.GetActiveDesktopEndpointIdentity(ctx, homeID, identity.AgentID, time.Now().UTC())
}

func (s *Server) handleDesktopBrowserAutoEnrollment(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext) {
	var body desktopBrowserAutoEnrollmentRequest
	if err := parseJSON(w, r, &body); err != nil || strings.TrimSpace(body.DeviceID) == "" || len(body.DeviceID) > 160 {
		http.Error(w, "invalid browser enrollment", http.StatusBadRequest)
		return
	}
	spki, err := s.validateDesktopAutoEnrollment(r, home, auth, "browser_operator", body.desktopAutoEnrollmentProof)
	if err != nil {
		http.Error(w, "browser enrollment rejected", http.StatusForbidden)
		return
	}
	root, key, _, err := s.ensureServerDesktopAuthority(r, home, auth)
	if err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	now := time.Now().UTC()
	identity, err := s.installServerDesktopIdentity(r, home, auth, root, key, domain.DesktopIdentity{ID: newID("dop"), HomeID: home.ID, IdentityType: domain.DesktopIdentityOperatorDevice,
		UserID: auth.User.ID, DeviceID: body.DeviceID, PublicKeySPKI: spki, Capabilities: append([]string(nil), desktopRequiredAdminCapabilities...), CreatedAt: now, ExpiresAt: auth.Session.ExpiresAt, AuthSessionID: auth.Session.ID}, "desktop.identity.auto_enrolled")
	if err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"root": map[string]any{"generation": root.Generation, "algorithm": root.Algorithm, "fingerprint": root.Fingerprint, "public_key_spki": base64.RawURLEncoding.EncodeToString(root.PublicKeySPKI)}, "identity": desktopIdentityPublicSnapshot(identity)})
}

func (s *Server) handleMacAutoEnrollment(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership) {
	if home.AgentEnrollmentPolicy != "all_users" && membership.Role != domain.HomeRoleAdmin {
		http.Error(w, "agent enrollment requires a home administrator", http.StatusForbidden)
		return
	}
	var body desktopMacAutoEnrollmentRequest
	if err := parseJSON(w, r, &body); err != nil || strings.TrimSpace(body.AgentID) == "" || len(body.AgentID) > 128 || len(strings.TrimSpace(body.Name)) > 256 {
		http.Error(w, "invalid Mac enrollment", http.StatusBadRequest)
		return
	}
	spki, err := s.validateDesktopAutoEnrollment(r, home, auth, "mac_agent", body.desktopAutoEnrollmentProof)
	if err != nil {
		http.Error(w, "Mac enrollment rejected", http.StatusForbidden)
		return
	}
	now := time.Now().UTC()
	agent, err := s.store.GetAgentByInstallationID(r.Context(), home.ID, body.InstallationID)
	if errors.Is(err, store.ErrNotFound) {
		agent = domain.Agent{ID: body.AgentID, HomeID: home.ID, Name: strings.TrimSpace(body.Name), Status: domain.AgentStatusOffline, AgentType: AgentTypeWorker, InstallationID: body.InstallationID, EnrolledByUserID: auth.User.ID, CreatedAt: now, UpdatedAt: now}
	} else if err != nil {
		writeDesktopStoreError(w, err)
		return
	} else {
		agent.Name = strings.TrimSpace(body.Name)
		if agent.Name == "" {
			agent.Name = agent.ID
		}
		agent.EnrolledByUserID = auth.User.ID
		agent.UpdatedAt = now
	}
	if agent.Name == "" {
		agent.Name = agent.ID
	}
	if err := s.store.UpsertAgent(r.Context(), agent); err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	root, key, _, err := s.ensureServerDesktopAuthority(r, home, auth)
	if err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	identity, err := s.installServerDesktopIdentity(r, home, auth, root, key, domain.DesktopIdentity{ID: newID("dep"), HomeID: home.ID, IdentityType: domain.DesktopIdentityEndpoint,
		AgentID: agent.ID, PublicKeySPKI: spki, Capabilities: []string{"desktop.view", "desktop.control", "desktop.clipboard.read", "desktop.clipboard.write", "desktop.elevate", "desktop.secure_desktop", "desktop.unattended"}, CreatedAt: now, ExpiresAt: now.Add(365 * 24 * time.Hour)}, "desktop.endpoint.auto_enrolled")
	if err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	if err := s.store.RevokeAgentTokensForAgent(r.Context(), home.ID, agent.ID, now); err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	rawToken := newToken()
	token := domain.AgentToken{ID: newID("agtok"), HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(rawToken), CreatedAt: now}
	if err := s.store.CreateAgentToken(r.Context(), token); err != nil {
		writeDesktopStoreError(w, err)
		return
	}
	s.audit(r.Context(), "agent.auto_enrolled", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "agent", agent.ID, map[string]any{"installation_id": agent.InstallationID, "policy": home.AgentEnrollmentPolicy})
	writeJSON(w, http.StatusCreated, map[string]any{"agent_id": agent.ID, "agent_name": agent.Name, "token": rawToken, "token_id": token.ID,
		"endpoint": desktopIdentityPublicSnapshot(identity), "root": map[string]any{"generation": root.Generation, "fingerprint": root.Fingerprint, "public_key_spki": base64.RawURLEncoding.EncodeToString(root.PublicKeySPKI)}})
}
