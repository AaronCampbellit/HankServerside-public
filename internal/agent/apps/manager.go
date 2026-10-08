package apps

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const (
	appStdioProtocolVersion = "hank.app.stdio.v1"
	maxPackageBytes         = 32 << 20
	appStateFilename        = ".hank-app-state.json"
)

var (
	ErrUnknownApp            = errors.New("unknown app")
	ErrDisabledApp           = errors.New("disabled app")
	ErrMissingStagingPackage = errors.New("missing staging package")
	ErrPackageValidation     = errors.New("package validation failed")
	ErrPermissionRefused     = errors.New("permission refused")
	ErrUnknownCommand        = errors.New("unknown app command")
	ErrAppInvocationFailed   = errors.New("app invocation failed")
	stagingIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

type Manager struct {
	active         map[string]map[uint64]context.CancelFunc
	nextInvocation uint64
	mutationMu     sync.Mutex
	files          atomic.Pointer[agentfiles.Service]
	appsDir        string
	stagingDir     string
	runner         Runner
	mu             sync.RWMutex
	apps           map[string]*InstalledApp
	staged         map[string]PackagePreview
	agentID        string
	agentToken     string
	eventSink      EventSink
}

type EventSink func(ctx context.Context, event string, topic string, payload any) error

type InstalledApp struct {
	Grants       map[string]string
	Revision     int64
	Manifest     Manifest
	Path         string
	Enabled      bool
	PublicConfig json.RawMessage
	Secrets      json.RawMessage
	SecretSet    map[string]bool
	Status       string
	LastError    string
}

type persistedAppState struct {
	Enabled      bool            `json:"enabled"`
	PublicConfig json.RawMessage `json:"public_config,omitempty"`
	Secrets      json.RawMessage `json:"secrets,omitempty"`
	SecretSet    map[string]bool `json:"secret_fields_set,omitempty"`
	Status       string          `json:"status,omitempty"`
	LastError    string          `json:"last_error,omitempty"`
}

func NewManager(appsDir string, stagingDir string, runner Runner) *Manager {
	return &Manager{
		appsDir:    appsDir,
		stagingDir: stagingDir,
		runner:     runner,
		apps:       make(map[string]*InstalledApp),
		active:     make(map[string]map[uint64]context.CancelFunc),
		staged:     make(map[string]PackagePreview),
	}
}

func (m *Manager) SetPackageDownloadAuth(agentID string, token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agentID = strings.TrimSpace(agentID)
	m.agentToken = strings.TrimSpace(token)
}

func (m *Manager) SetEventSink(sink EventSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventSink = sink
}

func (m *Manager) Load(ctx context.Context) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()

	m.apps = make(map[string]*InstalledApp)
	if m.appsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.appsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var loadErrors []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		appPath, err := containedPath(m.appsDir, entry.Name())
		if err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("app %q could not be loaded", entry.Name()))
			continue
		}
		manifest, err := readInstalledManifest(appPath)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("app %q could not be loaded", entry.Name()))
			continue
		}
		state, err := readPersistedAppState(appPath)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("app %q could not be loaded", entry.Name()))
			continue
		}
		secretSet := secretSetDefaults(manifest)
		for field, set := range state.SecretSet {
			secretSet[field] = set
		}
		status := strings.TrimSpace(state.Status)
		if status == "" {
			status = "installed"
		}
		grants, err := readAppGrants(appPath)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("app %q could not be loaded", entry.Name()))
			continue
		}
		m.apps[manifest.ID] = &InstalledApp{
			Grants: grants, Revision: time.Now().UnixNano(),
			Manifest:     manifest,
			Path:         appPath,
			Enabled:      state.Enabled,
			PublicConfig: cloneRawMessage(state.PublicConfig),
			Secrets:      cloneRawMessage(state.Secrets),
			SecretSet:    secretSet,
			Status:       status,
			LastError:    strings.TrimSpace(state.LastError),
		}
	}
	return errors.Join(loadErrors...)
}

func (m *Manager) List(ctx context.Context) protocol.AppsListResponse {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	return protocol.AppsListResponse{Apps: m.summariesLocked("")}
}

func (m *Manager) PreviewPackage(ctx context.Context, request protocol.AppsPackagePreviewRequest) (protocol.AppsPackagePreviewResponse, error) {
	if m.stagingDir == "" {
		return protocol.AppsPackagePreviewResponse{}, fmt.Errorf("%w: app staging directory is not configured", ErrPermissionRefused)
	}
	stagingID := request.StagingID
	if stagingID == "" {
		stagingID = fmt.Sprintf("stage_%d", time.Now().UTC().UnixNano())
	}
	stagePath, err := m.stagingPath(stagingID)
	if err != nil {
		return protocol.AppsPackagePreviewResponse{}, err
	}
	if err := os.MkdirAll(m.stagingDir, 0o700); err != nil {
		return protocol.AppsPackagePreviewResponse{}, err
	}
	agentID, agentToken := m.packageDownloadAuth()
	if err := downloadPackage(ctx, request, stagePath, agentID, agentToken); err != nil {
		return protocol.AppsPackagePreviewResponse{}, err
	}
	preview, err := PreviewArchive(stagePath)
	if err != nil {
		return protocol.AppsPackagePreviewResponse{}, fmt.Errorf("%w: %v", ErrPackageValidation, err)
	}
	packageHash, err := fileSHA256Hex(stagePath)
	if err != nil {
		return protocol.AppsPackagePreviewResponse{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.staged[stagingID] = preview
	_, replacing := m.apps[preview.Manifest.ID]
	return protocol.AppsPackagePreviewResponse{
		StagingID:     stagingID,
		PackageSHA256: packageHash,
		App:           summaryFromPreview(preview),
		Warnings:      append([]string(nil), preview.Warnings...),
		Replacing:     replacing,
	}, nil
}

func (m *Manager) ActivatePackage(ctx context.Context, request protocol.AppsPackageActivateRequest) (protocol.AppsPackageActivateResponse, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	_ = ctx
	stagePath, err := m.stagingPath(request.StagingID)
	if err != nil {
		return protocol.AppsPackageActivateResponse{}, err
	}

	m.mu.RLock()
	preview, ok := m.staged[request.StagingID]
	m.mu.RUnlock()
	if _, err := os.Stat(stagePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return protocol.AppsPackageActivateResponse{}, fmt.Errorf("%w: %s", ErrMissingStagingPackage, request.StagingID)
		}
		return protocol.AppsPackageActivateResponse{}, err
	}
	currentPreview, err := PreviewArchive(stagePath)
	if err != nil {
		return protocol.AppsPackageActivateResponse{}, fmt.Errorf("%w: %v", ErrPackageValidation, err)
	}
	if ok && currentPreview.Manifest.ID != preview.Manifest.ID {
		return protocol.AppsPackageActivateResponse{}, fmt.Errorf("%w: staged app changed from %q to %q", ErrPackageValidation, preview.Manifest.ID, currentPreview.Manifest.ID)
	}
	if request.PackageSHA256 != "" {
		currentHash, err := fileSHA256Hex(stagePath)
		if err != nil {
			return protocol.AppsPackageActivateResponse{}, err
		}
		if !strings.EqualFold(currentHash, request.PackageSHA256) {
			return protocol.AppsPackageActivateResponse{}, fmt.Errorf("%w: package hash mismatch", ErrPackageValidation)
		}
	}
	preview = currentPreview
	if m.appsDir == "" {
		return protocol.AppsPackageActivateResponse{}, fmt.Errorf("%w: app install directory is not configured", ErrPermissionRefused)
	}

	m.mu.RLock()
	existing, replacing := m.apps[preview.Manifest.ID]
	var previous InstalledApp
	if replacing {
		previous = cloneInstalledApp(existing)
	}
	m.mu.RUnlock()

	secretSet := secretSetDefaults(preview.Manifest)
	var publicConfig json.RawMessage
	var secrets json.RawMessage
	if replacing {
		publicConfig = cloneRawMessage(previous.PublicConfig)
		secrets, secretSet, err = preserveDeclaredSecrets(previous, preview.Manifest)
		if err != nil {
			return protocol.AppsPackageActivateResponse{}, err
		}
	}

	appPath, err := installArchive(m.appsDir, preview.Manifest.ID, stagePath)
	if err != nil {
		return protocol.AppsPackageActivateResponse{}, err
	}
	installed := &InstalledApp{
		Grants: map[string]string{}, Revision: time.Now().UnixNano(),
		Manifest:     preview.Manifest,
		Path:         appPath,
		Enabled:      request.Enable,
		PublicConfig: publicConfig,
		Secrets:      secrets,
		SecretSet:    secretSet,
		Status:       "installed",
	}
	if err := writeAppGrants(*installed); err != nil {
		return protocol.AppsPackageActivateResponse{}, err
	}
	if err := writePersistedAppState(installed); err != nil {
		return protocol.AppsPackageActivateResponse{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelAppLocked(preview.Manifest.ID)
	m.apps[preview.Manifest.ID] = installed
	delete(m.staged, request.StagingID)
	summary := appSummary(installed)
	summary.Permissions, _ = m.permissionState(ctx, *installed)
	return protocol.AppsPackageActivateResponse{App: summary}, nil
}

func (m *Manager) Uninstall(ctx context.Context, request protocol.AppsUninstallRequest) (protocol.AppsUninstallResponse, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	_ = ctx
	appID := strings.TrimSpace(request.AppID)
	if !identifierPattern.MatchString(appID) {
		return protocol.AppsUninstallResponse{}, fmt.Errorf("%w: invalid app id %q", ErrPermissionRefused, appID)
	}
	if m.appsDir == "" {
		return protocol.AppsUninstallResponse{}, fmt.Errorf("%w: app install directory is not configured", ErrPermissionRefused)
	}
	appPath, err := containedPath(m.appsDir, appID)
	if err != nil {
		return protocol.AppsUninstallResponse{}, err
	}

	// Hold the manager lock through removal so no new invocation or config write
	// can observe the app after its on-disk state has been deleted. RemoveAll does
	// not follow symlinks, including symlinks nested inside the app directory.
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.RemoveAll(appPath); err != nil {
		return protocol.AppsUninstallResponse{}, err
	}
	if err := os.Remove(appGrantsPath(appPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return protocol.AppsUninstallResponse{}, err
	}
	m.cancelAppLocked(appID)
	delete(m.apps, appID)
	return protocol.AppsUninstallResponse{AppID: appID}, nil
}

func (m *Manager) ConfigStatus(ctx context.Context, request protocol.AppsConfigStatusRequest) (protocol.AppsConfigStatusResponse, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	if request.AppID != "" {
		if _, ok := m.apps[request.AppID]; !ok {
			return protocol.AppsConfigStatusResponse{}, fmt.Errorf("%w: %s", ErrUnknownApp, request.AppID)
		}
	}
	return protocol.AppsConfigStatusResponse{Apps: m.summariesLocked(request.AppID)}, nil
}

func (m *Manager) ConfigApply(ctx context.Context, request protocol.AppsConfigApplyRequest) (protocol.AppsConfigApplyResponse, error) {
	m.mu.Lock()
	app, ok := m.apps[request.AppID]
	if !ok {
		m.mu.Unlock()
		return protocol.AppsConfigApplyResponse{}, fmt.Errorf("%w: %s", ErrUnknownApp, request.AppID)
	}
	candidate := cloneInstalledApp(app)
	originalGrants := cloneGrants(candidate.Grants)
	m.mu.Unlock()

	if len(request.PublicConfig) > 0 {
		candidate.PublicConfig = cloneRawMessage(request.PublicConfig)
	}
	if len(request.Secrets) > 0 {
		if err := applySecrets(&candidate, request.Secrets); err != nil {
			return protocol.AppsConfigApplyResponse{}, err
		}
	}
	if request.Enable != nil {
		candidate.Enabled = *request.Enable
	}
	state, err := m.permissionState(ctx, candidate)
	if err != nil {
		return protocol.AppsConfigApplyResponse{}, err
	}
	if request.PermissionGrants != nil {
		if err := validateGrants(state, *request.PermissionGrants); err != nil {
			return protocol.AppsConfigApplyResponse{}, err
		}
		candidate.Grants = cloneGrants(*request.PermissionGrants)
	} else {
		for id, fp := range candidate.Grants {
			if validateGrants(state, map[string]string{id: fp}) != nil {
				delete(candidate.Grants, id)
			}
		}
	}
	revoked := false
	for id, fp := range originalGrants {
		if candidate.Grants[id] != fp {
			revoked = true
		}
	}
	if candidate.Enabled && !revoked {
		if err := m.validateSettingsApply(ctx, candidate, request.Secrets); err != nil {
			return protocol.AppsConfigApplyResponse{}, err
		}
	}

	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	app, ok = m.apps[request.AppID]
	if !ok {
		return protocol.AppsConfigApplyResponse{}, fmt.Errorf("%w: %s", ErrUnknownApp, request.AppID)
	}
	if app.Revision != candidate.Revision {
		return protocol.AppsConfigApplyResponse{}, ErrPermissionRefused
	}
	if err := writeAppGrants(candidate); err != nil {
		return protocol.AppsConfigApplyResponse{}, err
	}
	candidate.Revision = time.Now().UnixNano()
	candidate.LastError = ""
	if err := writePersistedAppState(&candidate); err != nil {
		return protocol.AppsConfigApplyResponse{}, err
	}
	m.cancelAppLocked(app.Manifest.ID)
	*app = cloneInstalledApp(&candidate)

	summary := appSummary(app)
	summary.Permissions, _ = m.permissionState(ctx, *app)
	return protocol.AppsConfigApplyResponse{App: summary}, nil
}

func (m *Manager) validateSettingsApply(ctx context.Context, app InstalledApp, requestSecrets json.RawMessage) error {
	command, ok := findCommand(app.Manifest, "settings_apply")
	if !ok {
		return nil
	}
	executable, err := containedPath(app.Path, app.Manifest.Runtime.Command)
	if err != nil {
		return err
	}
	input, err := settingsApplyInput(app, requestSecrets)
	if err != nil {
		return err
	}
	timeout := time.Duration(command.TimeoutSeconds) * time.Second
	state, err := m.permissionState(ctx, app)
	if err != nil {
		return err
	}
	response, err := m.invokeApp(ctx, app, InvokeSpec{
		PackageHash: state.PackageHash, Broker: m.brokerFor(app, state),
		Executable: executable,
		WorkDir:    app.Path,
		Timeout:    timeout,
		Request: AppStdioRequest{
			ProtocolVersion: appStdioProtocolVersion,
			RequestID:       fmt.Sprintf("%s.settings_apply.%d", app.Manifest.ID, time.Now().UTC().UnixNano()),
			AppID:           app.Manifest.ID,
			CommandID:       "settings_apply",
			Config:          app.PublicConfig,
			Secrets:         app.Secrets,
			Input:           input,
		},
	})
	if err != nil {
		return fmt.Errorf("%w: settings validation failed: %v", ErrAppInvocationFailed, err)
	}
	if len(response.Events) > 0 {
		return ErrPermissionRefused
	}
	if !response.OK {
		message := "settings validation failed"
		if response.Error != nil && strings.TrimSpace(response.Error.Message) != "" {
			message = response.Error.Message
		}
		return fmt.Errorf("%w: %s", ErrAppInvocationFailed, message)
	}
	return nil
}

func settingsApplyInput(app InstalledApp, requestSecrets json.RawMessage) (json.RawMessage, error) {
	input := map[string]any{
		"persist": false,
	}
	if len(app.PublicConfig) > 0 {
		var settings map[string]any
		if err := json.Unmarshal(app.PublicConfig, &settings); err != nil {
			return nil, fmt.Errorf("decode app settings: %w", err)
		}
		input["settings"] = settings
	} else {
		input["settings"] = map[string]any{}
	}
	if password, ok, err := plaintextSecret(requestSecrets, "password"); err != nil {
		return nil, err
	} else if ok {
		input["password"] = password
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode settings validation input: %w", err)
	}
	return encoded, nil
}

func (m *Manager) Invoke(ctx context.Context, request protocol.AppsInvokeRequest) (protocol.AppsInvokeResponse, error) {
	m.mu.RLock()
	app, ok := m.apps[request.AppID]
	if !ok {
		m.mu.RUnlock()
		return protocol.AppsInvokeResponse{}, fmt.Errorf("%w: %s", ErrUnknownApp, request.AppID)
	}
	snapshot := cloneInstalledApp(app)
	m.mu.RUnlock()

	if !snapshot.Enabled {
		return protocol.AppsInvokeResponse{}, fmt.Errorf("%w: %s", ErrDisabledApp, request.AppID)
	}
	command, ok := findCommand(snapshot.Manifest, request.CommandID)
	if !ok {
		return protocol.AppsInvokeResponse{}, fmt.Errorf("%w: %s.%s", ErrUnknownCommand, request.AppID, request.CommandID)
	}
	if (request.ActorRole != "admin" && request.ActorRole != "member") || (command.AdminOnly && request.ActorRole != "admin") {
		return protocol.AppsInvokeResponse{}, ErrPermissionRefused
	}
	executable, err := containedPath(snapshot.Path, snapshot.Manifest.Runtime.Command)
	if err != nil {
		return protocol.AppsInvokeResponse{}, err
	}
	timeout := time.Duration(command.TimeoutSeconds) * time.Second
	state, err := m.permissionState(ctx, snapshot)
	if err != nil {
		return protocol.AppsInvokeResponse{}, err
	}
	response, err := m.invokeApp(ctx, snapshot, InvokeSpec{
		PackageHash: state.PackageHash, Broker: m.brokerFor(snapshot, state),
		Executable: executable,
		WorkDir:    snapshot.Path,
		Timeout:    timeout,
		Request: AppStdioRequest{
			ProtocolVersion: appStdioProtocolVersion,
			RequestID:       fmt.Sprintf("%s.%s.%d", request.AppID, request.CommandID, time.Now().UTC().UnixNano()),
			AppID:           request.AppID,
			CommandID:       request.CommandID,
			Config:          snapshot.PublicConfig,
			Secrets:         snapshot.Secrets,
			Input:           request.Input,
			Context:         request.Context,
		},
	})
	if err != nil {
		m.setLastError(request.AppID, "App invocation failed")
		return protocol.AppsInvokeResponse{}, err
	}
	if len(response.Events) > 0 {
		return protocol.AppsInvokeResponse{}, ErrPermissionRefused
	}
	if !response.OK {
		message := "app returned an error"
		if response.Error != nil {
			message = response.Error.Message
		}
		m.setLastError(request.AppID, "App returned an error")
		return protocol.AppsInvokeResponse{}, fmt.Errorf("%w: %s", ErrAppInvocationFailed, message)
	}
	m.setLastError(request.AppID, "")
	return protocol.AppsInvokeResponse{Output: cloneRawMessage(response.Output)}, nil
}

func (m *Manager) Capabilities() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	capabilities := make([]string, 0)
	for _, app := range m.apps {
		if !app.Enabled {
			continue
		}
		for _, command := range app.Manifest.Commands {
			capabilities = append(capabilities, appCapability(app.Manifest.ID, command.ID))
		}
	}
	sort.Strings(capabilities)
	return capabilities
}

func (m *Manager) summariesLocked(appID string) []protocol.AppSummary {
	apps := make([]*InstalledApp, 0, len(m.apps))
	for id, app := range m.apps {
		if appID != "" && id != appID {
			continue
		}
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].Manifest.ID < apps[j].Manifest.ID
	})
	summaries := make([]protocol.AppSummary, 0, len(apps))
	for _, app := range apps {
		summary := appSummary(app)
		state, err := m.permissionState(context.Background(), *app)
		if err == nil {
			summary.Permissions = state
		} else {
			summary.LastError = "App permission targets could not be resolved"
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func (m *Manager) stagingPath(stagingID string) (string, error) {
	if stagingID == "" || !stagingIdentifierPattern.MatchString(stagingID) {
		return "", fmt.Errorf("%w: invalid staging id %q", ErrPermissionRefused, stagingID)
	}
	return containedPath(m.stagingDir, stagingID+".hankapp")
}

func (m *Manager) packageDownloadAuth() (string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agentID, m.agentToken
}

func (m *Manager) setLastError(appID string, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if app, ok := m.apps[appID]; ok {
		app.LastError = message
		_ = writePersistedAppState(app)
	}
}

func readInstalledManifest(appPath string) (Manifest, error) {
	file, err := os.Open(filepath.Join(appPath, "app.json"))
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	var manifest Manifest
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func downloadPackage(ctx context.Context, request protocol.AppsPackagePreviewRequest, destination string, agentID string, agentToken string) error {
	parsed, err := url.Parse(request.DownloadURL)
	if err != nil {
		return err
	}
	tempPath := destination + ".tmp"
	defer os.Remove(tempPath)

	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	switch parsed.Scheme {
	case "file":
		sourcePath := parsed.Path
		if sourcePath == "" {
			return fmt.Errorf("file download URL missing path")
		}
		source, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		defer source.Close()
		if err := copyPackageBytes(file, source); err != nil {
			return err
		}
	case "http", "https":
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, request.DownloadURL, nil)
		if err != nil {
			return err
		}
		if agentToken != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+agentToken)
		}
		if agentID != "" {
			httpRequest.Header.Set("X-Hank-Agent-ID", agentID)
		}
		if request.DownloadToken != "" {
			httpRequest.Header.Set("X-Hank-App-Package-Token", request.DownloadToken)
		}
		response, err := http.DefaultClient.Do(httpRequest)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("download package: unexpected status %s", response.Status)
		}
		if err := copyPackageBytes(file, response.Body); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported package URL scheme %q", parsed.Scheme)
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, destination)
}

func copyPackageBytes(destination io.Writer, source io.Reader) error {
	limited := io.LimitReader(source, maxPackageBytes+1)
	written, err := io.Copy(destination, limited)
	if err != nil {
		return err
	}
	if written > maxPackageBytes {
		return fmt.Errorf("app package exceeds %d bytes", maxPackageBytes)
	}
	return nil
}

func fileSHA256Hex(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func installArchive(appsDir string, appID string, archivePath string) (string, error) {
	if !identifierPattern.MatchString(appID) {
		return "", fmt.Errorf("%w: invalid app id %q", ErrPermissionRefused, appID)
	}
	if err := os.MkdirAll(appsDir, 0o700); err != nil {
		return "", err
	}
	finalPath, err := containedPath(appsDir, appID)
	if err != nil {
		return "", err
	}
	tempPath, err := os.MkdirTemp(appsDir, ".install-"+appID+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempPath)

	if err := extractArchive(archivePath, tempPath); err != nil {
		return "", err
	}
	if err := os.RemoveAll(finalPath); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return "", err
	}
	return finalPath, nil
}

func extractArchive(archivePath string, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()

	rootPrefix, err := archiveRootPrefix(reader.File)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPackageValidation, err)
	}
	for _, file := range reader.File {
		if file.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: archive contains symlink entry %q", ErrPackageValidation, file.Name)
		}
		cleaned, isDir, keep, err := normalizeArchivePath(file.Name, rootPrefix)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPackageValidation, err)
		}
		if !keep {
			continue
		}
		target, err := containedPath(destination, cleaned)
		if err != nil {
			return err
		}
		mode := file.FileInfo().Mode().Perm()
		if file.FileInfo().IsDir() || isDir {
			if err := os.MkdirAll(target, dirMode(mode)); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := extractFile(file, target, fileMode(mode)); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(file *zip.File, target string, mode os.FileMode) error {
	source, err := file.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return err
	}
	return destination.Close()
}

func containedPath(root string, relative string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: empty root", ErrPermissionRefused)
	}
	cleanedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(relative) || strings.Contains(relative, "\x00") {
		return "", fmt.Errorf("%w: unsafe path %q", ErrPermissionRefused, relative)
	}
	target := filepath.Join(cleanedRoot, filepath.FromSlash(relative))
	cleanedTarget := filepath.Clean(target)
	rel, err := filepath.Rel(cleanedRoot, cleanedTarget)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return "", fmt.Errorf("%w: path escapes root", ErrPermissionRefused)
	}
	return cleanedTarget, nil
}

func appSummary(app *InstalledApp) protocol.AppSummary {
	summary := summaryFromManifest(app.Manifest)
	summary.Enabled = app.Enabled
	summary.Status = app.Status
	summary.LastError = app.LastError
	summary.PublicConfig = cloneRawMessage(app.PublicConfig)
	summary.SecretFieldsSet = cloneSecretSet(app.SecretSet)
	return summary
}

func summaryFromPreview(preview PackagePreview) protocol.AppSummary {
	return summaryFromManifest(preview.Manifest)
}

func summaryFromManifest(manifest Manifest) protocol.AppSummary {
	summary := protocol.AppSummary{
		ID:              manifest.ID,
		Name:            manifest.Name,
		Version:         manifest.Version,
		Publisher:       manifest.Publisher,
		Description:     manifest.Description,
		Status:          "preview",
		Capabilities:    make([]string, 0, len(manifest.Commands)),
		SlashCommands:   make([]protocol.AppSlashCommand, 0, len(manifest.Assistant.SlashCommands)),
		Commands:        make([]protocol.AppCommandSummary, 0, len(manifest.Commands)),
		SettingsSchema:  manifest.Config.Settings,
		SecretFieldsSet: secretSetDefaults(manifest),
	}
	for _, command := range manifest.Commands {
		summary.Capabilities = append(summary.Capabilities, appCapability(manifest.ID, command.ID))
		summary.Commands = append(summary.Commands, protocol.AppCommandSummary{
			ID:             command.ID,
			Mode:           command.Mode,
			TimeoutSeconds: command.TimeoutSeconds,
			AdminOnly:      command.AdminOnly,
		})
	}
	for _, slashCommand := range manifest.Assistant.SlashCommands {
		summary.SlashCommands = append(summary.SlashCommands, protocol.AppSlashCommand{
			Command:     slashCommand.Command,
			CommandID:   slashCommand.CommandID,
			Description: slashCommand.Description,
		})
	}
	return summary
}

func appCapability(appID string, commandID string) string {
	return "apps." + appID + "." + commandID
}

func findCommand(manifest Manifest, commandID string) (Command, bool) {
	for _, command := range manifest.Commands {
		if command.ID == commandID {
			return command, true
		}
	}
	return Command{}, false
}

func cloneInstalledApp(app *InstalledApp) InstalledApp {
	return InstalledApp{
		Grants: cloneGrants(app.Grants), Revision: app.Revision,
		Manifest:     app.Manifest,
		Path:         app.Path,
		Enabled:      app.Enabled,
		PublicConfig: cloneRawMessage(app.PublicConfig),
		Secrets:      cloneRawMessage(app.Secrets),
		SecretSet:    cloneSecretSet(app.SecretSet),
		Status:       app.Status,
		LastError:    app.LastError,
	}
}

func readPersistedAppState(appPath string) (persistedAppState, error) {
	statePath, err := containedPath(appPath, appStateFilename)
	if err != nil {
		return persistedAppState{}, err
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return persistedAppState{}, nil
	}
	if err != nil {
		return persistedAppState{}, err
	}
	var state persistedAppState
	if err := json.Unmarshal(data, &state); err != nil {
		return persistedAppState{}, fmt.Errorf("read app state: %w", err)
	}
	return state, nil
}

func writePersistedAppState(app *InstalledApp) error {
	if app == nil || app.Path == "" {
		return nil
	}
	statePath, err := containedPath(app.Path, appStateFilename)
	if err != nil {
		return err
	}
	state := persistedAppState{
		Enabled:      app.Enabled,
		PublicConfig: cloneRawMessage(app.PublicConfig),
		Secrets:      cloneRawMessage(app.Secrets),
		SecretSet:    cloneSecretSet(app.SecretSet),
		Status:       app.Status,
		LastError:    app.LastError,
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(statePath, data, 0o600)
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func secretSetDefaults(manifest Manifest) map[string]bool {
	fields := make(map[string]bool, len(manifest.Config.SecretFields))
	for _, field := range manifest.Config.SecretFields {
		fields[field] = false
	}
	return fields
}

func cloneSecretSet(secretSet map[string]bool) map[string]bool {
	if len(secretSet) == 0 {
		return nil
	}
	clone := make(map[string]bool, len(secretSet))
	for key, value := range secretSet {
		clone[key] = value
	}
	return clone
}

func preserveDeclaredSecrets(existing InstalledApp, manifest Manifest) (json.RawMessage, map[string]bool, error) {
	stored, err := decodeSecretObject(existing.Secrets)
	if err != nil {
		return nil, nil, err
	}
	preserved := make(map[string]json.RawMessage, len(manifest.Config.SecretFields))
	secretSet := secretSetDefaults(manifest)
	for _, field := range manifest.Config.SecretFields {
		value, ok := stored[field]
		if !ok || isEmptySecretValue(value) {
			continue
		}
		preserved[field] = cloneRawMessage(value)
		secretSet[field] = true
	}
	encoded, err := encodeSecretObject(preserved)
	if err != nil {
		return nil, nil, err
	}
	return encoded, secretSet, nil
}

func applySecrets(app *InstalledApp, secrets json.RawMessage) error {
	values, err := decodeSecretObject(secrets)
	if err != nil {
		return err
	}
	stored, err := decodeSecretObject(app.Secrets)
	if err != nil {
		return err
	}
	for _, field := range app.Manifest.Config.SecretFields {
		raw, ok := values[field]
		if !ok || isEmptySecretValue(raw) {
			continue
		}
		stored[field] = cloneRawMessage(raw)
		if app.SecretSet != nil {
			app.SecretSet[field] = true
		}
	}
	encoded, err := encodeSecretObject(stored)
	if err != nil {
		return err
	}
	app.Secrets = encoded
	return nil
}

func decodeSecretObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return make(map[string]json.RawMessage), nil
	}
	values := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode app secrets: %w", err)
	}
	return values, nil
}

func encodeSecretObject(values map[string]json.RawMessage) (json.RawMessage, error) {
	if len(values) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode app secrets: %w", err)
	}
	return encoded, nil
}

func plaintextSecret(raw json.RawMessage, field string) (string, bool, error) {
	values, err := decodeSecretObject(raw)
	if err != nil {
		return "", false, err
	}
	value, ok := values[field]
	if !ok || isEmptySecretValue(value) {
		return "", false, nil
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", false, fmt.Errorf("decode app secret %q: %w", field, err)
	}
	return decoded, true, nil
}

func isEmptySecretValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == `""` {
		return true
	}
	return false
}

func dirMode(mode os.FileMode) os.FileMode {
	if mode == 0 {
		return 0o700
	}
	return mode
}

func fileMode(mode os.FileMode) os.FileMode {
	if mode == 0 {
		return 0o600
	}
	return mode
}
