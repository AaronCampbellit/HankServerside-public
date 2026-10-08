package cloud

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const linuxEnrollmentTTL = 15 * time.Minute

var linuxDeviceIDPattern = regexp.MustCompile(`^linux_[a-zA-Z0-9._-]{16,122}$`)
var macPairingDeviceIDPattern = regexp.MustCompile(`^mac_[a-zA-Z0-9._-]{16,122}$`)
var windowsPairingDeviceIDPattern = regexp.MustCompile(`^win_[a-zA-Z0-9._-]{16,122}$`)
var lowerHexSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type linuxEnrollmentCreateRequest struct {
	Pairing  bool              `json:"pairing"`
	NameHint string            `json:"name_hint"`
	Labels   map[string]string `json:"labels"`
}

type linuxEnrollmentConsumeRequest struct {
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	AgentType      string `json:"agent_type"`
	Platform       string `json:"platform"`
	Architecture   string `json:"architecture"`
	OSVersion      string `json:"os_version"`
	AgentVersion   string `json:"agent_version"`
	CredentialHash string `json:"credential_hash"`
}

func (s *Server) handleHomeLinuxAgentEnrollments(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 0 || parts[0] != "agent-enrollments" || (len(parts) > 1 && parts[1] != "linux" && parts[1] != "macos" && parts[1] != "windows") {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}
	platform := ""
	if len(parts) > 1 {
		platform = parts[1]
	}
	now := time.Now().UTC()
	if len(parts) == 2 && r.Method == http.MethodPost {
		var body linuxEnrollmentCreateRequest
		if r.ContentLength > 0 {
			if err := parseJSON(w, r, &body); err != nil {
				http.Error(w, "invalid device enrollment request", http.StatusBadRequest)
				return true
			}
		}
		if platform != "linux" {
			body.Pairing = true
		}
		body.NameHint = strings.TrimSpace(body.NameHint)
		if len(body.NameHint) > 128 || len(body.Labels) > 32 {
			http.Error(w, "invalid device enrollment request", http.StatusBadRequest)
			return true
		}
		raw := newToken()
		if body.Pairing {
			random := make([]byte, 8)
			if _, err := rand.Read(random); err != nil {
				http.Error(w, "pairing unavailable", 500)
				return true
			}
			raw = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)[:12]
		}
		value := domain.AgentEnrollment{ID: newID("aenroll"), HomeID: home.ID, Platform: platform, TokenHash: hashToken(raw), CreatedByUserID: auth.User.ID, NameHint: body.NameHint, Labels: body.Labels, CreatedAt: now, ExpiresAt: now.Add(linuxEnrollmentTTL)}
		if err := s.store.CreateAgentEnrollment(r.Context(), value); err != nil {
			http.Error(w, "could not create device enrollment", http.StatusInternalServerError)
			return true
		}
		if body.Pairing {
			w.Header().Set("Cache-Control", "no-store")
			s.audit(r.Context(), "agent.pairing.created", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "agent_enrollment", value.ID, nil)
			writeJSON(w, http.StatusCreated, map[string]any{"id": value.ID, "pairing_code": raw[:4] + "-" + raw[4:8] + "-" + raw[8:], "server_url": absoluteRequestURL(r, ""), "created_at": value.CreatedAt, "expires_at": value.ExpiresAt})
			return true
		}
		command := fmt.Sprintf("curl -fsSL %q | sudo bash", absoluteRequestURL(r, "/install/linux/"+raw))
		s.audit(r.Context(), "agent.enrollment.created", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "agent_enrollment", value.ID, map[string]any{"platform": "linux", "expires_at": value.ExpiresAt})
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		writeJSON(w, http.StatusCreated, map[string]any{"id": value.ID, "install_command": command, "created_at": value.CreatedAt, "expires_at": value.ExpiresAt})
		return true
	}
	if (len(parts) == 1 || len(parts) == 2) && r.Method == http.MethodGet {
		values, err := s.store.ListAgentEnrollments(r.Context(), home.ID)
		if err != nil {
			http.Error(w, "could not list device enrollments", http.StatusInternalServerError)
			return true
		}
		if platform != "" {
			filtered := make([]domain.AgentEnrollment, 0, len(values))
			for _, value := range values {
				if value.Platform == platform {
					filtered = append(filtered, value)
				}
			}
			values = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"enrollments": values})
		return true
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		if err := s.store.RevokeAgentEnrollment(r.Context(), home.ID, parts[2], now); err != nil {
			http.NotFound(w, r)
			return true
		}
		s.audit(r.Context(), "agent.enrollment.revoked", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "agent_enrollment", parts[2], nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return true
}

func (s *Server) handleLinuxInstaller(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/install/linux/")
	if !lowerHexSHA256Pattern.MatchString(raw) {
		http.NotFound(w, r)
		return
	}
	hash := hashToken(raw)
	now := time.Now().UTC()
	value, err := s.store.GetActiveAgentEnrollmentByHash(r.Context(), hash, now)
	if err != nil || value.Platform != "linux" || subtle.ConstantTimeCompare([]byte(value.TokenHash), []byte(hash)) != 1 {
		http.NotFound(w, r)
		return
	}
	if err := s.store.MarkAgentEnrollmentDownloaded(r.Context(), hash, now); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	_, _ = w.Write([]byte(renderLinuxInstallerScript(absoluteRequestURL(r, ""), raw)))
}

func renderLinuxInstallerScript(serverURL, token string) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu
test "$(id -u)" -eq 0 || { echo "Run this installer through sudo." >&2; exit 1; }
umask 077
tmp=$(mktemp -d)
trap 'find "$tmp" -depth -delete' EXIT HUP INT TERM
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "Unsupported Linux architecture: $arch" >&2; exit 1 ;; esac
curl -fsSL %q/install/linux-release/hankagent-linux-$arch -o "$tmp/hankagent-linux-$arch"
curl -fsSL %q/install/linux-release/hankagent-linux-$arch.sha256 -o "$tmp/hankagent-linux-$arch.sha256"
(cd "$tmp" && sha256sum -c hankagent-linux-$arch.sha256)
install -m 0755 "$tmp/hankagent-linux-$arch" /usr/bin/hankagent
printf '%%s\n' %q | /usr/bin/hankagent --system enroll --server %q --enrollment-token-stdin
/usr/bin/hankagent --system service install
echo "HankAgent Linux installed and started."
`, serverURL, serverURL, token, serverURL)
}

func (s *Server) handleLinuxAgentEnrollmentConsume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "Hank-Enrollment "
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, prefix) {
		http.Error(w, "invalid enrollment", http.StatusNotFound)
		return
	}
	raw := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	pairing := regexp.MustCompile(`^[A-Z2-7]{12}$`).MatchString(strings.ToUpper(strings.ReplaceAll(raw, "-", "")))
	if pairing {
		raw = strings.ToUpper(strings.ReplaceAll(raw, "-", ""))
		if !s.limiter.Allow("linux_pair_ip:"+clientIP(r), 10, time.Minute) || !s.limiter.Allow("linux_pair_global", 100, time.Minute) {
			http.Error(w, "too many pairing attempts; try again later", 429)
			return
		}
	}
	if !pairing && !lowerHexSHA256Pattern.MatchString(raw) {
		http.Error(w, "invalid enrollment", http.StatusNotFound)
		return
	}
	var body linuxEnrollmentConsumeRequest
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, "invalid enrollment", http.StatusBadRequest)
		return
	}
	body.DeviceID, body.Name = strings.TrimSpace(body.DeviceID), strings.TrimSpace(body.Name)
	validArch := body.Architecture == "amd64" || body.Architecture == "arm64"
	if !validPairingDeviceID(body.Platform, body.DeviceID) || body.Name == "" || len(body.Name) > 128 || body.AgentType != "worker" || !validArch || !lowerHexSHA256Pattern.MatchString(body.CredentialHash) || len(body.OSVersion) > 128 || len(body.AgentVersion) > 64 {
		http.Error(w, "invalid enrollment", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	enrollment, err := s.store.GetActiveAgentEnrollmentByHash(r.Context(), hashToken(raw), now)
	if err != nil || enrollment.Platform != body.Platform || (r.URL.Path == "/v1/agent/enrollments/linux/consume" && body.Platform != "linux") {
		http.Error(w, "invalid enrollment", http.StatusNotFound)
		return
	}
	member, err := s.store.GetHomeMembership(r.Context(), enrollment.HomeID, enrollment.CreatedByUserID)
	if err != nil || member.Role != domain.HomeRoleAdmin {
		http.Error(w, "invalid enrollment", http.StatusNotFound)
		return
	}
	creator, err := s.store.GetUserByID(r.Context(), enrollment.CreatedByUserID)
	if err != nil || creator.PasswordChangeRequired {
		http.Error(w, "invalid enrollment", http.StatusNotFound)
		return
	}
	credentialID := newID("agtok")
	value, err := s.store.ConsumeAgentEnrollment(r.Context(), store.ConsumeAgentEnrollmentInput{TokenHash: hashToken(raw), Platform: body.Platform, AgentID: body.DeviceID, Name: body.Name, CredentialID: credentialID, CredentialHash: body.CredentialHash, InstallationID: body.DeviceID, Now: now})
	if err != nil {
		http.Error(w, "invalid enrollment", http.StatusNotFound)
		return
	}
	s.audit(r.Context(), "agent.enrollment.consumed", auditSeverityCritical, value.CreatedByUserID, value.AgentID, value.HomeID, requestIDFromContext(r.Context()), "agent_enrollment", value.ID, map[string]any{"platform": body.Platform, "architecture": body.Architecture})
	mode := "user"
	if body.Platform == "linux" {
		mode = "system"
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent_id": value.AgentID, "credential_id": credentialID, "server_url": absoluteRequestURL(r, ""), "installation_mode": mode})
}

func validPairingDeviceID(platform, id string) bool {
	switch platform {
	case "linux":
		return linuxDeviceIDPattern.MatchString(id)
	case "macos":
		return macPairingDeviceIDPattern.MatchString(id)
	case "windows":
		return windowsPairingDeviceIDPattern.MatchString(id)
	}
	return false
}
