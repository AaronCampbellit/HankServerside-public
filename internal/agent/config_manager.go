package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	agentha "github.com/dropfile/HankServerside/internal/agent/homeassistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

var errUnsupportedServiceType = errors.New("unsupported service type")

type configManager struct {
	mu       sync.Mutex
	envPath  string
	ha       *agentha.Client
	files    *agentfiles.Service
	profiles map[string]protocol.ServiceProfileSnapshot
	testSMB  func(context.Context, agentfiles.SMBConfig) error
}

func newConfigManager(envPath string, ha *agentha.Client, files *agentfiles.Service) *configManager {
	m := &configManager{
		envPath:  strings.TrimSpace(envPath),
		ha:       ha,
		files:    files,
		profiles: make(map[string]protocol.ServiceProfileSnapshot, 3),
		testSMB: func(ctx context.Context, cfg agentfiles.SMBConfig) error {
			probe := agentfiles.NewWithConfig(agentfiles.Config{Shares: []agentfiles.SMBConfig{cfg}})
			defer probe.ApplySMBConfigs(nil)
			_, err := probe.ListSource(ctx, cfg.ID, "")
			return err
		},
	}
	m.profiles[domain.ServiceTypeHomeAssistant] = m.homeAssistantProfile(0, 0, "")
	m.profiles[domain.ServiceTypeSMB] = m.smbProfile(0, 0, "")
	return m
}

func (m *configManager) TestSMB(ctx context.Context, request protocol.ConfigSMBTestRequest) (protocol.ConfigSMBTestResponse, error) {
	cfg := agentfiles.SMBConfig{
		ID:       strings.TrimSpace(request.ID),
		Name:     strings.TrimSpace(request.Name),
		Host:     strings.TrimSpace(request.Host),
		Share:    strings.TrimSpace(request.Share),
		Username: strings.TrimSpace(request.Username),
		Password: request.Password,
		Domain:   strings.TrimSpace(request.Domain),
	}
	if cfg.Password == "" {
		for _, existing := range m.files.SMBConfigs() {
			if existing.ID == cfg.ID {
				cfg.Password = existing.Password
				break
			}
		}
	}
	if cfg.ID == "" {
		return protocol.ConfigSMBTestResponse{}, errors.New("SMB share ID is required")
	}
	if !cfg.Enabled() {
		return protocol.ConfigSMBTestResponse{}, errors.New("SMB host and share are required")
	}
	if err := m.testSMB(ctx, cfg); err != nil {
		return protocol.ConfigSMBTestResponse{}, fmt.Errorf("SMB connection failed: %w", err)
	}
	return protocol.ConfigSMBTestResponse{OK: true}, nil
}

func (m *configManager) Status(_ context.Context, serviceType string) ([]protocol.ServiceProfileSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if serviceType != "" {
		profile, ok := m.profiles[serviceType]
		if !ok {
			return nil, errUnsupportedServiceType
		}
		return []protocol.ServiceProfileSnapshot{profile}, nil
	}

	serviceTypes := make([]string, 0, len(m.profiles))
	for serviceType := range m.profiles {
		serviceTypes = append(serviceTypes, serviceType)
	}
	sort.Strings(serviceTypes)

	profiles := make([]protocol.ServiceProfileSnapshot, 0, len(serviceTypes))
	for _, serviceType := range serviceTypes {
		profiles = append(profiles, m.profiles[serviceType])
	}
	return profiles, nil
}

func (m *configManager) Apply(_ context.Context, request protocol.ConfigApplyRequest) (protocol.ServiceProfileSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch request.ServiceType {
	case domain.ServiceTypeHomeAssistant:
		var public struct {
			BaseURL        string `json:"base_url"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if len(request.PublicConfig) > 0 {
			if err := json.Unmarshal(request.PublicConfig, &public); err != nil {
				return protocol.ServiceProfileSnapshot{}, err
			}
		}
		var secrets struct {
			Token string `json:"token"`
		}
		if len(request.Secrets) > 0 {
			if err := json.Unmarshal(request.Secrets, &secrets); err != nil {
				return protocol.ServiceProfileSnapshot{}, err
			}
		}
		timeout := time.Duration(public.TimeoutSeconds) * time.Second
		m.ha.ApplyConfig(public.BaseURL, secrets.Token, timeout)
		profile := m.homeAssistantProfile(request.SecretVersion, request.SecretVersion, "")
		m.profiles[request.ServiceType] = profile
		if request.Persist {
			if err := m.persistHomeAssistant(public.BaseURL, secrets.Token, public.TimeoutSeconds); err != nil {
				profile.Status = domain.SyncStatusDegraded
				profile.LastError = err.Error()
				m.profiles[request.ServiceType] = profile
				return profile, err
			}
		}
		return profile, nil

	case domain.ServiceTypeSMB:
		var public smbPublicConfig
		if len(request.PublicConfig) > 0 {
			if err := json.Unmarshal(request.PublicConfig, &public); err != nil {
				return protocol.ServiceProfileSnapshot{}, err
			}
		}
		var secrets smbSecretConfig
		if len(request.Secrets) > 0 {
			if err := json.Unmarshal(request.Secrets, &secrets); err != nil {
				return protocol.ServiceProfileSnapshot{}, err
			}
		}
		configs := smbConfigsFromApply(public, secrets, m.files.SMBConfigs())
		applyLocal := public.Folders != nil
		var locals []agentfiles.LocalConfig
		if applyLocal {
			var err error
			locals, err = prepareLocalConfigs(public.Folders)
			if err != nil {
				profile := m.smbProfile(request.SecretVersion, request.SecretVersion, err.Error())
				m.profiles[request.ServiceType] = profile
				return profile, err
			}
		}

		if request.Persist {
			if err := m.persistFileConfigs(configs, locals, applyLocal); err != nil {
				profile := m.smbProfile(request.SecretVersion, request.SecretVersion, err.Error())
				profile.Status = domain.SyncStatusDegraded
				profile.LastError = err.Error()
				m.profiles[request.ServiceType] = profile
				return profile, err
			}
		}

		m.files.ApplySMBConfigs(configs)
		if applyLocal {
			m.files.ApplyLocalConfigs(locals)
		}

		profile := m.smbProfile(request.SecretVersion, request.SecretVersion, "")
		m.profiles[request.ServiceType] = profile
		return profile, nil

	default:
		return protocol.ServiceProfileSnapshot{}, errUnsupportedServiceType
	}
}

func (m *configManager) homeAssistantProfile(secretVersion int, appliedVersion int, lastError string) protocol.ServiceProfileSnapshot {
	publicConfig, _ := json.Marshal(m.ha.Snapshot())
	status := domain.SyncStatusHealthy
	if lastError != "" {
		status = domain.SyncStatusDegraded
	}
	return protocol.ServiceProfileSnapshot{
		ServiceType:    domain.ServiceTypeHomeAssistant,
		PublicConfig:   publicConfig,
		SecretVersion:  secretVersion,
		AppliedVersion: appliedVersion,
		Status:         status,
		LastError:      lastError,
		UpdatedAt:      time.Now().UTC(),
	}
}

func (m *configManager) smbProfile(secretVersion int, appliedVersion int, lastError string) protocol.ServiceProfileSnapshot {
	publicConfig, _ := json.Marshal(m.files.Snapshot())
	status := domain.SyncStatusHealthy
	if lastError != "" {
		status = domain.SyncStatusDegraded
	}
	return protocol.ServiceProfileSnapshot{
		ServiceType:    domain.ServiceTypeSMB,
		PublicConfig:   publicConfig,
		SecretVersion:  secretVersion,
		AppliedVersion: appliedVersion,
		Status:         status,
		LastError:      lastError,
		UpdatedAt:      time.Now().UTC(),
	}
}

type smbPublicConfig struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Host        string                  `json:"host"`
	Share       string                  `json:"share"`
	Username    string                  `json:"username"`
	Domain      string                  `json:"domain"`
	Policy      agentfiles.AccessPolicy `json:"policy"`
	Shares      []smbSharePayload       `json:"shares"`
	FileSources []smbSharePayload       `json:"file_sources"`
	Sources     []smbSharePayload       `json:"sources"`
	// Folders carries host directory sources. A nil slice means the request did
	// not manage folders (leave them untouched); a non-nil slice replaces them.
	Folders []localFolderPayload `json:"folders"`
}

type localFolderPayload struct {
	ID       string                  `json:"id"`
	SourceID string                  `json:"source_id"`
	Name     string                  `json:"name"`
	Root     string                  `json:"root"`
	Path     string                  `json:"path"`
	Create   bool                    `json:"create"`
	Policy   agentfiles.AccessPolicy `json:"policy"`
}

type smbSecretConfig struct {
	Password string            `json:"password"`
	Shares   []smbShareSecret  `json:"shares"`
	ByID     map[string]string `json:"by_id"`
	SourceID string            `json:"source_id"`
}

type smbSharePayload struct {
	ID          string                  `json:"id"`
	SourceID    string                  `json:"source_id"`
	Name        string                  `json:"name"`
	Type        string                  `json:"type"`
	Host        string                  `json:"host"`
	Share       string                  `json:"share"`
	Username    string                  `json:"username"`
	Domain      string                  `json:"domain"`
	SMBHost     string                  `json:"smb_host"`
	SMBShare    string                  `json:"smb_share"`
	SMBUsername string                  `json:"smb_username"`
	SMBDomain   string                  `json:"smb_domain"`
	Policy      agentfiles.AccessPolicy `json:"policy"`
}

type smbShareSecret struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Password string `json:"password"`
}

func smbConfigsFromApply(public smbPublicConfig, secrets smbSecretConfig, existing []agentfiles.SMBConfig) []agentfiles.SMBConfig {
	managesSMB := public.Shares != nil || public.FileSources != nil || public.Sources != nil ||
		strings.TrimSpace(public.Host) != "" || strings.TrimSpace(public.Share) != ""
	if !managesSMB {
		return append([]agentfiles.SMBConfig(nil), existing...)
	}

	publicShares := public.Shares
	if len(publicShares) == 0 {
		publicShares = filterSMBSourcePayloads(public.FileSources)
	}
	if len(publicShares) == 0 {
		publicShares = filterSMBSourcePayloads(public.Sources)
	}

	configs := make([]agentfiles.SMBConfig, 0, len(publicShares))
	for _, share := range publicShares {
		policy := share.Policy
		if !policy.HasRules() {
			policy = public.Policy
		}
		configs = append(configs, agentfiles.SMBConfig{
			ID:       strings.TrimSpace(firstNonEmpty(share.ID, share.SourceID)),
			Name:     strings.TrimSpace(share.Name),
			Host:     strings.TrimSpace(firstNonEmpty(share.Host, share.SMBHost)),
			Share:    strings.TrimSpace(firstNonEmpty(share.Share, share.SMBShare)),
			Username: strings.TrimSpace(firstNonEmpty(share.Username, share.SMBUsername)),
			Domain:   strings.TrimSpace(firstNonEmpty(share.Domain, share.SMBDomain)),
			Policy:   policy,
		})
	}
	// Fall back to the legacy single-share top-level fields only when they
	// actually carry a target; otherwise an SMB-free save (for example one that
	// only manages host folders) would synthesize a phantom empty share.
	if len(configs) == 0 && (strings.TrimSpace(public.Host) != "" || strings.TrimSpace(public.Share) != "") {
		configs = append(configs, agentfiles.SMBConfig{
			ID:       strings.TrimSpace(public.ID),
			Name:     strings.TrimSpace(public.Name),
			Host:     strings.TrimSpace(public.Host),
			Share:    strings.TrimSpace(public.Share),
			Username: strings.TrimSpace(public.Username),
			Domain:   strings.TrimSpace(public.Domain),
			Policy:   public.Policy,
		})
	}

	passwords := map[string]string{}
	for _, cfg := range existing {
		passwords[cfg.ID] = cfg.Password
	}
	if secrets.Password != "" {
		targetID := strings.TrimSpace(secrets.SourceID)
		if targetID == "" && len(configs) == 1 {
			targetID = configs[0].ID
		}
		if targetID != "" {
			passwords[targetID] = secrets.Password
		}
	}
	for id, password := range secrets.ByID {
		if strings.TrimSpace(id) != "" && password != "" {
			passwords[strings.TrimSpace(id)] = password
		}
	}
	for _, secret := range secrets.Shares {
		id := strings.TrimSpace(firstNonEmpty(secret.ID, secret.SourceID))
		if id != "" && secret.Password != "" {
			passwords[id] = secret.Password
		}
	}
	for i := range configs {
		configs[i].Password = passwords[configs[i].ID]
	}
	return configs
}

func filterSMBSourcePayloads(sources []smbSharePayload) []smbSharePayload {
	filtered := make([]smbSharePayload, 0, len(sources))
	for _, source := range sources {
		if source.Type == "" || source.Type == "smb" {
			filtered = append(filtered, source)
		}
	}
	return filtered
}

// prepareLocalConfigs validates host folder payloads, optionally creating the
// directories, and returns the configs ready to apply. The host folder may live
// anywhere on the host filesystem; per-folder access is confined to the tree by
// the files service resolve helpers.
func prepareLocalConfigs(payloads []localFolderPayload) ([]agentfiles.LocalConfig, error) {
	configs := make([]agentfiles.LocalConfig, 0, len(payloads))
	for _, payload := range payloads {
		root := strings.TrimSpace(firstNonEmpty(payload.Root, payload.Path))
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("host folder %q must be an absolute path", root)
		}
		root = filepath.Clean(root)
		if payload.Create {
			if err := os.MkdirAll(root, 0o755); err != nil {
				return nil, fmt.Errorf("create host folder %q: %w", root, err)
			}
		}
		info, err := os.Stat(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("host folder %q does not exist", root)
			}
			return nil, fmt.Errorf("host folder %q: %w", root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("host folder %q is not a directory", root)
		}
		configs = append(configs, agentfiles.LocalConfig{
			ID:     strings.TrimSpace(firstNonEmpty(payload.ID, payload.SourceID)),
			Name:   strings.TrimSpace(payload.Name),
			Root:   root,
			Policy: payload.Policy,
		})
	}
	return configs, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (m *configManager) persistHomeAssistant(baseURL string, token string, timeoutSeconds int) error {
	if m.envPath == "" {
		return nil
	}
	env, err := m.readEnvFile()
	if err != nil {
		return err
	}
	env["HANK_HA_BASE_URL"] = strings.TrimSpace(baseURL)
	env["HANK_HA_TOKEN"] = token
	if timeoutSeconds > 0 {
		env["HANK_HA_TIMEOUT_SECONDS"] = fmt.Sprintf("%d", timeoutSeconds)
	}
	return writeEnvFile(m.envPath, env)
}

func (m *configManager) persistSMB(host string, share string, username string, password string, domainName string) error {
	return m.persistSMBConfigs([]agentfiles.SMBConfig{{
		Host:     host,
		Share:    share,
		Username: username,
		Password: password,
		Domain:   domainName,
	}})
}

func (m *configManager) persistSMBConfigs(configs []agentfiles.SMBConfig) error {
	if m.envPath == "" {
		return nil
	}
	env, err := m.readEnvFile()
	if err != nil {
		return err
	}
	if err := applySMBConfigsToEnv(env, configs); err != nil {
		return err
	}
	return writeEnvFile(m.envPath, env)
}

func applySMBConfigsToEnv(env map[string]string, configs []agentfiles.SMBConfig) error {
	delete(env, "HANK_SMB_HOST")
	delete(env, "HANK_SMB_SHARE")
	delete(env, "HANK_SMB_USERNAME")
	delete(env, "HANK_SMB_PASSWORD")
	delete(env, "HANK_SMB_DOMAIN")
	type envShare struct {
		ID       string                  `json:"id,omitempty"`
		Name     string                  `json:"name,omitempty"`
		Host     string                  `json:"host"`
		Share    string                  `json:"share"`
		Username string                  `json:"username,omitempty"`
		Password string                  `json:"password,omitempty"`
		Domain   string                  `json:"domain,omitempty"`
		Policy   agentfiles.AccessPolicy `json:"policy,omitempty"`
	}
	shares := make([]envShare, 0, len(configs))
	for _, cfg := range configs {
		if !cfg.Enabled() {
			continue
		}
		shares = append(shares, envShare{
			ID:       strings.TrimSpace(cfg.ID),
			Name:     strings.TrimSpace(cfg.Name),
			Host:     strings.TrimSpace(cfg.Host),
			Share:    strings.TrimSpace(cfg.Share),
			Username: strings.TrimSpace(cfg.Username),
			Password: cfg.Password,
			Domain:   strings.TrimSpace(cfg.Domain),
			Policy:   cfg.Policy,
		})
	}
	if len(shares) > 0 {
		encoded, err := json.Marshal(shares)
		if err != nil {
			return err
		}
		env["HANK_SMB_SHARES_JSON"] = string(encoded)
	} else {
		delete(env, "HANK_SMB_SHARES_JSON")
	}
	return nil
}

func (m *configManager) persistLocalConfigs(configs []agentfiles.LocalConfig) error {
	if m.envPath == "" {
		return nil
	}
	env, err := m.readEnvFile()
	if err != nil {
		return err
	}
	if err := applyLocalConfigsToEnv(env, configs); err != nil {
		return err
	}
	return writeEnvFile(m.envPath, env)
}

func applyLocalConfigsToEnv(env map[string]string, configs []agentfiles.LocalConfig) error {
	// The single-root legacy key is consolidated into the JSON list.
	delete(env, "HANK_AGENT_FILES_ROOT")
	type envFolder struct {
		ID     string                  `json:"id,omitempty"`
		Name   string                  `json:"name,omitempty"`
		Root   string                  `json:"root"`
		Policy agentfiles.AccessPolicy `json:"policy,omitempty"`
	}
	folders := make([]envFolder, 0, len(configs))
	for _, cfg := range configs {
		if !cfg.Enabled() {
			continue
		}
		folders = append(folders, envFolder{
			ID:     strings.TrimSpace(cfg.ID),
			Name:   strings.TrimSpace(cfg.Name),
			Root:   strings.TrimSpace(cfg.Root),
			Policy: cfg.Policy,
		})
	}
	if len(folders) > 0 {
		encoded, err := json.Marshal(folders)
		if err != nil {
			return err
		}
		env["HANK_AGENT_FILES_ROOTS_JSON"] = string(encoded)
	} else {
		delete(env, "HANK_AGENT_FILES_ROOTS_JSON")
	}
	return nil
}

func (m *configManager) persistFileConfigs(smbConfigs []agentfiles.SMBConfig, localConfigs []agentfiles.LocalConfig, applyLocal bool) error {
	if m.envPath == "" {
		return nil
	}
	env, err := m.readEnvFile()
	if err != nil {
		return err
	}
	if err := applySMBConfigsToEnv(env, smbConfigs); err != nil {
		return err
	}
	if applyLocal {
		if err := applyLocalConfigsToEnv(env, localConfigs); err != nil {
			return err
		}
	}
	return writeEnvFile(m.envPath, env)
}

func (m *configManager) readEnvFile() (map[string]string, error) {
	env := map[string]string{}
	if m.envPath == "" {
		return env, nil
	}
	data, err := os.ReadFile(m.envPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return env, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(key)] = value
	}
	return env, nil
}

func writeEnvFile(path string, env map[string]string) error {
	return writeEnvFileWithRename(path, env, os.Rename)
}

func writeEnvFileWithRename(path string, env map[string]string, rename func(string, string) error) error {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		lines = append(lines, key+"="+env[key])
	}
	lines = append(lines, "")

	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temp.WriteString(strings.Join(lines, "\n")); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := rename(tempPath, path); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			data, readErr := os.ReadFile(tempPath)
			_ = os.Remove(tempPath)
			if readErr != nil {
				return readErr
			}
			file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
			if openErr != nil {
				return openErr
			}
			if chmodErr := file.Chmod(0o600); chmodErr != nil {
				_ = file.Close()
				return chmodErr
			}
			if _, writeErr := file.Write(data); writeErr != nil {
				_ = file.Close()
				return writeErr
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				return syncErr
			}
			return file.Close()
		}
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}
