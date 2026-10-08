package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	recoveryProduct       = "hank"
	recoverySchemaVersion = 2
	recoverySecretKind    = "password"
)

type recoveryBundle struct {
	SchemaVersion   int                      `json:"schema_version"`
	ExportedAt      time.Time                `json:"exported_at,omitempty"`
	Product         string                   `json:"product"`
	Home            recoveryHome             `json:"home"`
	Settings        recoverySettings         `json:"settings"`
	ServiceProfiles []recoveryServiceProfile `json:"service_profiles"`
	EnvTemplates    recoveryEnvTemplates     `json:"env_templates"`
	Warnings        []string                 `json:"warnings,omitempty"`
}

type recoveryHome struct {
	Name string `json:"name"`
}

type recoverySettings struct {
	Profile    json.RawMessage     `json:"profile,omitempty"`
	Assistant  json.RawMessage     `json:"assistant,omitempty"`
	Storage    json.RawMessage     `json:"storage,omitempty"`
	QuickLinks []recoveryQuickLink `json:"quick_links,omitempty"`
	Dashboard  json.RawMessage     `json:"dashboard,omitempty"`
}

type recoveryQuickLink struct {
	ID                 string `json:"id,omitempty"`
	Title              string `json:"title"`
	URL                string `json:"url"`
	Description        string `json:"description,omitempty"`
	SortOrder          int    `json:"sort_order"`
	HealthCheckEnabled bool   `json:"health_check_enabled"`
}

type recoveryServiceProfile struct {
	ServiceType     string                   `json:"service_type"`
	PublicConfig    json.RawMessage          `json:"public_config"`
	RequiredSecrets []recoveryRequiredSecret `json:"required_secrets,omitempty"`
}

type recoveryRequiredSecret struct {
	ID          string         `json:"id"`
	Label       string         `json:"label"`
	Kind        string         `json:"kind"`
	ServiceType string         `json:"service_type"`
	Target      map[string]any `json:"target,omitempty"`
}

type recoveryEnvTemplates struct {
	Cloud map[string]string `json:"cloud"`
	Agent map[string]string `json:"agent"`
}

type recoveryImportPreview struct {
	Valid           bool                     `json:"valid"`
	Changes         []recoveryImportChange   `json:"changes"`
	RequiredSecrets []recoveryRequiredSecret `json:"required_secrets"`
	Warnings        []string                 `json:"warnings,omitempty"`
}

type recoveryImportChange struct {
	Area   string `json:"area"`
	Target string `json:"target"`
	Action string `json:"action"`
}

type recoveryImportApplyResult struct {
	Applied         bool                     `json:"applied"`
	Changes         []recoveryImportChange   `json:"changes"`
	RequiredSecrets []recoveryRequiredSecret `json:"required_secrets"`
}

func (s *Server) handleHomeRecovery(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) < 1 || parts[0] != "recovery" {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}

	switch {
	case len(parts) == 2 && parts[1] == "export" && r.Method == http.MethodGet:
		bundle, err := s.buildRecoveryBundle(r, home, auth.User.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		w.Header().Set("Content-Disposition", `attachment; filename="hank-recovery-settings.json"`)
		writeJSON(w, http.StatusOK, bundle)
		return true

	case len(parts) == 3 && parts[1] == "import" && parts[2] == "preview" && r.Method == http.MethodPost:
		var bundle recoveryBundle
		if err := parseJSON(w, r, &bundle); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		preview, err := s.previewRecoveryImport(r, home, bundle)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		writeJSON(w, http.StatusOK, preview)
		return true

	case len(parts) == 3 && parts[1] == "import" && parts[2] == "apply" && r.Method == http.MethodPost:
		var body struct {
			Bundle  recoveryBundle `json:"bundle"`
			Confirm bool           `json:"confirm"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		if !body.Confirm {
			http.Error(w, "import confirmation is required", http.StatusBadRequest)
			return true
		}
		result, err := s.applyRecoveryImport(r, home, auth.User.ID, body.Bundle)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		writeJSON(w, http.StatusOK, result)
		return true
	}

	http.NotFound(w, r)
	return true
}

func (s *Server) buildRecoveryBundle(r *http.Request, home domain.Home, userID string) (recoveryBundle, error) {
	bundle := recoveryBundle{
		SchemaVersion: recoverySchemaVersion,
		ExportedAt:    time.Now().UTC(),
		Product:       recoveryProduct,
		Home:          recoveryHome{Name: home.Name},
		EnvTemplates:  defaultRecoveryEnvTemplates(home.Name),
		Warnings:      []string{"Secrets are intentionally blank and must be re-entered during import or first setup."},
	}

	if profile, err := s.store.GetUserProfileSettings(r.Context(), userID); err == nil {
		bundle.Settings.Profile = cloneRecoveryRawJSON(profile.Settings)
	} else if !errors.Is(err, store.ErrNotFound) {
		return recoveryBundle{}, err
	}
	if assistant, err := s.currentAssistantSettings(r.Context(), home.ID, userID); err == nil {
		encoded, err := json.Marshal(assistantSettingsUpdateFromDomain(assistant))
		if err != nil {
			return recoveryBundle{}, err
		}
		bundle.Settings.Assistant = encoded
	} else if !errors.Is(err, store.ErrNotFound) {
		return recoveryBundle{}, err
	}
	if links, err := s.store.ListHomeQuickLinks(r.Context(), home.ID); err == nil {
		bundle.Settings.QuickLinks = recoveryQuickLinksFromDomain(links)
	} else {
		return recoveryBundle{}, err
	}
	if s.storage != nil {
		if cfg, err := s.storage.Config(); err == nil {
			if encoded, err := json.Marshal(redactJSONValue(cfg)); err == nil {
				bundle.Settings.Storage = encoded
			}
		}
	}

	profiles, err := s.store.ListHomeServiceProfiles(r.Context(), home.ID)
	if err != nil {
		return recoveryBundle{}, err
	}
	bundle.ServiceProfiles = make([]recoveryServiceProfile, 0, len(profiles))
	for _, profile := range profiles {
		publicConfig := redactedRawJSON(profile.PublicConfigJSON)
		recoveryProfile := recoveryServiceProfile{
			ServiceType:     profile.ServiceType,
			PublicConfig:    publicConfig,
			RequiredSecrets: requiredSecretsForServiceProfile(profile.ServiceType, publicConfig),
		}
		bundle.ServiceProfiles = append(bundle.ServiceProfiles, recoveryProfile)
	}
	sort.Slice(bundle.ServiceProfiles, func(i, j int) bool {
		return bundle.ServiceProfiles[i].ServiceType < bundle.ServiceProfiles[j].ServiceType
	})
	return bundle, nil
}

func (s *Server) previewRecoveryImport(r *http.Request, home domain.Home, bundle recoveryBundle) (recoveryImportPreview, error) {
	if err := validateRecoveryBundle(bundle); err != nil {
		return recoveryImportPreview{}, err
	}
	changes := recoveryChangesForBundle(home, bundle)
	requiredSecrets := requiredSecretsForBundle(bundle)
	return recoveryImportPreview{
		Valid:           true,
		Changes:         changes,
		RequiredSecrets: requiredSecrets,
		Warnings:        bundle.Warnings,
	}, nil
}

func (s *Server) applyRecoveryImport(r *http.Request, home domain.Home, userID string, bundle recoveryBundle) (recoveryImportApplyResult, error) {
	preview, err := s.previewRecoveryImport(r, home, bundle)
	if err != nil {
		return recoveryImportApplyResult{}, err
	}
	now := time.Now().UTC()
	if strings.TrimSpace(bundle.Home.Name) != "" && strings.TrimSpace(bundle.Home.Name) != home.Name {
		if _, err := s.store.RenameSingletonHome(r.Context(), home.ID, strings.TrimSpace(bundle.Home.Name)); err != nil {
			return recoveryImportApplyResult{}, err
		}
	}
	if len(bundle.Settings.Profile) > 0 {
		if _, err := s.store.SaveUserProfileSettings(r.Context(), userID, nil, bundle.Settings.Profile); err != nil {
			return recoveryImportApplyResult{}, err
		}
	}
	if len(bundle.Settings.Assistant) > 0 {
		current, err := s.currentAssistantSettings(r.Context(), home.ID, userID)
		if err != nil {
			return recoveryImportApplyResult{}, err
		}
		var request assistantSettingsUpdateRequest
		if err := json.Unmarshal(bundle.Settings.Assistant, &request); err != nil {
			return recoveryImportApplyResult{}, fmt.Errorf("assistant settings are invalid: %w", err)
		}
		updated, err := applyAssistantSettingsUpdate(current, request)
		if err != nil {
			return recoveryImportApplyResult{}, err
		}
		updated.HomeID = home.ID
		updated.UserID = userID
		updated.UpdatedAt = now
		updated.UpdatedBy = userID
		if updated.CreatedAt.IsZero() {
			updated.CreatedAt = now
		}
		if err := s.store.UpsertAssistantSettings(r.Context(), updated); err != nil {
			return recoveryImportApplyResult{}, err
		}
	}
	if err := s.applyRecoveryQuickLinks(r, home.ID, userID, bundle.Settings.QuickLinks); err != nil {
		return recoveryImportApplyResult{}, err
	}
	for _, profile := range bundle.ServiceProfiles {
		publicConfig := redactedRawJSON(string(profile.PublicConfig))
		status := domain.SyncStatusPending
		lastError := ""
		if len(requiredSecretsForServiceProfile(profile.ServiceType, publicConfig)) > 0 {
			lastError = "secret required"
		}
		if err := s.store.UpsertHomeServiceProfile(r.Context(), domain.HomeServiceProfile{
			HomeID:           home.ID,
			ServiceType:      profile.ServiceType,
			PublicConfigJSON: strings.TrimSpace(string(publicConfig)),
			SecretVersion:    0,
			AppliedVersion:   0,
			Status:           status,
			UpdatedAt:        now,
			UpdatedBy:        userID,
			LastError:        lastError,
		}); err != nil {
			return recoveryImportApplyResult{}, err
		}
	}
	return recoveryImportApplyResult{Applied: true, Changes: preview.Changes, RequiredSecrets: preview.RequiredSecrets}, nil
}

func (s *Server) applyRecoveryQuickLinks(r *http.Request, homeID string, userID string, links []recoveryQuickLink) error {
	for index, link := range links {
		if strings.TrimSpace(link.Title) == "" || strings.TrimSpace(link.URL) == "" {
			return fmt.Errorf("quick link title and url are required")
		}
		now := time.Now().UTC()
		id := strings.TrimSpace(link.ID)
		if id == "" {
			id = stableAssistantID("ql", link.Title+"|"+link.URL)
		}
		next := domain.HomeQuickLink{
			ID:                 id,
			HomeID:             homeID,
			Title:              strings.TrimSpace(link.Title),
			URL:                strings.TrimSpace(link.URL),
			Description:        strings.TrimSpace(link.Description),
			SortOrder:          link.SortOrder,
			HealthCheckEnabled: link.HealthCheckEnabled,
			Status:             domain.QuickLinkStatusUnchecked,
			CreatedAt:          now,
			UpdatedAt:          now,
			UpdatedBy:          userID,
		}
		if next.SortOrder == 0 {
			next.SortOrder = index * 10
		}
		existing, err := s.store.GetHomeQuickLink(r.Context(), homeID, id)
		if errors.Is(err, store.ErrNotFound) {
			if err := s.store.CreateHomeQuickLink(r.Context(), next); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		next.CreatedAt = existing.CreatedAt
		next.SortOrder = existing.SortOrder
		if _, err := s.store.UpdateHomeQuickLink(r.Context(), next); err != nil {
			return err
		}
	}
	return nil
}

func validateRecoveryBundle(bundle recoveryBundle) error {
	if bundle.Product != recoveryProduct {
		return fmt.Errorf("unsupported recovery product")
	}
	if bundle.SchemaVersion != recoverySchemaVersion {
		return fmt.Errorf("unsupported recovery schema version")
	}
	for _, profile := range bundle.ServiceProfiles {
		if profile.ServiceType != domain.ServiceTypeHomeAssistant && profile.ServiceType != domain.ServiceTypeSMB && profile.ServiceType != domain.ServiceTypeHermes {
			return fmt.Errorf("unsupported service type %q", profile.ServiceType)
		}
		if len(profile.PublicConfig) > 0 && !json.Valid(profile.PublicConfig) {
			return fmt.Errorf("public config for %s must be valid json", profile.ServiceType)
		}
	}
	if len(bundle.Settings.Profile) > 0 && !json.Valid(bundle.Settings.Profile) {
		return fmt.Errorf("profile settings must be valid json")
	}
	if len(bundle.Settings.Assistant) > 0 && !json.Valid(bundle.Settings.Assistant) {
		return fmt.Errorf("assistant settings must be valid json")
	}
	return nil
}

func recoveryChangesForBundle(home domain.Home, bundle recoveryBundle) []recoveryImportChange {
	var changes []recoveryImportChange
	if strings.TrimSpace(bundle.Home.Name) != "" {
		action := "unchanged"
		if strings.TrimSpace(bundle.Home.Name) != home.Name {
			action = "update"
		}
		changes = append(changes, recoveryImportChange{Area: "home", Target: "name", Action: action})
	}
	if len(bundle.Settings.Profile) > 0 {
		changes = append(changes, recoveryImportChange{Area: "settings", Target: "profile", Action: "update"})
	}
	if len(bundle.Settings.Assistant) > 0 {
		changes = append(changes, recoveryImportChange{Area: "settings", Target: "assistant", Action: "update"})
	}
	if len(bundle.Settings.QuickLinks) > 0 {
		changes = append(changes, recoveryImportChange{Area: "settings", Target: "quick_links", Action: "update"})
	}
	for _, profile := range bundle.ServiceProfiles {
		changes = append(changes, recoveryImportChange{Area: "service_profile", Target: profile.ServiceType, Action: "update"})
	}
	return changes
}

func requiredSecretsForBundle(bundle recoveryBundle) []recoveryRequiredSecret {
	var secrets []recoveryRequiredSecret
	for _, profile := range bundle.ServiceProfiles {
		publicConfig := redactedRawJSON(string(profile.PublicConfig))
		secrets = append(secrets, requiredSecretsForServiceProfile(profile.ServiceType, publicConfig)...)
	}
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].ID < secrets[j].ID })
	return secrets
}

func requiredSecretsForServiceProfile(serviceType string, publicConfig json.RawMessage) []recoveryRequiredSecret {
	switch serviceType {
	case domain.ServiceTypeHomeAssistant:
		if configHasAny(publicConfig, "base_url", "url") {
			return []recoveryRequiredSecret{{
				ID:          "homeassistant.token",
				Label:       "Home Assistant token",
				Kind:        recoverySecretKind,
				ServiceType: domain.ServiceTypeHomeAssistant,
				Target:      map[string]any{"field": "token"},
			}}
		}
	case domain.ServiceTypeSMB:
		return smbRequiredSecrets(publicConfig)
	case domain.ServiceTypeHermes:
		if configHasAny(publicConfig, "api_base_url") || configBool(publicConfig, "api_key_set") {
			return []recoveryRequiredSecret{{
				ID:          "hermes.api_key",
				Label:       "Hermes API key",
				Kind:        recoverySecretKind,
				ServiceType: domain.ServiceTypeHermes,
				Target:      map[string]any{"field": "api_key"},
			}}
		}
	}
	return nil
}

func smbRequiredSecrets(publicConfig json.RawMessage) []recoveryRequiredSecret {
	var config map[string]any
	if len(publicConfig) == 0 || json.Unmarshal(publicConfig, &config) != nil {
		return nil
	}
	rawShares, _ := config["shares"].([]any)
	if len(rawShares) == 0 {
		if configHasAny(publicConfig, "host", "share") {
			id := cleanRecoveryID(firstString(config["id"], config["source_id"], config["share"], "smb"))
			return []recoveryRequiredSecret{smbRequiredSecret(id, firstString(config["name"], config["share"], "SMB share"))}
		}
		return nil
	}
	secrets := make([]recoveryRequiredSecret, 0, len(rawShares))
	seen := map[string]struct{}{}
	for index, raw := range rawShares {
		share, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if firstString(share["host"], share["smb_host"]) == "" && firstString(share["share"], share["smb_share"]) == "" {
			continue
		}
		id := cleanRecoveryID(firstString(share["id"], share["source_id"], share["name"], share["share"], fmt.Sprintf("smb-%d", index+1)))
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		secrets = append(secrets, smbRequiredSecret(id, firstString(share["name"], share["share"], id)))
	}
	return secrets
}

func smbRequiredSecret(id string, label string) recoveryRequiredSecret {
	id = cleanRecoveryID(id)
	if id == "" {
		id = "smb"
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = id
	}
	return recoveryRequiredSecret{
		ID:          "smb." + id + ".password",
		Label:       label + " SMB password",
		Kind:        recoverySecretKind,
		ServiceType: domain.ServiceTypeSMB,
		Target:      map[string]any{"share_id": id, "field": "password"},
	}
}

func redactedRawJSON(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" || !json.Valid([]byte(raw)) {
		return json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(redactJSONValue(value))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func redactJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if recoveryKeyIsSecret(key) {
				continue
			}
			out[key] = redactJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactJSONValue(item)
		}
		return out
	default:
		return typed
	}
}

func recoveryKeyIsSecret(key string) bool {
	cleaned := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(cleaned, "password") ||
		strings.Contains(cleaned, "token") ||
		strings.Contains(cleaned, "api_key") ||
		strings.Contains(cleaned, "apikey") ||
		strings.Contains(cleaned, "secret") ||
		strings.Contains(cleaned, "cipher") ||
		strings.Contains(cleaned, "cookie") ||
		strings.Contains(cleaned, "bearer")
}

func assistantSettingsUpdateFromDomain(settings domain.AssistantSettings) assistantSettingsUpdateRequest {
	return assistantSettingsUpdateRequest{
		ProfileNotesEnabled:  boolPtr(settings.ProfileNotesEnabled),
		HomeNotesEnabled:     boolPtr(settings.HomeNotesEnabled),
		FilesEnabled:         boolPtr(settings.FilesEnabled),
		CalendarEnabled:      boolPtr(settings.CalendarEnabled),
		HomeAssistantEnabled: boolPtr(settings.HomeAssistantEnabled),
		ProjectDocsEnabled:   boolPtr(settings.ProjectDocsEnabled),
		ConversationsEnabled: boolPtr(settings.ConversationsEnabled),
		SystemPrompt:         recoveryStringPtr(settings.SystemPrompt),
		AIProvider:           recoveryStringPtr(settings.AIProvider),
		OllamaBaseURL:        recoveryStringPtr(settings.OllamaBaseURL),
		ChatModel:            recoveryStringPtr(settings.ChatModel),
		EmbeddingModel:       recoveryStringPtr(settings.EmbeddingModel),
		PromptProfile:        recoveryStringPtr(settings.PromptProfile),
		PlannerEnabled:       boolPtr(settings.PlannerEnabled),
		PlannerModel:         recoveryStringPtr(settings.PlannerModel),
	}
}

func recoveryQuickLinksFromDomain(links []domain.HomeQuickLink) []recoveryQuickLink {
	result := make([]recoveryQuickLink, 0, len(links))
	for _, link := range links {
		result = append(result, recoveryQuickLink{
			ID:                 link.ID,
			Title:              link.Title,
			URL:                link.URL,
			Description:        link.Description,
			SortOrder:          link.SortOrder,
			HealthCheckEnabled: link.HealthCheckEnabled,
		})
	}
	return result
}

func defaultRecoveryEnvTemplates(homeName string) recoveryEnvTemplates {
	return recoveryEnvTemplates{
		Cloud: map[string]string{
			"HANK_CLOUD_ADDR":              "127.0.0.1:18080",
			"HANK_CLOUD_DATABASE_URL":      "",
			"HANK_SECRET_ENCRYPTION_KEY":   "",
			"HANK_DB_OPS_INTENT_SECRET":    "",
			"HANK_DB_OPS_REPO_CIPHER_PASS": "",
		},
		Agent: map[string]string{
			"HANK_AGENT_CLOUD_URL": "",
			"HANK_AGENT_ID":        "",
			"HANK_AGENT_TOKEN":     "",
			"HANK_AGENT_HOME_NAME": strings.TrimSpace(homeName),
		},
	}
}

func configHasAny(raw json.RawMessage, keys ...string) bool {
	var config map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &config) != nil {
		return false
	}
	for _, key := range keys {
		if strings.TrimSpace(firstString(config[key])) != "" {
			return true
		}
	}
	return false
}

func configBool(raw json.RawMessage, key string) bool {
	var config map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &config) != nil {
		return false
	}
	value, _ := config[key].(bool)
	return value
}

func firstString(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		}
	}
	return ""
}

func cleanRecoveryID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, ch := range value {
		ok := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '.'
		if ok {
			builder.WriteRune(ch)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func cloneRecoveryRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func boolPtr(value bool) *bool {
	return &value
}

func recoveryStringPtr(value string) *string {
	return &value
}
