package protocol

import "encoding/json"

const (
	CommandAppsList            = "apps.list"
	CommandAppsPackagePreview  = "apps.package_preview"
	CommandAppsPackageActivate = "apps.package_activate"
	CommandAppsUninstall       = "apps.uninstall"
	CommandAppsConfigStatus    = "apps.config_status"
	CommandAppsConfigApply     = "apps.config_apply"
	CommandAppsInvoke          = "apps.invoke"

	AppSchemaVersion = "hank.app.v1"
	AppRuntimeStdio  = "stdio"

	AppUserAccessAdminsOnly  = "admins_only"
	AppUserAccessHomeMembers = "home_members"
)

type AppSummary struct {
	Permissions     AppPermissionState  `json:"permissions"`
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Version         string              `json:"version"`
	Publisher       string              `json:"publisher,omitempty"`
	Description     string              `json:"description,omitempty"`
	Enabled         bool                `json:"enabled"`
	Status          string              `json:"status"`
	LastError       string              `json:"last_error,omitempty"`
	Capabilities    []string            `json:"capabilities,omitempty"`
	SlashCommands   []AppSlashCommand   `json:"slash_commands,omitempty"`
	Commands        []AppCommandSummary `json:"commands,omitempty"`
	SettingsSchema  AppSettingsSchema   `json:"settings_schema,omitempty"`
	UserAccess      string              `json:"user_access,omitempty"`
	PublicConfig    json.RawMessage     `json:"public_config,omitempty"`
	SecretFieldsSet map[string]bool     `json:"secret_fields_set,omitempty"`
}

type AppSlashCommand struct {
	Command     string `json:"command"`
	CommandID   string `json:"command_id"`
	Description string `json:"description,omitempty"`
}

type AppCommandSummary struct {
	ID             string `json:"id"`
	Mode           string `json:"mode"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	AdminOnly      bool   `json:"admin_only"`
}

type AppSettingsSchema struct {
	Fields []AppSettingsField `json:"fields,omitempty"`
}

type AppSettingsField struct {
	Key         string              `json:"key"`
	Label       string              `json:"label,omitempty"`
	Type        string              `json:"type"`
	Required    bool                `json:"required,omitempty"`
	Secret      bool                `json:"secret,omitempty"`
	SecretKey   string              `json:"secret_key,omitempty"`
	Source      string              `json:"source,omitempty"`
	SourceField string              `json:"source_field,omitempty"`
	Placeholder string              `json:"placeholder,omitempty"`
	Help        string              `json:"help,omitempty"`
	Default     json.RawMessage     `json:"default,omitempty"`
	Order       int                 `json:"order,omitempty"`
	Options     []AppSettingsOption `json:"options,omitempty"`
}

type AppSettingsOption struct {
	Value json.RawMessage `json:"value"`
	Label string          `json:"label,omitempty"`
}

type AppsListRequest struct{}

type AppsListResponse struct {
	Apps []AppSummary `json:"apps"`
}

type AppsPackagePreviewRequest struct {
	StagingID     string `json:"staging_id"`
	DownloadURL   string `json:"download_url"`
	DownloadToken string `json:"download_token"`
}

type AppsPackagePreviewResponse struct {
	StagingID     string     `json:"staging_id"`
	PackageSHA256 string     `json:"package_sha256,omitempty"`
	App           AppSummary `json:"app"`
	Warnings      []string   `json:"warnings,omitempty"`
	Replacing     bool       `json:"replacing"`
}

type AppsPackageActivateRequest struct {
	StagingID     string `json:"staging_id"`
	PackageSHA256 string `json:"package_sha256,omitempty"`
	Enable        bool   `json:"enable"`
}

type AppsPackageActivateResponse struct {
	App AppSummary `json:"app"`
}

type AppsUninstallRequest struct {
	AppID string `json:"app_id"`
}

type AppsUninstallResponse struct {
	AppID string `json:"app_id"`
}

type AppsConfigStatusRequest struct {
	AppID string `json:"app_id,omitempty"`
}

type AppsConfigStatusResponse struct {
	Apps []AppSummary `json:"apps"`
}

type AppsConfigApplyRequest struct {
	PermissionGrants *map[string]string `json:"permission_grants,omitempty"`
	AppID            string             `json:"app_id"`
	PublicConfig     json.RawMessage    `json:"public_config,omitempty"`
	Secrets          json.RawMessage    `json:"secrets,omitempty"`
	Enable           *bool              `json:"enable,omitempty"`
	UserAccess       string             `json:"user_access,omitempty"`
}

type AppsConfigApplyResponse struct {
	App AppSummary `json:"app"`
}

type AppsInvokeRequest struct {
	ActorRole string          `json:"actor_role,omitempty"`
	AppID     string          `json:"app_id"`
	CommandID string          `json:"command_id"`
	Input     json.RawMessage `json:"input,omitempty"`
	Context   json.RawMessage `json:"context,omitempty"`
}

type AppsInvokeResponse struct {
	Output json.RawMessage `json:"output,omitempty"`
	JobID  string          `json:"job_id,omitempty"`
}

// AppPermissionState is the resolved, reviewable scope on the owning agent.
// Grants map a permission ID to the exact target fingerprint reviewed by an admin.
type AppPermissionState struct {
	PackageHash string                `json:"package_hash,omitempty"`
	Targets     []AppPermissionTarget `json:"targets,omitempty"`
	Grants      map[string]string     `json:"grants,omitempty"`
	Sandbox     string                `json:"sandbox,omitempty"`
}
type AppPermissionTarget struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Field       string   `json:"field"`
	Target      string   `json:"target"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Addresses   []string `json:"addresses,omitempty"`
}

const CapabilityAppsSandboxV1 = "apps.sandbox.v1"

func RequiresAppSandbox(command string) bool {
	return command == CommandAppsInvoke || command == CommandAppsConfigApply || command == CommandAppsPackageActivate
}
