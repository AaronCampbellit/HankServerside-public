package cloud

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const entraProvider = "entra"
const entraTransactionTTL = 10 * time.Minute

type EntraConfig struct {
	Enabled       bool
	TenantID      string
	ClientID      string
	ClientSecret  string
	PublicBaseURL string
}

type entraService struct {
	tenantID string
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	tx       *entraTransactionRegistry
}

type entraTransaction struct {
	State         string
	Nonce         string
	Verifier      string
	InviteToken   string
	LinkUserID    string
	LinkSessionID string
	ExpiresAt     time.Time
}

type entraTransactionRegistry struct {
	mu     sync.Mutex
	values map[string]entraTransaction
}

func newEntraTransactionRegistry() *entraTransactionRegistry {
	return &entraTransactionRegistry{values: make(map[string]entraTransaction)}
}

func (r *entraTransactionRegistry) create(value entraTransaction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for key, existing := range r.values {
		if !existing.ExpiresAt.After(now) {
			delete(r.values, key)
		}
	}
	r.values[value.State] = value
}

func (r *entraTransactionRegistry) consume(state string) (entraTransaction, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[state]
	delete(r.values, state)
	return value, ok && value.ExpiresAt.After(time.Now().UTC())
}

func (s *Server) ConfigureEntra(ctx context.Context, cfg EntraConfig) error {
	service, err := newEntraService(ctx, cfg)
	if err != nil {
		return err
	}
	s.entra = service
	return nil
}

func newEntraService(ctx context.Context, cfg EntraConfig) (*entraService, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	cfg.TenantID = strings.TrimSpace(cfg.TenantID)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.ClientSecret = strings.TrimSpace(cfg.ClientSecret)
	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if cfg.TenantID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.PublicBaseURL == "" {
		return nil, errors.New("incomplete Entra configuration")
	}
	base, err := url.Parse(cfg.PublicBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("invalid Entra public base URL")
	}
	if base.Scheme != "https" && base.Hostname() != "localhost" && base.Hostname() != "127.0.0.1" {
		return nil, errors.New("Entra public base URL must use HTTPS outside local development")
	}
	provider, err := oidc.NewProvider(ctx, "https://login.microsoftonline.com/"+url.PathEscape(cfg.TenantID)+"/v2.0")
	if err != nil {
		return nil, fmt.Errorf("discover Entra OIDC provider: %w", err)
	}
	callbackURL := cfg.PublicBaseURL + "/v1/auth/entra/callback"
	return &entraService{
		tenantID: cfg.TenantID,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth:    oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: callbackURL, Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"}},
		tx:       newEntraTransactionRegistry(),
	}, nil
}

func (s *Server) handleEntraStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.entra == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		InvitationToken string `json:"invitation_token"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.limiter.Allow("entra-start:"+clientIP(r), 20, time.Minute) {
		http.Error(w, "too many sign-in attempts", http.StatusTooManyRequests)
		return
	}
	if strings.TrimSpace(body.InvitationToken) != "" {
		if _, ok := s.loadUsableInvitation(w, r, body.InvitationToken); !ok {
			return
		}
	}
	s.startEntraAuthorization(w, r, strings.TrimSpace(body.InvitationToken), "", "")
}

func (s *Server) handleEntraLinkStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.entra == nil {
		http.NotFound(w, r)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	s.startEntraAuthorization(w, r, "", auth.User.ID, auth.Session.ID)
}

func (s *Server) startEntraAuthorization(w http.ResponseWriter, r *http.Request, inviteToken, linkUserID, linkSessionID string) {
	state, err := randomURLToken()
	if err != nil {
		http.Error(w, "could not start Entra sign-in", http.StatusInternalServerError)
		return
	}
	nonce, err := randomURLToken()
	if err != nil {
		http.Error(w, "could not start Entra sign-in", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	s.entra.tx.create(entraTransaction{State: state, Nonce: nonce, Verifier: verifier, InviteToken: inviteToken, LinkUserID: linkUserID, LinkSessionID: linkSessionID, ExpiresAt: time.Now().UTC().Add(entraTransactionTTL)})
	writeJSON(w, http.StatusOK, map[string]any{"authorization_url": s.entra.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))})
}

func (s *Server) handleEntraCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.entra == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("error") != "" {
		s.entraFailure(w, r, "provider_error")
		return
	}
	transaction, ok := s.entra.tx.consume(strings.TrimSpace(r.URL.Query().Get("state")))
	if !ok {
		s.entraFailure(w, r, "invalid_state")
		return
	}
	if transaction.LinkSessionID != "" {
		auth, err := s.appAuthFromRequest(r)
		if err != nil || auth.Session.ID != transaction.LinkSessionID || auth.User.ID != transaction.LinkUserID {
			s.entraFailure(w, r, "link_session_expired")
			return
		}
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.entraFailure(w, r, "missing_code")
		return
	}
	token, err := s.entra.oauth.Exchange(r.Context(), code, oauth2.VerifierOption(transaction.Verifier))
	if err != nil {
		s.entraFailure(w, r, "code_exchange_failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		s.entraFailure(w, r, "missing_id_token")
		return
	}
	idToken, err := s.entra.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		s.entraFailure(w, r, "invalid_id_token")
		return
	}
	var claims struct {
		TenantID          string `json:"tid"`
		ObjectID          string `json:"oid"`
		Nonce             string `json:"nonce"`
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.TenantID != s.entra.tenantID || claims.ObjectID == "" || claims.Nonce != transaction.Nonce {
		s.entraFailure(w, r, "invalid_claims")
		return
	}
	user, err := s.resolveEntraUser(r.Context(), transaction, claims)
	if err != nil {
		s.audit(r.Context(), "sso.login.failed", auditSeverityWarning, "", "", "", requestIDFromContext(r.Context()), "entra", stableAuditTarget(claims.ObjectID), map[string]any{"reason": "not_provisioned"})
		s.entraFailure(w, r, "not_provisioned")
		return
	}
	session, rawSession, err := s.createSession(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	s.audit(r.Context(), "sso.login.succeeded", auditSeverityInfo, user.ID, "", s.auditHomeIDForUser(r.Context(), user.ID), requestIDFromContext(r.Context()), "session", session.ID, map[string]any{"provider": entraProvider})
	setSessionCookie(w, r, rawSession, session.ExpiresAt)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) resolveEntraUser(ctx context.Context, transaction entraTransaction, claims struct {
	TenantID          string `json:"tid"`
	ObjectID          string `json:"oid"`
	Nonce             string `json:"nonce"`
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
}) (domain.User, error) {
	if user, err := s.store.GetUserByExternalIdentity(ctx, entraProvider, claims.TenantID, claims.ObjectID); err == nil {
		if transaction.InviteToken != "" {
			invitation, err := s.store.GetHomeInvitationByTokenHash(ctx, hashToken(transaction.InviteToken))
			if err != nil || invitation.AcceptedAt != nil || (invitation.ExpiresAt != nil && !invitation.ExpiresAt.After(time.Now().UTC())) {
				return domain.User{}, errors.New("invitation is not usable")
			}
			if err := s.store.AcceptHomeInvitation(ctx, invitation.ID, user, invitation.Role); err != nil {
				return domain.User{}, err
			}
		}
		return user, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.User{}, err
	}
	now := time.Now().UTC()
	if transaction.LinkUserID != "" {
		user, err := s.store.GetUserByID(ctx, transaction.LinkUserID)
		if err != nil {
			return domain.User{}, err
		}
		identity := domain.ExternalIdentity{ID: newID("eid"), UserID: user.ID, Provider: entraProvider, TenantID: claims.TenantID, SubjectID: claims.ObjectID, CreatedAt: now, UpdatedAt: now}
		if err := s.store.CreateExternalIdentity(ctx, identity); err != nil {
			return domain.User{}, err
		}
		s.audit(ctx, "sso.identity.linked", auditSeverityInfo, user.ID, "", s.auditHomeIDForUser(ctx, user.ID), "", "user", user.ID, map[string]any{"provider": entraProvider})
		return user, nil
	}
	if transaction.InviteToken != "" {
		invitation, err := s.store.GetHomeInvitationByTokenHash(ctx, hashToken(transaction.InviteToken))
		if err != nil {
			return domain.User{}, err
		}
		if invitation.AcceptedAt != nil || (invitation.ExpiresAt != nil && !invitation.ExpiresAt.After(now)) {
			return domain.User{}, errors.New("invitation is not usable")
		}
		if _, err := s.store.GetUserByEmail(ctx, invitation.Email); err == nil {
			return domain.User{}, errors.New("existing account must be linked")
		} else if !errors.Is(err, store.ErrNotFound) {
			return domain.User{}, err
		}
		user := domain.User{ID: newID("usr"), Email: invitation.Email, PasswordHash: hashToken(newToken()), CreatedAt: now, UpdatedAt: now}
		if err := s.store.CreateSSOUserAndAcceptHomeInvitation(ctx, invitation.ID, user, domain.ExternalIdentity{ID: newID("eid"), UserID: user.ID, Provider: entraProvider, TenantID: claims.TenantID, SubjectID: claims.ObjectID, CreatedAt: now, UpdatedAt: now}, invitation.Role); err != nil {
			return domain.User{}, err
		}
		s.audit(ctx, "sso.user.provisioned", auditSeverityInfo, user.ID, "", invitation.HomeID, "", "user", user.ID, map[string]any{"provider": entraProvider})
		return user, nil
	}
	count, err := s.store.CountHomes(ctx)
	if err != nil || count != 0 {
		return domain.User{}, errors.New("registration is unavailable")
	}
	email := strings.TrimSpace(strings.ToLower(claims.Email))
	if email == "" {
		email = strings.TrimSpace(strings.ToLower(claims.PreferredUsername))
	}
	if email == "" {
		return domain.User{}, errors.New("Entra account has no email")
	}
	user := domain.User{ID: newID("usr"), Email: email, PasswordHash: hashToken(newToken()), CreatedAt: now, UpdatedAt: now}
	if _, err := s.store.CreateSSOUserAndBootstrapSingletonHome(ctx, user, domain.ExternalIdentity{ID: newID("eid"), UserID: user.ID, Provider: entraProvider, TenantID: claims.TenantID, SubjectID: claims.ObjectID, CreatedAt: now, UpdatedAt: now}, "Home"); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Server) entraFailure(w http.ResponseWriter, r *http.Request, reason string) {
	s.metrics.IncAuthFailure("entra_" + reason)
	http.Redirect(w, r, "/?sso_error="+url.QueryEscape(reason), http.StatusSeeOther)
}

func randomURLToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
