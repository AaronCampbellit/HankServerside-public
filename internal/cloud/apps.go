package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	storepkg "github.com/dropfile/HankServerside/internal/store"
)

const (
	maxAppPackageBytes = 32 << 20
	appPackageTTL      = 15 * time.Minute
)

var (
	errAppInvokeBadPayload   = errors.New("invalid apps.invoke payload")
	errAppInvokeUnavailable  = errors.New("app is not installed, enabled, or command-capable")
	errAppInvokeAccessDenied = errors.New("app is not available to this home member")
)

type appPackageStagingRecord struct {
	HomeID        string
	StagingID     string
	DownloadToken string
	Bytes         []byte
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

type appPackageStagingRegistry struct {
	mu       sync.Mutex
	packages map[string]appPackageStagingRecord
}

func newAppPackageStagingRegistry() *appPackageStagingRegistry {
	return &appPackageStagingRegistry{packages: make(map[string]appPackageStagingRecord)}
}

func (r *appPackageStagingRegistry) Put(homeID string, data []byte) appPackageStagingRecord {
	now := time.Now().UTC()
	record := appPackageStagingRecord{
		HomeID:        homeID,
		StagingID:     newID("appstage"),
		DownloadToken: newToken(),
		Bytes:         append([]byte(nil), data...),
		CreatedAt:     now,
		ExpiresAt:     now.Add(appPackageTTL),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	r.packages[record.StagingID] = record
	return record
}

func (r *appPackageStagingRegistry) Get(stagingID string) (appPackageStagingRecord, bool) {
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	record, ok := r.packages[stagingID]
	if !ok || !record.ExpiresAt.After(now) {
		if ok {
			delete(r.packages, stagingID)
		}
		return appPackageStagingRecord{}, false
	}
	return record, true
}

func (r *appPackageStagingRegistry) Delete(stagingID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.packages, stagingID)
}

func (r *appPackageStagingRegistry) pruneLocked(now time.Time) {
	for stagingID, record := range r.packages {
		if !record.ExpiresAt.After(now) {
			delete(r.packages, stagingID)
		}
	}
}

func (s *Server) handleHomeAppPackageDownload(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if len(parts) != 3 || parts[0] != "apps" || parts[1] != "packages" || parts[2] == "" {
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	stagingID := strings.TrimSpace(parts[2])
	record, ok := s.appPackages.Get(stagingID)
	if !ok {
		http.NotFound(w, r)
		return true
	}
	if _, ok := s.authenticateAgentPackageDownload(r, record.HomeID); !ok {
		http.Error(w, "unauthorized agent", http.StatusUnauthorized)
		return true
	}
	token := strings.TrimSpace(r.Header.Get("X-Hank-App-Package-Token"))
	if token == "" || token != record.DownloadToken {
		http.Error(w, "unauthorized package download", http.StatusUnauthorized)
		return true
	}
	w.Header().Set("Content-Type", "application/vnd.hank.app-package")
	w.Header().Set("Content-Disposition", `attachment; filename="`+stagingID+`.hankapp"`)
	http.ServeContent(w, r, stagingID+".hankapp", record.CreatedAt, bytes.NewReader(record.Bytes))
	return true
}

func (s *Server) handleHomeApps(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 1 && parts[0] == "apps" && r.Method == http.MethodGet {
		if _, ok := s.router.GetAgent(home.ID); ok {
			envelope, err := s.sendAgentCommand(r.Context(), home.ID, protocol.CommandAppsList, protocol.AppsListRequest{})
			if err == nil && envelope.Error == nil {
				response, err := protocol.DecodePayload[protocol.AppsListResponse](envelope)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return true
				}
				for _, app := range response.Apps {
					if err := s.persistAgentApp(r, home.ID, auth.User.ID, app); err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return true
					}
				}
			}
		}
		apps, err := s.store.ListHomeApps(r.Context(), home.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		summaries, err := persistedAppSummaries(apps)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		summaries = filterAppSummariesForMembership(summaries, membership)
		writeJSON(w, http.StatusOK, protocol.AppsListResponse{Apps: nonNilSlice(summaries)})
		return true
	}

	if len(parts) == 3 && parts[0] == "apps" && parts[1] == "import" && parts[2] == "preview" && r.Method == http.MethodPost {
		if membership.Role != domain.HomeRoleAdmin {
			s.audit(r.Context(), "app_package.preview_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", "preview", map[string]any{"reason": "admin_required"})
			http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
			return true
		}
		if _, ok := s.router.GetAgent(home.ID); !ok {
			s.audit(r.Context(), "app_package.preview_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", "preview", map[string]any{"reason": "agent_offline"})
			writeJSON(w, http.StatusConflict, map[string]any{"error": "agent_offline"})
			return true
		}
		data, err := readAppPackageUpload(r)
		if err != nil {
			status := http.StatusBadRequest
			reason := "invalid_package_upload"
			if errors.Is(err, errAppPackageTooLarge) {
				status = http.StatusRequestEntityTooLarge
				reason = "package_too_large"
			}
			s.audit(r.Context(), "app_package.preview_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", "preview", map[string]any{"reason": reason})
			http.Error(w, err.Error(), status)
			return true
		}
		staged := s.appPackages.Put(home.ID, data)
		downloadURL := absoluteRequestURL(r, "/v1/home/apps/packages/"+staged.StagingID)
		envelope, err := s.sendAgentCommand(r.Context(), home.ID, protocol.CommandAppsPackagePreview, protocol.AppsPackagePreviewRequest{
			StagingID:     staged.StagingID,
			DownloadURL:   downloadURL,
			DownloadToken: staged.DownloadToken,
		})
		if err != nil {
			s.audit(r.Context(), "app_package.preview_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", staged.StagingID, map[string]any{"reason": "agent_command_failed"})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if envelope.Error != nil {
			s.audit(r.Context(), "app_package.preview_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", staged.StagingID, map[string]any{"reason": envelope.Error.Code})
			writeAppAgentError(w, "agent_preview", envelope.Error)
			return true
		}
		response, err := protocol.DecodePayload[protocol.AppsPackagePreviewResponse](envelope)
		if err != nil {
			s.audit(r.Context(), "app_package.preview_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", staged.StagingID, map[string]any{"reason": "agent_response_decode_failed"})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		s.audit(r.Context(), "app_package.previewed", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", staged.StagingID, map[string]any{"app_id": response.App.ID})
		writeJSON(w, http.StatusOK, response)
		return true
	}

	if len(parts) == 3 && parts[0] == "apps" && parts[1] == "import" && parts[2] == "activate" && r.Method == http.MethodPost {
		if membership.Role != domain.HomeRoleAdmin {
			s.audit(r.Context(), "app_package.activate_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", "activate", map[string]any{"reason": "admin_required"})
			http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
			return true
		}
		var body protocol.AppsPackageActivateRequest
		if err := parseJSON(w, r, &body); err != nil {
			s.audit(r.Context(), "app_package.activate_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", "activate", map[string]any{"reason": "invalid_request"})
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		envelope, err := s.sendAgentCommand(r.Context(), home.ID, protocol.CommandAppsPackageActivate, body)
		if err != nil {
			s.audit(r.Context(), "app_package.activate_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", body.StagingID, map[string]any{"reason": "agent_command_failed"})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if envelope.Error != nil {
			s.audit(r.Context(), "app_package.activate_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", body.StagingID, map[string]any{"reason": envelope.Error.Code})
			writeAppAgentError(w, "agent_activate", envelope.Error)
			return true
		}
		response, err := protocol.DecodePayload[protocol.AppsPackageActivateResponse](envelope)
		if err != nil {
			s.audit(r.Context(), "app_package.activate_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", body.StagingID, map[string]any{"reason": "agent_response_decode_failed"})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		response.App.UserAccess = domain.HomeAgentAppUserAccessAdminsOnly
		if err := s.persistAgentApp(r, home.ID, auth.User.ID, response.App); err != nil {
			s.audit(r.Context(), "app_package.activate_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app_package", body.StagingID, map[string]any{"reason": "persist_failed", "app_id": response.App.ID})
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		s.appPackages.Delete(body.StagingID)
		s.audit(r.Context(), "app_package.activated", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", response.App.ID, map[string]any{"staging_id": body.StagingID})
		writeJSON(w, http.StatusOK, response)
		return true
	}

	if len(parts) == 2 && parts[0] == "apps" && parts[1] != "" && r.Method == http.MethodDelete {
		appID := strings.TrimSpace(parts[1])
		if membership.Role != domain.HomeRoleAdmin {
			s.audit(r.Context(), "app.uninstall_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, map[string]any{"reason": "admin_required"})
			http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
			return true
		}
		if _, err := s.store.GetHomeApp(r.Context(), home.ID, appID); err != nil {
			if errors.Is(err, storepkg.ErrNotFound) {
				http.NotFound(w, r)
				return true
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		if _, ok := s.router.GetAgent(home.ID); !ok {
			s.audit(r.Context(), "app.uninstall_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, map[string]any{"reason": "agent_offline"})
			writeJSON(w, http.StatusConflict, map[string]any{"error": "agent_offline"})
			return true
		}
		envelope, err := s.sendAgentCommand(r.Context(), home.ID, protocol.CommandAppsUninstall, protocol.AppsUninstallRequest{AppID: appID})
		if err != nil {
			s.audit(r.Context(), "app.uninstall_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, map[string]any{"reason": "agent_command_failed"})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if envelope.Error != nil {
			s.audit(r.Context(), "app.uninstall_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, map[string]any{"reason": envelope.Error.Code})
			writeAppAgentError(w, "uninstall", envelope.Error)
			return true
		}
		response, err := protocol.DecodePayload[protocol.AppsUninstallResponse](envelope)
		if err != nil {
			s.audit(r.Context(), "app.uninstall_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, map[string]any{"reason": "agent_response_decode_failed"})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if response.AppID != appID {
			s.audit(r.Context(), "app.uninstall_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, map[string]any{"reason": "agent_response_mismatch"})
			http.Error(w, "agent returned a mismatched app id", http.StatusBadGateway)
			return true
		}
		if err := s.store.DeleteHomeApp(r.Context(), home.ID, appID); err != nil {
			s.audit(r.Context(), "app.uninstall_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, map[string]any{"reason": "persist_delete_failed"})
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		s.audit(r.Context(), "app.uninstalled", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", appID, nil)
		writeJSON(w, http.StatusOK, response)
		return true
	}

	if len(parts) == 3 && parts[0] == "apps" && parts[2] == "config" && r.Method == http.MethodPut {
		if membership.Role != domain.HomeRoleAdmin {
			http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
			return true
		}
		var body protocol.AppsConfigApplyRequest
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		body.AppID = parts[1]
		requestedUserAccess := ""
		if strings.TrimSpace(body.UserAccess) != "" {
			requestedUserAccess = normalizeAppUserAccess(body.UserAccess)
			if requestedUserAccess == "" {
				http.Error(w, "invalid app user_access", http.StatusBadRequest)
				return true
			}
			body.UserAccess = requestedUserAccess
		}
		envelope, err := s.sendAgentCommand(r.Context(), home.ID, protocol.CommandAppsConfigApply, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if envelope.Error != nil {
			writeAppAgentError(w, "config_apply", envelope.Error)
			return true
		}
		response, err := protocol.DecodePayload[protocol.AppsConfigApplyResponse](envelope)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if requestedUserAccess != "" {
			response.App.UserAccess = requestedUserAccess
		}
		if err := s.persistAgentApp(r, home.ID, auth.User.ID, response.App); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		if body.PermissionGrants != nil {
			s.audit(r.Context(), "app.permissions.updated", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "app", body.AppID, map[string]any{"grant_count": len(*body.PermissionGrants)})
		}
		writeJSON(w, http.StatusOK, response)
		return true
	}

	return false
}

func writeAppAgentError(w http.ResponseWriter, stage string, payload *protocol.ErrorPayload) {
	if payload == nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "agent_error",
			"message": "agent returned an error",
			"details": map[string]any{"stage": stage},
		})
		return
	}
	code := strings.TrimSpace(payload.Code)
	if code == "" {
		code = "agent_error"
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "agent returned an error"
	}
	details := map[string]any{"stage": stage}
	for key, value := range payload.Details {
		details[key] = value
	}
	writeJSON(w, statusForAppAgentError(code), map[string]any{
		"error":   code,
		"message": message,
		"details": details,
	})
}

func statusForAppAgentError(code string) int {
	switch code {
	case "app_package_invalid":
		return http.StatusBadRequest
	case "app_staging_missing", "app_staging_expired":
		return http.StatusGone
	case "app_not_found", "app_command_not_found":
		return http.StatusNotFound
	case "app_disabled":
		return http.StatusConflict
	case "app_permission_refused":
		return http.StatusForbidden
	case "app_invocation_failed":
		return http.StatusBadRequest
	case "app_invocation_timeout", "request_timeout":
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

func (s *Server) authenticateAgentPackageDownload(r *http.Request, homeID string) (domain.Agent, bool) {
	agentID := strings.TrimSpace(r.Header.Get("X-Hank-Agent-ID"))
	token, err := bearerToken(r.Header.Get("Authorization"))
	if agentID == "" || err != nil {
		return domain.Agent{}, false
	}
	record, err := s.store.ValidateAgentToken(r.Context(), hashToken(token))
	if err != nil || record.Agent.ID != agentID || record.Home.ID != homeID {
		return domain.Agent{}, false
	}
	return record.Agent, true
}

func (s *Server) persistAgentApp(r *http.Request, homeID string, updatedBy string, app protocol.AppSummary) error {
	now := time.Now().UTC()
	publicConfig := strings.TrimSpace(string(app.PublicConfig))
	if publicConfig == "" {
		publicConfig = "{}"
	}
	secretFieldsSet, err := marshalAppMetadataObject(app.SecretFieldsSet)
	if err != nil {
		return err
	}
	settingsSchema, err := marshalAppMetadataObject(app.SettingsSchema)
	if err != nil {
		return err
	}
	capabilities, err := marshalAppMetadataArray(app.Capabilities)
	if err != nil {
		return err
	}
	slashCommands, err := marshalAppMetadataArray(app.SlashCommands)
	if err != nil {
		return err
	}
	commands, err := marshalAppMetadataArray(app.Commands)
	if err != nil {
		return err
	}
	permissions, err := json.Marshal(app.Permissions)
	if err != nil {
		return err
	}
	userAccess := normalizeAppUserAccess(app.UserAccess)
	if userAccess == "" {
		if existing, err := s.store.GetHomeApp(r.Context(), homeID, app.ID); err == nil {
			userAccess = normalizeAppUserAccess(existing.UserAccess)
		} else if !errors.Is(err, storepkg.ErrNotFound) {
			return err
		}
	}
	if userAccess == "" {
		userAccess = domain.HomeAgentAppUserAccessAdminsOnly
	}
	status := strings.TrimSpace(app.Status)
	if status == "" {
		status = domain.SyncStatusPending
	}
	return s.store.UpsertHomeApp(r.Context(), domain.HomeAgentApp{
		HomeID:              homeID,
		AppID:               app.ID,
		Name:                app.Name,
		Version:             app.Version,
		Enabled:             app.Enabled,
		PublicConfigJSON:    publicConfig,
		SecretFieldsSetJSON: secretFieldsSet,
		SettingsSchemaJSON:  settingsSchema,
		CapabilitiesJSON:    capabilities,
		SlashCommandsJSON:   slashCommands,
		CommandsJSON:        commands,
		PermissionsJSON:     string(permissions),
		UserAccess:          userAccess,
		Status:              status,
		LastError:           app.LastError,
		UpdatedAt:           now,
		UpdatedBy:           updatedBy,
	})
}

func persistedAppSummaries(apps []domain.HomeAgentApp) ([]protocol.AppSummary, error) {
	summaries := make([]protocol.AppSummary, 0, len(apps))
	for _, app := range apps {
		summary := protocol.AppSummary{
			ID:           app.AppID,
			Name:         app.Name,
			Version:      app.Version,
			Enabled:      app.Enabled,
			Status:       app.Status,
			LastError:    app.LastError,
			UserAccess:   normalizeAppUserAccess(app.UserAccess),
			PublicConfig: json.RawMessage(defaultJSONObject(app.PublicConfigJSON)),
		}
		if err := unmarshalAppMetadata(defaultJSONObject(app.SecretFieldsSetJSON), &summary.SecretFieldsSet); err != nil {
			return nil, fmt.Errorf("decode %s secret fields: %w", app.AppID, err)
		}
		if err := unmarshalAppMetadata(defaultJSONObject(app.SettingsSchemaJSON), &summary.SettingsSchema); err != nil {
			return nil, fmt.Errorf("decode %s settings schema: %w", app.AppID, err)
		}
		if err := unmarshalAppMetadata(defaultJSONArray(app.CapabilitiesJSON), &summary.Capabilities); err != nil {
			return nil, fmt.Errorf("decode %s capabilities: %w", app.AppID, err)
		}
		if err := unmarshalAppMetadata(defaultJSONArray(app.SlashCommandsJSON), &summary.SlashCommands); err != nil {
			return nil, fmt.Errorf("decode %s slash commands: %w", app.AppID, err)
		}
		if err := unmarshalAppMetadata(defaultJSONArray(app.CommandsJSON), &summary.Commands); err != nil {
			return nil, fmt.Errorf("decode %s commands: %w", app.AppID, err)
		}
		if err := unmarshalAppMetadata(defaultJSONObject(app.PermissionsJSON), &summary.Permissions); err != nil {
			return nil, fmt.Errorf("decode %s permissions: %w", app.AppID, err)
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func filterAppSummariesForMembership(apps []protocol.AppSummary, membership domain.HomeMembership) []protocol.AppSummary {
	if membership.Role == domain.HomeRoleAdmin {
		return apps
	}
	filtered := make([]protocol.AppSummary, 0, len(apps))
	for _, app := range apps {
		if !app.Enabled {
			continue
		}
		if normalizeAppUserAccess(app.UserAccess) != domain.HomeAgentAppUserAccessHomeMembers {
			continue
		}
		filtered = append(filtered, app)
	}
	return filtered
}

func canUseHomeAgentApp(app domain.HomeAgentApp, membership domain.HomeMembership) bool {
	if !app.Enabled {
		return false
	}
	if membership.Role == domain.HomeRoleAdmin {
		return true
	}
	return normalizeAppUserAccess(app.UserAccess) == domain.HomeAgentAppUserAccessHomeMembers
}

func (s *Server) authorizeAppsInvokeCommand(ctx context.Context, homeID string, membership domain.HomeMembership, command protocol.RoutedCommand) error {
	if command.Command != protocol.CommandAppsInvoke {
		return nil
	}
	var request protocol.AppsInvokeRequest
	if err := json.Unmarshal(command.Body, &request); err != nil {
		return fmt.Errorf("%w: %v", errAppInvokeBadPayload, err)
	}
	if strings.TrimSpace(request.AppID) == "" || strings.TrimSpace(request.CommandID) == "" {
		return fmt.Errorf("%w: app_id and command_id are required", errAppInvokeBadPayload)
	}
	app, err := s.store.GetHomeApp(ctx, homeID, request.AppID)
	if errors.Is(err, storepkg.ErrNotFound) {
		return fmt.Errorf("%w: %s", errAppInvokeUnavailable, request.AppID)
	}
	if err != nil {
		return err
	}
	if !canUseHomeAgentApp(app, membership) {
		if !app.Enabled {
			return fmt.Errorf("%w: %s", errAppInvokeUnavailable, request.AppID)
		}
		return fmt.Errorf("%w: %s", errAppInvokeAccessDenied, request.AppID)
	}
	if !homeAgentAppHasCommand(app, request.CommandID) {
		return fmt.Errorf("%w: %s.%s", errAppInvokeUnavailable, request.AppID, request.CommandID)
	}
	if !canUseHomeAgentAppCommand(app, membership, request.CommandID) {
		return errAppInvokeAccessDenied
	}
	return nil
}

func canUseHomeAgentAppCommand(app domain.HomeAgentApp, membership domain.HomeMembership, commandID string) bool {
	if !canUseHomeAgentApp(app, membership) {
		return false
	}
	var commands []protocol.AppCommandSummary
	if json.Unmarshal([]byte(defaultJSONArray(app.CommandsJSON)), &commands) != nil {
		return false
	}
	for _, command := range commands {
		if command.ID == commandID {
			return !command.AdminOnly || membership.Role == domain.HomeRoleAdmin
		}
	}
	return false
}

func homeAgentAppHasCommand(app domain.HomeAgentApp, commandID string) bool {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return false
	}
	var commands []protocol.AppCommandSummary
	if err := json.Unmarshal([]byte(defaultJSONArray(app.CommandsJSON)), &commands); err != nil {
		return false
	}
	for _, command := range commands {
		if command.ID == commandID {
			return true
		}
	}
	return false
}

func normalizeAppUserAccess(value string) string {
	switch strings.TrimSpace(value) {
	case "", "admins_only":
		return domain.HomeAgentAppUserAccessAdminsOnly
	case "home_members":
		return domain.HomeAgentAppUserAccessHomeMembers
	default:
		return ""
	}
}

func marshalAppMetadataObject(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if string(raw) == "null" {
		return "{}", nil
	}
	return string(raw), nil
}

func marshalAppMetadataArray(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if string(raw) == "null" {
		return "[]", nil
	}
	return string(raw), nil
}

func defaultJSONObject(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "{}"
	}
	return value
}

func defaultJSONArray(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "[]"
	}
	return value
}

func unmarshalAppMetadata(raw string, target any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), target)
}

var errAppPackageTooLarge = errors.New("app package too large")

func readAppPackageUpload(r *http.Request) ([]byte, error) {
	contentType := r.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxAppPackageBytes); err != nil {
			return nil, err
		}
		file, _, err := r.FormFile("package")
		if err != nil {
			file, _, err = r.FormFile("file")
		}
		if err != nil {
			return nil, fmt.Errorf("package file is required")
		}
		defer file.Close()
		return readBoundedAppPackage(file)
	}
	return readBoundedAppPackage(r.Body)
}

func readBoundedAppPackage(reader io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	written, err := io.Copy(&buf, io.LimitReader(reader, maxAppPackageBytes+1))
	if err != nil {
		return nil, err
	}
	if written == 0 {
		return nil, fmt.Errorf("package body is required")
	}
	if written > maxAppPackageBytes {
		return nil, errAppPackageTooLarge
	}
	return buf.Bytes(), nil
}

func absoluteRequestURL(r *http.Request, path string) string {
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto == "http" || forwardedProto == "https" {
		scheme = forwardedProto
	}
	return scheme + "://" + host + path
}

type appActorContextKey struct{}

func stampAppInvocation(body json.RawMessage, homeID, userID, role string) (json.RawMessage, error) {
	var request protocol.AppsInvokeRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	contextFields := map[string]any{}
	if len(request.Context) > 0 {
		if err := json.Unmarshal(request.Context, &contextFields); err != nil {
			return nil, err
		}
		if contextFields == nil {
			contextFields = map[string]any{}
		}
	}
	request.ActorRole = role
	contextFields["home_id"] = homeID
	contextFields["user_id"] = userID
	contextFields["role"] = role
	request.Context, _ = json.Marshal(contextFields)
	return json.Marshal(request)
}
