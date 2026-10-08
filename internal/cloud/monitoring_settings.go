package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

// The shared configuration volume lets Alertmanager retain the last applied
// email configuration independently of cloud availability. No Docker socket is
// exposed to cloud. User input cannot choose the reload URL or file path.
type monitoringDelivery struct {
	mu            sync.Mutex
	dir           string
	managerURL    string
	prometheusURL string
	group         int
	client        *http.Client
	lookup        func(context.Context, string, string) ([]netip.Addr, error)
	applied       [32]byte
	status        string
	lastTest      time.Time
}

func (s *Server) ConfigureMonitoringDelivery(dir, managerURL string, group int) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	u, err := url.Parse(managerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid monitoring manager URL")
	}
	s.monitoring = &monitoringDelivery{dir: dir, managerURL: strings.TrimRight(managerURL, "/"), group: group, client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, lookup: net.DefaultResolver.LookupNetIP, status: "pending"}
	return nil
}

func (s *Server) monitoringSettings(ctx context.Context, homeID string) (domain.MonitoringSettings, error) {
	v, err := s.store.GetMonitoringSettings(ctx, homeID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.DefaultMonitoringSettings(homeID), nil
	}
	return v, err
}

func (s *Server) runMonitoringDelivery(ctx context.Context) {
	if s.monitoring == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.monitoring.mu.Lock()
		home, err := s.store.GetSingletonHome(ctx)
		if err == nil {
			v, loadErr := s.monitoringSettings(ctx, home.ID)
			if loadErr == nil && v.WebhookToken == "" {
				v.WebhookToken = rand.Text()
				v.UpdatedAt = time.Now().UTC()
				loadErr = s.store.SaveMonitoringSettings(ctx, v)
			}
			if loadErr == nil {
				loadErr = s.monitoring.apply(ctx, v)
			}
			if loadErr != nil {
				s.monitoring.status = "pending"
			}
		}
		s.monitoring.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func validateMonitoringSettings(v *domain.MonitoringSettings) error {
	v.SMTPHost = strings.ToLower(strings.TrimSpace(v.SMTPHost))
	v.SMTPUsername = strings.TrimSpace(v.SMTPUsername)
	v.EmailFrom = strings.TrimSpace(v.EmailFrom)
	v.EmailTo = strings.TrimSpace(v.EmailTo)
	if v.InboxAudience != "admins" && v.InboxAudience != "members" {
		return errors.New("choose administrators or all Home members")
	}
	if v.SMTPPort < 1 || v.SMTPPort > 65535 || v.SMTPPort == 465 {
		return errors.New("use a STARTTLS SMTP port, usually 587")
	}
	if len(v.SMTPHost) > 253 || strings.ContainsAny(v.SMTPHost, " /\\\r\n\t{}?#@") || len(v.SMTPUsername) > 320 || strings.ContainsAny(v.SMTPUsername, "\r\n\x00") || len(v.SMTPPassword) > 4096 || strings.ContainsRune(v.SMTPPassword, 0) {
		return errors.New("invalid SMTP configuration")
	}
	for _, address := range []string{v.EmailFrom, v.EmailTo} {
		if address == "" && !v.EmailEnabled {
			continue
		}
		parsed, err := mail.ParseAddress(address)
		if err != nil || parsed.Address != address || len(address) > 254 || strings.ContainsAny(address, "{}\r\n") {
			return errors.New("enter one plain sender and recipient email address")
		}
	}
	if v.EmailEnabled && (v.SMTPHost == "" || v.EmailFrom == "" || v.EmailTo == "" || (v.SMTPUsername != "" && v.SMTPPassword == "")) {
		return errors.New("complete the SMTP server, sender, recipient, and account password")
	}
	return nil
}

func (m *monitoringDelivery) render(ctx context.Context, v domain.MonitoringSettings) ([]byte, error) {
	// JSON is a YAML subset and avoids interpolation/injection into generated
	// Alertmanager configuration. Email addresses cannot contain template syntax.
	receivers := []any{map[string]any{"name": "disabled"}}
	routes := []any{}
	if v.InboxEnabled && v.WebhookToken != "" {
		receivers = append(receivers, map[string]any{"name": "hank-inbox", "webhook_configs": []any{map[string]any{"url": "http://cloud:8080/v1/integrations/alertmanager", "send_resolved": true, "max_alerts": 0, "http_config": map[string]any{"authorization": map[string]any{"type": "Bearer", "credentials": v.WebhookToken}}}}})
		routes = append(routes, map[string]any{"receiver": "hank-inbox", "continue": true})
	}
	if v.EmailEnabled {
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		addresses, err := m.lookup(lookupCtx, "ip", v.SMTPHost)
		if err != nil {
			return nil, errors.New("SMTP hostname could not be resolved")
		}
		address := ""
		for _, ip := range addresses {
			if publicWebPushAddress(ip) {
				address = ip.String()
				break
			}
		}
		if address == "" {
			return nil, errors.New("SMTP server must resolve to a public address")
		}
		// Pin the validated IP, retaining the configured hostname for certificate
		// verification. Alertmanager cannot re-resolve it into an internal address.
		email := map[string]any{"to": v.EmailTo, "from": v.EmailFrom, "smarthost": net.JoinHostPort(address, strconv.Itoa(v.SMTPPort)), "auth_username": v.SMTPUsername, "auth_password": v.SMTPPassword, "require_tls": true, "send_resolved": true, "tls_config": map[string]any{"server_name": v.SMTPHost}}
		receivers = append(receivers, map[string]any{"name": "email", "email_configs": []any{email}})
		routes = append(routes, map[string]any{"receiver": "email"})
	}
	return json.Marshal(map[string]any{"route": map[string]any{"receiver": "disabled", "group_by": []string{"alertname", "instance"}, "group_wait": "30s", "group_interval": "5m", "repeat_interval": "4h", "routes": routes}, "receivers": receivers})
}

func (m *monitoringDelivery) apply(ctx context.Context, v domain.MonitoringSettings) error {
	body, err := m.render(ctx, v)
	if err != nil {
		m.status = "pending"
		return err
	}
	digest := sha256.Sum256(body)
	if digest == m.applied && m.status == "ready" {
		m.status = "ready"
		return nil
	}
	m.status = "pending"
	if err = writeMonitoringConfig(m.dir, m.group, body); err != nil {
		return err
	}
	if err = m.request(ctx, "/-/reload", nil); err != nil {
		return err
	}
	m.applied = digest
	m.status = "ready"
	return nil
}

func writeMonitoringConfig(dir string, group int, body []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.New("monitoring configuration storage is unavailable")
	}
	defer root.Close()
	name := ".config-" + rand.Text()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot prepare monitoring configuration")
	}
	defer func() { file.Close(); root.Remove(name) }()
	if group >= 0 {
		if err = file.Chown(-1, group); err != nil {
			return errors.New("cannot protect monitoring configuration")
		}
	}
	if err = file.Chmod(0640); err != nil {
		return err
	}
	if _, err = file.Write(body); err != nil {
		return errors.New("cannot write monitoring configuration")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = root.Rename(name, "alertmanager.yml"); err != nil {
		return errors.New("cannot publish monitoring configuration")
	}
	return nil
}

func (m *monitoringDelivery) request(ctx context.Context, path string, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.managerURL+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("delivery service unavailable")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return errors.New("delivery service unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("delivery service could not apply the request")
	}
	return nil
}

func (s *Server) handleHomeMonitoringSettings(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 0 || parts[0] != "monitoring-settings" {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, "administrator role required", 403)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.monitoring == nil {
		http.Error(w, "Monitoring configuration is unavailable on this server.", 503)
		return true
	}
	m := s.monitoring
	if len(parts) == 2 && parts[1] == "health" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, m.health(r.Context()))
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, err := s.monitoringSettings(r.Context(), home.ID)
	if err != nil {
		http.Error(w, "could not load monitoring settings", 500)
		return true
	}
	if len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost {
		if !v.InboxEnabled && !v.EmailEnabled {
			http.Error(w, "enable an alert destination before testing", 400)
			return true
		}
		if time.Since(m.lastTest) < 30*time.Second {
			http.Error(w, "wait 30 seconds before sending another test", 429)
			return true
		}
		if v.WebhookToken == "" || m.apply(r.Context(), v) != nil {
			http.Error(w, "save and apply settings before testing", 503)
			return true
		}
		now := time.Now().UTC()
		// Each short-lived test needs its own group_wait window; sharing the
		// five-minute group_interval can suppress a test before it expires.
		testID := rand.Text()
		payload, _ := json.Marshal([]any{map[string]any{"labels": map[string]string{"alertname": "HankNotificationTest", "instance": "hank-test-" + testID, "severity": "info", "test_id": testID}, "annotations": map[string]string{"summary": "Hank test alert — notification delivery is configured."}, "startsAt": now, "endsAt": now.Add(2 * time.Minute)}})
		m.lastTest = now
		if err = m.request(r.Context(), "/api/v2/alerts", payload); err != nil {
			http.Error(w, "test alert could not be queued", 503)
			return true
		}
		s.audit(r.Context(), "monitoring.delivery.test", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "home", home.ID, nil)
		writeJSON(w, 200, map[string]any{"queued": true})
		return true
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return true
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		var body struct {
			InboxEnabled  bool    `json:"inbox_enabled"`
			InboxAudience string  `json:"inbox_audience"`
			EmailEnabled  bool    `json:"email_enabled"`
			SMTPHost      string  `json:"smtp_host"`
			SMTPPort      int     `json:"smtp_port"`
			SMTPUsername  string  `json:"smtp_username"`
			EmailFrom     string  `json:"email_from"`
			EmailTo       string  `json:"email_to"`
			SMTPPassword  *string `json:"smtp_password"`
			ClearPassword bool    `json:"clear_password"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if err = parseJSON(w, r, &body); err != nil {
			http.Error(w, "invalid monitoring settings", 400)
			return true
		}
		if body.ClearPassword {
			v.SMTPPassword = ""
		}
		if body.SMTPPassword != nil && *body.SMTPPassword != "" {
			v.SMTPPassword = *body.SMTPPassword
		}
		v.InboxEnabled = body.InboxEnabled
		v.InboxAudience = body.InboxAudience
		v.EmailEnabled = body.EmailEnabled
		v.SMTPHost = body.SMTPHost
		v.SMTPPort = body.SMTPPort
		v.SMTPUsername = body.SMTPUsername
		v.EmailFrom = body.EmailFrom
		v.EmailTo = body.EmailTo
		if err = validateMonitoringSettings(&v); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		if v.WebhookToken == "" {
			v.WebhookToken = rand.Text()
		}
		if _, err = m.render(r.Context(), v); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		v.UpdatedAt = time.Now().UTC()
		v.UpdatedBy = auth.User.ID
		if err = s.store.SaveMonitoringSettings(r.Context(), v); err != nil {
			http.Error(w, "could not securely save monitoring settings", 500)
			return true
		}
		m.status = "pending"
		_ = m.apply(r.Context(), v)
		s.audit(r.Context(), "monitoring.settings.updated", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "home", home.ID, map[string]any{"email_enabled": v.EmailEnabled, "inbox_enabled": v.InboxEnabled, "inbox_audience": v.InboxAudience})
	default:
		http.Error(w, "method not allowed", 405)
		return true
	}
	writeJSON(w, 200, map[string]any{"settings": v, "smtp_password_set": v.SMTPPassword != "", "delivery_status": m.status})
	return true
}
