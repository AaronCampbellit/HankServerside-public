package domain

import (
	"encoding/json"
	"time"
)

const (
	AgentStatusOffline = "offline"
	AgentStatusOnline  = "online"

	HomeRoleAdmin  = "admin"
	HomeRoleMember = "member"
	HomeRoleOwner  = HomeRoleAdmin

	SyncStatusHealthy   = "healthy"
	SyncStatusDegraded  = "degraded"
	SyncStatusOutOfSync = "out_of_sync"
	SyncStatusOffline   = "offline"
	SyncStatusPending   = "pending"

	ServiceTypeHomeAssistant = "homeassistant"
	ServiceTypeSMB           = "smb"
	ServiceTypeHermes        = "hermes"

	HomePermissionFeatureHomeAssistant = "homeassistant"
	HomePermissionFeatureFiles         = "files"
	HomePermissionFeatureNotes         = "notes"
)

const (
	QuickLinkStatusUnchecked = "unchecked"
	QuickLinkStatusUp        = "up"
	QuickLinkStatusDown      = "down"
	QuickLinkStatusDisabled  = "disabled"
)

type User struct {
	ID                     string     `json:"id"`
	Email                  string     `json:"email"`
	DisplayName            string     `json:"display_name"`
	PasswordHash           string     `json:"-"`
	PasswordLoginEnabled   bool       `json:"password_login_enabled"`
	PasswordChangeRequired bool       `json:"password_change_required"`
	PasswordChangedAt      *time.Time `json:"password_changed_at,omitempty"`
	PasswordResetAt        *time.Time `json:"password_reset_at,omitempty"`
	PasswordResetBy        string     `json:"password_reset_by,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type ExternalIdentity struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Provider  string    `json:"provider"`
	TenantID  string    `json:"tenant_id"`
	SubjectID string    `json:"subject_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EntraSSOSettings struct {
	HomeID        string    `json:"home_id"`
	Enabled       bool      `json:"enabled"`
	TenantID      string    `json:"tenant_id"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"-"`
	PublicBaseURL string    `json:"public_base_url"`
	UpdatedAt     time.Time `json:"updated_at"`
	UpdatedBy     string    `json:"updated_by"`
}

type Home struct {
	ID                    string    `json:"id"`
	UserID                string    `json:"user_id"`
	Name                  string    `json:"name"`
	AgentEnrollmentPolicy string    `json:"agent_enrollment_policy,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type HomeMembership struct {
	HomeID    string    `json:"home_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type HomeMember struct {
	UserID      string    `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type HomeInvitation struct {
	ID         string     `json:"id"`
	HomeID     string     `json:"home_id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	TokenHash  string     `json:"-"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Agent struct {
	ID               string     `json:"id"`
	HomeID           string     `json:"home_id"`
	Name             string     `json:"name"`
	Status           string     `json:"status"`
	AgentType        string     `json:"agent_type,omitempty"`
	InstallationID   string     `json:"installation_id,omitempty"`
	EnrolledByUserID string     `json:"enrolled_by_user_id,omitempty"`
	Platform         string     `json:"platform,omitempty"`
	Architecture     string     `json:"architecture,omitempty"`
	AppVersion       string     `json:"app_version,omitempty"`
	InstallationMode string     `json:"installation_mode,omitempty"`
	Capabilities     []string   `json:"capabilities,omitempty"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type LinuxAgentRelease struct {
	ID             string     `json:"id"`
	Version        string     `json:"version"`
	ManifestSHA256 string     `json:"manifest_sha256"`
	SourceCommit   string     `json:"source_commit"`
	ManifestURL    string     `json:"manifest_url"`
	State          string     `json:"state"`
	CreatedAt      time.Time  `json:"created_at"`
	ActivatedAt    *time.Time `json:"activated_at,omitempty"`
}

type LinuxAgentRollout struct {
	ID          string     `json:"id"`
	ReleaseID   string     `json:"release_id"`
	Version     string     `json:"version"`
	State       string     `json:"state"`
	Scope       string     `json:"scope"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type LinuxAgentUpdateAssignment struct {
	ID          string    `json:"id"`
	RolloutID   string    `json:"rollout_id"`
	HomeID      string    `json:"home_id"`
	AgentID     string    `json:"agent_id"`
	FromVersion string    `json:"from_version,omitempty"`
	ToVersion   string    `json:"to_version"`
	State       string    `json:"state"`
	ErrorCode   string    `json:"error_code,omitempty"`
	NotBefore   time.Time `json:"not_before"`
	Attempt     int       `json:"attempt"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type LinuxAgentVersionPin struct {
	HomeID    string    `json:"home_id"`
	AgentID   string    `json:"agent_id"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentToken struct {
	ID                  string     `json:"id"`
	HomeID              string     `json:"home_id"`
	AgentID             string     `json:"agent_id"`
	TokenHash           string     `json:"-"`
	Generation          int        `json:"generation,omitempty"`
	ReplacesTokenID     string     `json:"replaces_token_id,omitempty"`
	ActivatedAt         *time.Time `json:"activated_at,omitempty"`
	ConfirmedAt         *time.Time `json:"confirmed_at,omitempty"`
	RotationRequestedAt *time.Time `json:"rotation_requested_at,omitempty"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

type AgentEnrollment struct {
	ID                 string            `json:"id"`
	HomeID             string            `json:"home_id"`
	Platform           string            `json:"platform"`
	TokenHash          string            `json:"-"`
	CreatedByUserID    string            `json:"created_by_user_id"`
	NameHint           string            `json:"name_hint,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	ExpiresAt          time.Time         `json:"expires_at"`
	DownloadedAt       *time.Time        `json:"downloaded_at,omitempty"`
	ConsumedAt         *time.Time        `json:"consumed_at,omitempty"`
	RevokedAt          *time.Time        `json:"revoked_at,omitempty"`
	AgentID            string            `json:"agent_id,omitempty"`
	DownloadCount      int               `json:"download_count"`
	FailedAttemptCount int               `json:"failed_attempt_count"`
}

type AgentCredentialState struct {
	CredentialID         string     `json:"credential_id"`
	AgentID              string     `json:"agent_id"`
	Generation           int        `json:"generation"`
	ReplacesCredentialID string     `json:"replaces_credential_id,omitempty"`
	ActivatedAt          time.Time  `json:"activated_at"`
	ConfirmedAt          *time.Time `json:"confirmed_at,omitempty"`
	ConfirmBy            *time.Time `json:"confirm_by,omitempty"`
	RotationRequestedAt  *time.Time `json:"rotation_requested_at,omitempty"`
	RotationRequested    bool       `json:"rotation_requested"`
	RotationDueAt        time.Time  `json:"rotation_due_at"`
	RotationDue          bool       `json:"rotation_due"`
}

type AppSession struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	TokenHash string     `json:"-"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

const (
	NotesAPIScopeRead   = "notes:read"
	NotesAPIScopeAppend = "notes:append"
	NotesAPIScopeWrite  = "notes:write"
	NotesAPIScopeDelete = "notes:delete"
)

type NotesAPIToken struct {
	ID                    string     `json:"id"`
	HomeID                string     `json:"home_id"`
	UserID                string     `json:"user_id"`
	Name                  string     `json:"name"`
	TokenHash             string     `json:"-"`
	Scopes                []string   `json:"scopes"`
	AllowHomeNotes        bool       `json:"allow_home_notes"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	RevokedAt             *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt            *time.Time `json:"last_used_at,omitempty"`
	LastUsedRoute         string     `json:"last_used_route,omitempty"`
	LastUsedIPHash        string     `json:"last_used_ip_hash,omitempty"`
	LastUsedUserAgentHash string     `json:"last_used_user_agent_hash,omitempty"`
	RequestCount          int64      `json:"request_count"`
	CreatedAt             time.Time  `json:"created_at"`
	CreatedBy             string     `json:"created_by"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

const (
	NotificationCategoryMonitoring        = "monitoring"
	NotificationCategoryAgentHealth       = "agent_health"
	NotificationCategoryQuickLinks        = "quick_links"
	NotificationCategoryStorage           = "storage"
	NotificationCategoryNotes             = "notes"
	NotificationCategoryDashboardEntities = "dashboard_entities"
)

func ValidNotificationCategory(category string) bool {
	switch category {
	case NotificationCategoryMonitoring, NotificationCategoryAgentHealth,
		NotificationCategoryQuickLinks,
		NotificationCategoryStorage,
		NotificationCategoryNotes,
		NotificationCategoryDashboardEntities:
		return true
	default:
		return false
	}
}

type NotificationSettings struct {
	MonitoringEnabled        bool      `json:"monitoring"`
	UserID                   string    `json:"user_id"`
	AgentHealthEnabled       bool      `json:"agent_health"`
	QuickLinksEnabled        bool      `json:"quick_links"`
	StorageEnabled           bool      `json:"storage"`
	NotesEnabled             bool      `json:"notes"`
	DashboardEntitiesEnabled bool      `json:"dashboard_entities"`
	UpdatedAt                time.Time `json:"updated_at"`
}

type UserNotification struct {
	ID              string     `json:"id"`
	UserID          string     `json:"user_id"`
	HomeID          string     `json:"home_id"`
	AuditEventID    string     `json:"audit_event_id,omitempty"`
	Category        string     `json:"category"`
	EventKind       string     `json:"event_kind"`
	Severity        string     `json:"severity"`
	Title           string     `json:"title"`
	Body            string     `json:"body"`
	TargetPath      string     `json:"target_path"`
	CollapseKey     string     `json:"collapse_key"`
	Outcome         string     `json:"outcome"`
	OccurrenceCount int        `json:"occurrence_count"`
	FirstOccurredAt time.Time  `json:"first_occurred_at"`
	LastOccurredAt  time.Time  `json:"last_occurred_at"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type NotificationListOptions struct {
	Cursor string
	Limit  int
	Unread bool
}

type NotificationPage struct {
	Items       []UserNotification `json:"items"`
	NextCursor  string             `json:"next_cursor,omitempty"`
	UnreadCount int                `json:"unread_count"`
}

type CreateUserNotificationInput struct {
	Notification            UserNotification
	EventKey                string
	DeliverySubscriptionIDs []string
}

type NotificationSourceObservation struct {
	SourceKey             string
	HomeID                string
	ResourceID            string
	State                 string
	EventKind             string
	Outcome               string
	Severity              string
	DisplayName           string
	OccurredAt            time.Time
	NotifyInitial         bool
	AllowedPreviousStates []string
}

type PendingNotificationSourceEvent struct {
	ID          string
	EventKey    string
	SourceKey   string
	HomeID      string
	ResourceID  string
	EventKind   string
	Outcome     string
	Severity    string
	DisplayName string
	OccurredAt  time.Time
}

type WebPushSubscription struct {
	ID                  string     `json:"id"`
	UserID              string     `json:"user_id"`
	SessionID           string     `json:"session_id"`
	Endpoint            string     `json:"endpoint,omitempty"`
	P256DH              string     `json:"p256dh,omitempty"`
	Auth                string     `json:"auth,omitempty"`
	EndpointFingerprint string     `json:"-"`
	BrowserLabel        string     `json:"browser_label"`
	CreatedAt           time.Time  `json:"created_at"`
	RefreshedAt         time.Time  `json:"refreshed_at"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	LastFailureCode     string     `json:"last_failure_code,omitempty"`
}

type WebPushDelivery struct {
	ID              string     `json:"id"`
	NotificationID  string     `json:"notification_id"`
	SubscriptionID  string     `json:"subscription_id"`
	State           string     `json:"state"`
	AttemptCount    int        `json:"attempt_count"`
	NextAttemptAt   time.Time  `json:"next_attempt_at"`
	ClaimOwner      string     `json:"claim_owner,omitempty"`
	ClaimExpiresAt  *time.Time `json:"claim_expires_at,omitempty"`
	LastOutcomeCode string     `json:"last_outcome_code,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeliveredAt     *time.Time `json:"delivered_at,omitempty"`
}

const WebPushDeliveryMaxAge = 15 * time.Minute

type WebPushDeliveryContext struct {
	Delivery        WebPushDelivery
	Notification    UserNotification
	Subscription    WebPushSubscription
	SessionActive   bool
	CategoryEnabled bool
}

type APNSDevice struct {
	UserID            string          `json:"user_id"`
	SessionID         string          `json:"session_id"`
	DeviceID          string          `json:"device_id"`
	Token             string          `json:"token"`
	Environment       string          `json:"environment"`
	BundleID          string          `json:"bundle_id"`
	EnabledCategories json.RawMessage `json:"enabled_categories"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	LastRegisteredAt  time.Time       `json:"last_registered_at"`
}

type UserProfileSettings struct {
	UserID    string          `json:"user_id"`
	Revision  int             `json:"revision"`
	Settings  json.RawMessage `json:"settings"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type UserProfileSecretVault struct {
	UserID    string          `json:"user_id"`
	Revision  int             `json:"revision"`
	KeyID     string          `json:"key_id"`
	Vault     json.RawMessage `json:"vault"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type HomeNoteSyncState struct {
	HomeID               string     `json:"home_id"`
	AgentID              string     `json:"agent_id"`
	LastManifestAt       *time.Time `json:"last_manifest_at,omitempty"`
	LastPullAt           *time.Time `json:"last_pull_at,omitempty"`
	LastPushAt           *time.Time `json:"last_push_at,omitempty"`
	Status               string     `json:"status"`
	LastError            string     `json:"last_error,omitempty"`
	PendingPullCount     int        `json:"pending_pull_count"`
	PendingPushCount     int        `json:"pending_push_count"`
	LastSuccessfulSyncAt *time.Time `json:"last_successful_sync_at,omitempty"`
}

type HomeServiceProfile struct {
	HomeID           string     `json:"home_id"`
	ServiceType      string     `json:"service_type"`
	PublicConfigJSON string     `json:"public_config_json,omitempty"`
	SecretVersion    int        `json:"secret_version"`
	AppliedVersion   int        `json:"applied_version"`
	Status           string     `json:"status"`
	UpdatedAt        time.Time  `json:"updated_at"`
	UpdatedBy        string     `json:"updated_by"`
	LastBackupAt     *time.Time `json:"last_backup_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

type HomeAgentApp struct {
	PermissionsJSON     string    `json:"permissions_json,omitempty"`
	HomeID              string    `json:"home_id"`
	AppID               string    `json:"app_id"`
	Name                string    `json:"name"`
	Version             string    `json:"version"`
	Enabled             bool      `json:"enabled"`
	PublicConfigJSON    string    `json:"public_config_json,omitempty"`
	SecretFieldsSetJSON string    `json:"secret_fields_set_json,omitempty"`
	SettingsSchemaJSON  string    `json:"settings_schema_json,omitempty"`
	CapabilitiesJSON    string    `json:"capabilities_json,omitempty"`
	SlashCommandsJSON   string    `json:"slash_commands_json,omitempty"`
	CommandsJSON        string    `json:"commands_json,omitempty"`
	UserAccess          string    `json:"user_access,omitempty"`
	Status              string    `json:"status"`
	LastError           string    `json:"last_error,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
	UpdatedBy           string    `json:"updated_by"`
}

const (
	HomeAgentAppUserAccessAdminsOnly  = "admins_only"
	HomeAgentAppUserAccessHomeMembers = "home_members"
)

type HomeQuickLink struct {
	ID                 string     `json:"id"`
	HomeID             string     `json:"home_id"`
	Title              string     `json:"title"`
	URL                string     `json:"url"`
	Description        string     `json:"description,omitempty"`
	SortOrder          int        `json:"sort_order"`
	HealthCheckEnabled bool       `json:"health_check_enabled"`
	Status             string     `json:"status"`
	StatusCode         int        `json:"status_code"`
	LastCheckedAt      *time.Time `json:"last_checked_at,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	UpdatedBy          string     `json:"updated_by"`
}

type HomePermissions struct {
	HomeID               string    `json:"home_id"`
	HomeAssistantEnabled bool      `json:"homeassistant"`
	FilesEnabled         bool      `json:"files"`
	NotesEnabled         bool      `json:"notes"`
	UpdatedAt            time.Time `json:"updated_at"`
	UpdatedBy            string    `json:"updated_by"`
}

type HomeMemberPermissions struct {
	HomeID               string    `json:"home_id"`
	UserID               string    `json:"user_id"`
	HomeAssistantEnabled *bool     `json:"homeassistant,omitempty"`
	FilesEnabled         *bool     `json:"files,omitempty"`
	NotesEnabled         *bool     `json:"notes,omitempty"`
	UpdatedAt            time.Time `json:"updated_at"`
	UpdatedBy            string    `json:"updated_by"`
}

type UserProfileBackup struct {
	UserID    string          `json:"user_id"`
	Revision  int             `json:"revision"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type AssistantSession struct {
	ID            string    `json:"id"`
	HomeID        string    `json:"home_id"`
	UserID        string    `json:"user_id"`
	Title         string    `json:"title"`
	LastMessageAt time.Time `json:"last_message_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type AssistantMessage struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	Role        string    `json:"role"`
	Status      string    `json:"status"`
	ContentJSON string    `json:"content_json"`
	ModelName   string    `json:"model_name"`
	CreatedAt   time.Time `json:"created_at"`
}

type AssistantRun struct {
	ID                   string     `json:"id"`
	SessionID            string     `json:"session_id"`
	MessageID            string     `json:"message_id"`
	State                string     `json:"state"`
	RequiresClientTools  bool       `json:"requires_client_tools"`
	RequiresConfirmation bool       `json:"requires_confirmation"`
	PendingActionJSON    string     `json:"pending_action_json"`
	CreatedAt            time.Time  `json:"created_at"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
}

type AssistantAttachment struct {
	ID                 string     `json:"id"`
	SessionID          string     `json:"session_id"`
	UserID             string     `json:"user_id"`
	ClientAttachmentID string     `json:"client_attachment_id"`
	Filename           string     `json:"filename"`
	ContentType        string     `json:"content_type"`
	Kind               string     `json:"kind"`
	SizeBytes          int64      `json:"size_bytes"`
	ChecksumSHA256     string     `json:"checksum_sha256"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CommittedAt        *time.Time `json:"committed_at,omitempty"`
}

type NoteAttachment struct {
	ID                    string     `json:"id"`
	NoteID                string     `json:"note_id"`
	HomeID                string     `json:"home_id,omitempty"`
	OwnerUserID           string     `json:"owner_user_id"`
	Filename              string     `json:"filename"`
	ContentType           string     `json:"content_type"`
	SizeBytes             int64      `json:"size_bytes"`
	ChecksumSHA256        string     `json:"checksum_sha256"`
	StorageKey            string     `json:"storage_key"`
	PreviewStorageKey     string     `json:"preview_storage_key,omitempty"`
	PreviewContentType    string     `json:"preview_content_type,omitempty"`
	PreviewSizeBytes      int64      `json:"preview_size_bytes,omitempty"`
	PreviewChecksumSHA256 string     `json:"preview_checksum_sha256,omitempty"`
	DeletedAt             *time.Time `json:"deleted_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type MCPNoteAttachmentUpload struct {
	ID                      string     `json:"id"`
	OwnerUserID             string     `json:"owner_user_id"`
	NoteRecordID            string     `json:"note_record_id"`
	TargetNoteID            string     `json:"target_note_id"`
	TargetKind              string     `json:"target_kind"`
	BoardID                 string     `json:"board_id,omitempty"`
	CardID                  string     `json:"card_id,omitempty"`
	ReplacementAttachmentID string     `json:"replacement_attachment_id,omitempty"`
	Filename                string     `json:"filename"`
	ContentType             string     `json:"content_type"`
	DeclaredSizeBytes       *int64     `json:"declared_size_bytes,omitempty"`
	DeclaredChecksumSHA256  string     `json:"declared_checksum_sha256,omitempty"`
	ReceivedBytes           int64      `json:"received_bytes"`
	StagingKey              string     `json:"staging_key"`
	ExpectedRevision        string     `json:"expected_revision"`
	Status                  string     `json:"status"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	ExpiresAt               time.Time  `json:"expires_at"`
	CompletedAt             *time.Time `json:"completed_at,omitempty"`
}

type AssistantCalendarEntry struct {
	ID              string    `json:"id"`
	HomeID          string    `json:"home_id"`
	UserID          string    `json:"user_id"`
	DeviceID        string    `json:"device_id"`
	ExternalEventID string    `json:"external_event_id"`
	CalendarID      string    `json:"calendar_id"`
	Title           string    `json:"title"`
	Location        string    `json:"location"`
	Notes           string    `json:"notes"`
	StartsAt        time.Time `json:"starts_at"`
	EndsAt          time.Time `json:"ends_at"`
	IsAllDay        bool      `json:"is_all_day"`
	SearchText      string    `json:"search_text"`
	MetadataJSON    string    `json:"metadata_json"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AssistantDocument struct {
	ID               string    `json:"id"`
	HomeID           string    `json:"home_id"`
	UserID           *string   `json:"user_id,omitempty"`
	SourceType       string    `json:"source_type"`
	SourceID         string    `json:"source_id"`
	SourceKey        string    `json:"source_key"`
	Title            string    `json:"title"`
	Path             string    `json:"path"`
	CanonicalURI     string    `json:"canonical_uri"`
	MetadataJSON     string    `json:"metadata_json"`
	SearchText       string    `json:"search_text"`
	EmbeddingModel   string    `json:"embedding_model"`
	EmbeddingVersion string    `json:"embedding_version"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type AssistantChunk struct {
	ID               string    `json:"id"`
	DocumentID       string    `json:"document_id"`
	ChunkIndex       int       `json:"chunk_index"`
	Content          string    `json:"content"`
	TokenCount       int       `json:"token_count"`
	EmbeddingJSON    string    `json:"embedding_json"`
	EmbeddingModel   string    `json:"embedding_model"`
	EmbeddingVersion string    `json:"embedding_version"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type AssistantFileIndex struct {
	ID               string     `json:"id"`
	HomeID           string     `json:"home_id"`
	ServiceProfileID string     `json:"service_profile_id"`
	Path             string     `json:"path"`
	Name             string     `json:"name"`
	IsDirectory      bool       `json:"is_directory"`
	SizeBytes        int64      `json:"size_bytes"`
	ModifiedAt       *time.Time `json:"modified_at,omitempty"`
	SearchText       string     `json:"search_text"`
	MetadataJSON     string     `json:"metadata_json"`
	EmbeddingJSON    string     `json:"embedding_json"`
	EmbeddingModel   string     `json:"embedding_model"`
	EmbeddingVersion string     `json:"embedding_version"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type AssistantIndexJob struct {
	ID          string     `json:"id"`
	HomeID      string     `json:"home_id"`
	UserID      string     `json:"user_id"`
	SourceType  string     `json:"source_type"`
	SourceID    string     `json:"source_id"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	RunAfter    time.Time  `json:"run_after"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type AssistantRetrievedContext struct {
	SourceType   string    `json:"source_type"`
	SourceID     string    `json:"source_id"`
	Title        string    `json:"title"`
	Path         string    `json:"path"`
	CanonicalURI string    `json:"canonical_uri"`
	Snippet      string    `json:"snippet"`
	Score        float64   `json:"score"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AssistantIndexSourceCount struct {
	SourceType         string `json:"source_type"`
	DocumentCount      int    `json:"document_count"`
	ChunkCount         int    `json:"chunk_count"`
	EmbeddedChunkCount int    `json:"embedded_chunk_count"`
}

type AssistantIndexStats struct {
	VectorAvailable    bool                        `json:"vector_available"`
	VectorMode         string                      `json:"vector_mode"`
	DocumentsBySource  []AssistantIndexSourceCount `json:"documents_by_source"`
	ChunkCount         int                         `json:"chunk_count"`
	EmbeddedChunkCount int                         `json:"embedded_chunk_count"`
	FileCount          int                         `json:"file_count"`
	EmbeddedFileCount  int                         `json:"embedded_file_count"`
	ConversationCount  int                         `json:"conversation_count"`
	QueuedJobCount     int                         `json:"queued_job_count"`
	RunningJobCount    int                         `json:"running_job_count"`
	FailedJobCount     int                         `json:"failed_job_count"`
}

type AssistantSettings struct {
	HomeID               string    `json:"home_id"`
	UserID               string    `json:"user_id"`
	ProfileNotesEnabled  bool      `json:"profile_notes_enabled"`
	HomeNotesEnabled     bool      `json:"home_notes_enabled"`
	FilesEnabled         bool      `json:"files_enabled"`
	CalendarEnabled      bool      `json:"calendar_enabled"`
	HomeAssistantEnabled bool      `json:"homeassistant_enabled"`
	ProjectDocsEnabled   bool      `json:"project_docs_enabled"`
	ConversationsEnabled bool      `json:"conversations_enabled"`
	SystemPrompt         string    `json:"system_prompt"`
	MaxContextItems      int       `json:"max_context_items"`
	AIProvider           string    `json:"ai_provider"`
	OllamaBaseURL        string    `json:"ollama_base_url"`
	ChatModel            string    `json:"chat_model"`
	EmbeddingModel       string    `json:"embedding_model"`
	PromptProfile        string    `json:"prompt_profile"`
	PlannerEnabled       bool      `json:"planner_enabled"`
	PlannerModel         string    `json:"planner_model"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	UpdatedBy            string    `json:"updated_by"`
}

type OpenAIAccount struct {
	UserID          string     `json:"user_id"`
	ProviderUserID  string     `json:"provider_user_id"`
	AuthProvider    string     `json:"auth_provider"`
	ChatGPTPlanType string     `json:"chatgpt_plan_type"`
	AccessToken     string     `json:"-"`
	RefreshToken    string     `json:"-"`
	TokenType       string     `json:"token_type"`
	Scope           string     `json:"scope"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
