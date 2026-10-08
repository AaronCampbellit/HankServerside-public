package cloud

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const sessionCookieName = "hank_session"
const csrfCookieName = "hank_csrf"
const csrfHeaderName = "X-Hank-CSRF-Token"
const fileTransferCookiePrefix = "hank_transfer_"

func newID(prefix string) string {
	return prefix + "_" + randomHex(12)
}

func stableAssistantID(prefix string, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

func newToken() string {
	return randomHex(32)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func bearerToken(header string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", errors.New("missing authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", errors.New("authorization header must use Bearer token")
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", errors.New("bearer token is empty")
	}
	return token, nil
}

func sessionTokenFromCookie(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Lax (not Strict) so the session is sent on top-level cross-site
		// navigations like the MCP OAuth authorize redirect from ChatGPT/Claude.
		// Cross-site writes are still blocked (Lax) and CSRF tokens gate writes.
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		Expires:  expiresAt,
		MaxAge:   max(0, int(time.Until(expiresAt).Seconds())),
	})
	setCSRFCookie(w, r, newToken(), expiresAt)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   -1,
	})
}

func setCSRFCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:  csrfCookieName,
		Value: token,
		Path:  "/",
		// Lax so the CSRF cookie accompanies the session on the cross-site OAuth
		// authorize navigation, letting the consent page render a valid token.
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		Expires:  expiresAt,
		MaxAge:   max(0, int(time.Until(expiresAt).Seconds())),
	})
}

func csrfTokenFromCookie(r *http.Request) string {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func fileTransferCookieName(transferID string) string {
	clean := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(strings.TrimSpace(transferID))
	return fileTransferCookiePrefix + clean
}

func setFileTransferCookie(w http.ResponseWriter, r *http.Request, transferID string, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     fileTransferCookieName(transferID),
		Value:    token,
		Path:     "/v1/file-transfers/" + strings.Trim(transferID, "/"),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsHTTPS(r),
		Expires:  expiresAt,
		MaxAge:   max(0, int(time.Until(expiresAt).Seconds())),
	})
}

func fileTransferTokenFromCookie(r *http.Request, transferID string) string {
	cookie, err := r.Cookie(fileTransferCookieName(transferID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func unsafeHTTPMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func enforceSameOriginIfPresent(r *http.Request) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid origin")
	}
	if !strings.EqualFold(parsed.Host, r.Host) {
		return fmt.Errorf("origin not allowed")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("origin not allowed")
	}
	return nil
}

const defaultContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'self'; frame-ancestors 'self'; form-action 'self'"

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", defaultContentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

// streamingTransferDeadline bounds file-transfer request reads and response
// writes; it matches the previous server-wide WriteTimeout ceiling.
const streamingTransferDeadline = 30 * time.Minute

// routeDeadlineMiddleware scopes connection deadlines per route instead of
// relying on one server-wide WriteTimeout sized for the slowest transfer.
// File-transfer streams get a long read+write allowance, WebSocket upgrades
// get no HTTP-level deadline (the websocket library manages per-frame
// deadlines and both sides ping), and every other route keeps the tighter
// server defaults.
func routeDeadlineMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/file-transfers/"):
			deadline := time.Now().Add(streamingTransferDeadline)
			_ = rc.SetReadDeadline(deadline)
			_ = rc.SetWriteDeadline(deadline)
		case strings.HasPrefix(r.URL.Path, "/ws/"):
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}
		next.ServeHTTP(w, r)
	})
}

func randomHex(size int) string {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		panic(fmt.Sprintf("random token generation failed: %v", err))
	}
	return hex.EncodeToString(data)
}
