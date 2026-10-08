package config

import (
	"crypto/elliptic"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestLoadCloudWebPushConfiguration(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_WEB_PUSH_VAPID_PUBLIC_KEY", "")
	t.Setenv("HANK_WEB_PUSH_VAPID_PRIVATE_KEY", "")
	t.Setenv("HANK_WEB_PUSH_VAPID_SUBJECT", "")
	cfg, err := LoadCloud()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebPush.Enabled {
		t.Fatal("Web Push enabled without VAPID configuration")
	}

	x, y := elliptic.P256().ScalarBaseMult([]byte{1})
	publicKey := elliptic.Marshal(elliptic.P256(), x, y)
	privateKey := make([]byte, 32)
	privateKey[31] = 1
	t.Setenv("HANK_WEB_PUSH_VAPID_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(publicKey))
	t.Setenv("HANK_WEB_PUSH_VAPID_PRIVATE_KEY", base64.RawURLEncoding.EncodeToString(privateKey))
	t.Setenv("HANK_WEB_PUSH_VAPID_SUBJECT", "mailto:operator@example.com")
	cfg, err = LoadCloud()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WebPush.Enabled || cfg.WebPush.Subject != "mailto:operator@example.com" {
		t.Fatalf("WebPush = %#v", cfg.WebPush)
	}
}

func TestLoadCloudRejectsPartialOrMalformedWebPushConfiguration(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_WEB_PUSH_VAPID_PUBLIC_KEY", "public-only")
	t.Setenv("HANK_WEB_PUSH_VAPID_PRIVATE_KEY", "")
	t.Setenv("HANK_WEB_PUSH_VAPID_SUBJECT", "")
	if _, err := LoadCloud(); err == nil || !strings.Contains(err.Error(), "WEB_PUSH") {
		t.Fatalf("partial Web Push error = %v", err)
	}
	t.Setenv("HANK_WEB_PUSH_VAPID_PRIVATE_KEY", "private")
	t.Setenv("HANK_WEB_PUSH_VAPID_SUBJECT", "javascript:alert(1)")
	if _, err := LoadCloud(); err == nil || !strings.Contains(err.Error(), "VAPID") {
		t.Fatalf("malformed Web Push error = %v", err)
	}
}

func TestLoadCloudRejectsMismatchedWebPushKeyPair(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")

	x, y := elliptic.P256().ScalarBaseMult([]byte{1})
	publicKey := elliptic.Marshal(elliptic.P256(), x, y)
	privateKey := make([]byte, 32)
	privateKey[31] = 2
	t.Setenv("HANK_WEB_PUSH_VAPID_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(publicKey))
	t.Setenv("HANK_WEB_PUSH_VAPID_PRIVATE_KEY", base64.RawURLEncoding.EncodeToString(privateKey))
	t.Setenv("HANK_WEB_PUSH_VAPID_SUBJECT", "mailto:operator@example.com")

	if _, err := LoadCloud(); err == nil || !strings.Contains(err.Error(), "matching key pair") {
		t.Fatalf("mismatched Web Push key error = %v", err)
	}
}

func TestLoadCloudDefaults(t *testing.T) {
	t.Setenv("HANK_CLOUD_ADDR", "")
	t.Setenv("HANK_CLOUD_DATABASE_URL", "")
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_SESSION_TTL_SECONDS", "")
	t.Setenv("HANK_REQUEST_TIMEOUT_SECONDS", "")
	t.Setenv("HANK_MAINTENANCE_INTERVAL_SECONDS", "")
	t.Setenv("HANK_MAINTENANCE_RETENTION_DAYS", "")
	t.Setenv("HANK_CHATGPT_OAUTH_ENABLED", "")
	t.Setenv("HANK_CHATGPT_AUTH_ISSUER", "")
	t.Setenv("HANK_CHATGPT_BACKEND_BASE_URL", "")
	t.Setenv("HANK_CHATGPT_CLIENT_ID", "")
	t.Setenv("HANK_CHATGPT_CHAT_MODEL", "")
	t.Setenv("HANK_PROJECT_DOCS_DIR", "")

	cfg, err := LoadCloud()
	if err != nil {
		t.Fatalf("LoadCloud error: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, ":8080")
	}
	if !strings.Contains(cfg.DatabaseURL, "postgres://") {
		t.Fatalf("DatabaseURL = %q, want default postgres URL", cfg.DatabaseURL)
	}
	if cfg.SessionTTL != 7*24*time.Hour {
		t.Fatalf("SessionTTL = %s, want %s", cfg.SessionTTL, 7*24*time.Hour)
	}
	if cfg.RequestTimeout != 120*time.Second {
		t.Fatalf("RequestTimeout = %s, want %s", cfg.RequestTimeout, 120*time.Second)
	}
	if cfg.MaintenanceInterval != time.Hour {
		t.Fatalf("MaintenanceInterval = %s, want %s", cfg.MaintenanceInterval, time.Hour)
	}
	if cfg.MaintenanceRetention != 30*24*time.Hour {
		t.Fatalf("MaintenanceRetention = %s, want %s", cfg.MaintenanceRetention, 30*24*time.Hour)
	}
	if cfg.AssistantAI.ChatGPTOAuthEnabled {
		t.Fatal("ChatGPTOAuthEnabled = true, want false by default")
	}
	if cfg.AssistantAI.ChatGPTAuthIssuer != "https://auth.openai.com" {
		t.Fatalf("ChatGPTAuthIssuer = %q", cfg.AssistantAI.ChatGPTAuthIssuer)
	}
	if cfg.AssistantAI.ChatGPTBackendBaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("ChatGPTBackendBaseURL = %q", cfg.AssistantAI.ChatGPTBackendBaseURL)
	}
	if cfg.AssistantAI.ChatGPTClientID != "app_EMoamEEZ73f0CkXaXp7hrann" {
		t.Fatalf("ChatGPTClientID = %q", cfg.AssistantAI.ChatGPTClientID)
	}
	if cfg.AssistantAI.ChatGPTChatModel != "gpt-5.4-mini" {
		t.Fatalf("ChatGPTChatModel = %q", cfg.AssistantAI.ChatGPTChatModel)
	}
	if cfg.AssistantAI.ProjectDocsDir != "." {
		t.Fatalf("ProjectDocsDir = %q", cfg.AssistantAI.ProjectDocsDir)
	}
}

func TestLoadCloudParsesLinuxAgentReleaseConfig(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_LINUX_AGENT_RELEASE_DIR", " /srv/hank/linux-agent ")
	t.Setenv("HANK_LINUX_AGENT_RELEASE_PUBLIC_KEY", " release-public-key ")

	cfg, err := LoadCloud()
	if err != nil {
		t.Fatalf("LoadCloud error: %v", err)
	}
	if cfg.LinuxAgentReleaseDir != "/srv/hank/linux-agent" {
		t.Fatalf("LinuxAgentReleaseDir = %q", cfg.LinuxAgentReleaseDir)
	}
	if cfg.LinuxAgentReleasePublicKey != "release-public-key" {
		t.Fatalf("LinuxAgentReleasePublicKey = %q", cfg.LinuxAgentReleasePublicKey)
	}
}

func TestLoadCloudParsesChatGPTOAuthConfig(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_CHATGPT_OAUTH_ENABLED", "true")
	t.Setenv("HANK_CHATGPT_AUTH_ISSUER", " https://auth.example.com/ ")
	t.Setenv("HANK_CHATGPT_BACKEND_BASE_URL", " https://chatgpt.example.com/backend-api/codex/ ")
	t.Setenv("HANK_CHATGPT_CLIENT_ID", "test-client")
	t.Setenv("HANK_CHATGPT_CHAT_MODEL", "gpt-test")
	t.Setenv("HANK_PROJECT_DOCS_DIR", "/srv/hank/docs")

	cfg, err := LoadCloud()
	if err != nil {
		t.Fatalf("LoadCloud error: %v", err)
	}
	if !cfg.AssistantAI.ChatGPTOAuthEnabled {
		t.Fatal("ChatGPTOAuthEnabled = false, want true")
	}
	if cfg.AssistantAI.ChatGPTAuthIssuer != "https://auth.example.com" {
		t.Fatalf("ChatGPTAuthIssuer = %q", cfg.AssistantAI.ChatGPTAuthIssuer)
	}
	if cfg.AssistantAI.ChatGPTBackendBaseURL != "https://chatgpt.example.com/backend-api/codex" {
		t.Fatalf("ChatGPTBackendBaseURL = %q", cfg.AssistantAI.ChatGPTBackendBaseURL)
	}
	if cfg.AssistantAI.ChatGPTClientID != "test-client" {
		t.Fatalf("ChatGPTClientID = %q", cfg.AssistantAI.ChatGPTClientID)
	}
	if cfg.AssistantAI.ChatGPTChatModel != "gpt-test" {
		t.Fatalf("ChatGPTChatModel = %q", cfg.AssistantAI.ChatGPTChatModel)
	}
	if cfg.AssistantAI.ProjectDocsDir != "/srv/hank/docs" {
		t.Fatalf("ProjectDocsDir = %q", cfg.AssistantAI.ProjectDocsDir)
	}
}

func TestLoadCloudRequiresCompleteEntraConfig(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_ENTRA_ENABLED", "true")
	t.Setenv("HANK_ENTRA_TENANT_ID", "tenant")
	t.Setenv("HANK_ENTRA_CLIENT_ID", "client")
	t.Setenv("HANK_ENTRA_CLIENT_SECRET", "")
	t.Setenv("HANK_PUBLIC_BASE_URL", "https://hank.example")

	if _, err := LoadCloud(); err == nil || !strings.Contains(err.Error(), "HANK_ENTRA_CLIENT_SECRET") {
		t.Fatalf("LoadCloud error = %v, want Entra configuration validation", err)
	}
}

func TestLoadCloudRejectsInvalidDuration(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_SESSION_TTL_SECONDS", "0")

	_, err := LoadCloud()
	if err == nil || !strings.Contains(err.Error(), "HANK_SESSION_TTL_SECONDS") {
		t.Fatalf("LoadCloud error = %v, want session ttl validation error", err)
	}
}

func TestLoadCloudRejectsInvalidMaintenanceRetention(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "test-secret-encryption-key")
	t.Setenv("HANK_MAINTENANCE_RETENTION_DAYS", "0")

	_, err := LoadCloud()
	if err == nil || !strings.Contains(err.Error(), "HANK_MAINTENANCE_RETENTION_DAYS") {
		t.Fatalf("LoadCloud error = %v, want maintenance retention validation error", err)
	}
}

func TestLoadCloudRequiresDBOpsIntentSecret(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "")

	_, err := LoadCloud()
	if err == nil || !strings.Contains(err.Error(), "HANK_DB_OPS_INTENT_SECRET") {
		t.Fatalf("LoadCloud error = %v, want missing db ops intent secret error", err)
	}
}

func TestLoadCloudRequiresSecretEncryptionKeyUnlessPlaintextOptOut(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "")
	t.Setenv("HANK_ALLOW_PLAINTEXT_SECRETS", "")

	_, err := LoadCloud()
	if err == nil || !strings.Contains(err.Error(), "HANK_SECRET_ENCRYPTION_KEY") {
		t.Fatalf("LoadCloud error = %v, want missing secret encryption key error", err)
	}

	t.Setenv("HANK_ALLOW_PLAINTEXT_SECRETS", "true")
	cfg, err := LoadCloud()
	if err != nil {
		t.Fatalf("LoadCloud with plaintext opt-out error: %v", err)
	}
	if !cfg.AllowPlaintextSecrets {
		t.Fatal("AllowPlaintextSecrets = false, want true for explicit dev opt-out")
	}
}

func TestLoadDBOpsRequiresIntentSecret(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "")
	t.Setenv("HANK_DB_OPS_REPO_CIPHER_PASS", "cipher-secret")

	_, err := LoadDBOps()
	if err == nil || !strings.Contains(err.Error(), "HANK_DB_OPS_INTENT_SECRET") {
		t.Fatalf("LoadDBOps error = %v, want missing intent secret error", err)
	}
}

func TestLoadDBOpsRequiresRepoCipherPass(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_DB_OPS_REPO_CIPHER_PASS", "")

	_, err := LoadDBOps()
	if err == nil || !strings.Contains(err.Error(), "HANK_DB_OPS_REPO_CIPHER_PASS") {
		t.Fatalf("LoadDBOps error = %v, want missing cipher pass error", err)
	}
}

func TestLoadDBOpsParsesRepoCipherPass(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "test-db-ops-intent-secret")
	t.Setenv("HANK_DB_OPS_REPO_CIPHER_PASS", " cipher-secret ")

	cfg, err := LoadDBOps()
	if err != nil {
		t.Fatalf("LoadDBOps error: %v", err)
	}
	if cfg.RepoCipherPass != "cipher-secret" {
		t.Fatalf("RepoCipherPass = %q, want trimmed cipher pass", cfg.RepoCipherPass)
	}
}

func TestLoadAgentRequiresIdentityAndToken(t *testing.T) {
	t.Setenv("HANK_AGENT_ID", "")
	t.Setenv("HANK_AGENT_TOKEN", "")

	_, err := LoadAgent()
	if err == nil || !strings.Contains(err.Error(), "HANK_AGENT_ID") {
		t.Fatalf("LoadAgent error = %v, want missing agent id error", err)
	}

	t.Setenv("HANK_AGENT_ID", "home-main")

	_, err = LoadAgent()
	if err == nil || !strings.Contains(err.Error(), "HANK_AGENT_TOKEN") {
		t.Fatalf("LoadAgent error = %v, want missing agent token error", err)
	}
}

func TestLoadAgentParsesValidConfig(t *testing.T) {
	t.Setenv("HANK_AGENT_CLOUD_URL", "ws://cloud.example/ws/agent")
	t.Setenv("HANK_AGENT_ID", "home-main")
	t.Setenv("HANK_AGENT_TOKEN", "secret-token")
	t.Setenv("HANK_AGENT_HOME_NAME", "Campbell Home")
	t.Setenv("HANK_HA_BASE_URL", "http://127.0.0.1:8123")
	t.Setenv("HANK_HA_TOKEN", "ha-token")
	t.Setenv("HANK_HA_TIMEOUT_SECONDS", "12")
	t.Setenv("HANK_SMB_SHARES_JSON", `[{"id":"media","host":"192.168.1.20","share":"media","username":"aaron","password":"secret","domain":"WORKGROUP"}]`)
	t.Setenv("HANK_AGENT_FILES_ROOT", "/srv/hank/files")
	t.Setenv("HANK_AGENT_NOTES_ROOT", "/srv/hank/notes")
	t.Setenv("HANK_AGENT_SHELL_ENABLED", "true")
	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent error: %v", err)
	}

	if cfg.CloudURL != "ws://cloud.example/ws/agent" {
		t.Fatalf("CloudURL = %q", cfg.CloudURL)
	}
	if cfg.AgentID != "home-main" || cfg.Token != "secret-token" {
		t.Fatalf("agent identity = %#v", cfg)
	}
	if cfg.HA.Timeout != 12*time.Second {
		t.Fatalf("HA.Timeout = %s, want %s", cfg.HA.Timeout, 12*time.Second)
	}
	if len(cfg.SMBShares) != 1 || cfg.SMBShares[0].Share != "media" {
		t.Fatalf("SMBShares = %#v, want JSON SMB share", cfg.SMBShares)
	}
	if cfg.FilesRoot != "/srv/hank/files" || cfg.NotesRoot != "/srv/hank/notes" {
		t.Fatalf("roots = files:%q notes:%q", cfg.FilesRoot, cfg.NotesRoot)
	}
	if !cfg.ShellEnabled {
		t.Fatal("ShellEnabled = false, want true")
	}
}

func TestLoadAgentParsesAppRuntimePaths(t *testing.T) {
	t.Setenv("HANK_AGENT_CLOUD_URL", "ws://cloud.example/ws/agent")
	t.Setenv("HANK_AGENT_ID", "home-main")
	t.Setenv("HANK_AGENT_TOKEN", "secret-token")
	t.Setenv("HANK_AGENT_OPERATION_DIR", " /srv/hank/operations ")
	t.Setenv("HANK_AGENT_APPS_DIR", " /srv/hank/apps ")
	t.Setenv("HANK_AGENT_APP_STAGING_DIR", " /srv/hank/app-staging ")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent error: %v", err)
	}
	if cfg.OperationDir != "/srv/hank/operations" {
		t.Fatalf("OperationDir = %q", cfg.OperationDir)
	}
	if cfg.AppsDir != "/srv/hank/apps" {
		t.Fatalf("AppsDir = %q", cfg.AppsDir)
	}
	if cfg.AppStagingDir != "/srv/hank/app-staging" {
		t.Fatalf("AppStagingDir = %q", cfg.AppStagingDir)
	}
}

func TestLoadAgentAppRuntimePathDefaults(t *testing.T) {
	t.Setenv("HANK_AGENT_CLOUD_URL", "ws://cloud.example/ws/agent")
	t.Setenv("HANK_AGENT_ID", "home-main")
	t.Setenv("HANK_AGENT_TOKEN", "secret-token")
	t.Setenv("HANK_AGENT_OPERATION_DIR", "")
	t.Setenv("HANK_AGENT_APPS_DIR", "")
	t.Setenv("HANK_AGENT_APP_STAGING_DIR", "")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent error: %v", err)
	}
	if cfg.OperationDir != "/var/lib/hank/assistant-operations" {
		t.Fatalf("OperationDir = %q", cfg.OperationDir)
	}
	if cfg.AppsDir != "/var/lib/hank/apps" {
		t.Fatalf("AppsDir = %q", cfg.AppsDir)
	}
	if cfg.AppStagingDir != "/var/lib/hank/app-staging" {
		t.Fatalf("AppStagingDir = %q", cfg.AppStagingDir)
	}
}

func TestLoadAgentIgnoresLegacySingleShareSMBEnv(t *testing.T) {
	t.Setenv("HANK_AGENT_ID", "home-main")
	t.Setenv("HANK_AGENT_TOKEN", "secret-token")
	t.Setenv("HANK_SMB_HOST", "192.168.1.20")
	t.Setenv("HANK_SMB_SHARE", "media")
	t.Setenv("HANK_SMB_USERNAME", "aaron")
	t.Setenv("HANK_SMB_PASSWORD", "secret")
	t.Setenv("HANK_SMB_DOMAIN", "WORKGROUP")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent error: %v", err)
	}
	if len(cfg.SMBShares) != 0 {
		t.Fatalf("SMBShares = %#v, want legacy single-share env ignored", cfg.SMBShares)
	}
}

func TestLoadAgentParsesLocalFoldersJSON(t *testing.T) {
	t.Setenv("HANK_AGENT_ID", "home-main")
	t.Setenv("HANK_AGENT_TOKEN", "secret-token")
	t.Setenv("HANK_AGENT_FILES_ROOTS_JSON", `[
		{"id":"media","name":"Media","root":"/srv/media"},
		{"root":" /srv/docs "}
	]`)

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent error: %v", err)
	}
	if len(cfg.LocalFolders) != 2 {
		t.Fatalf("LocalFolders count = %d, want 2", len(cfg.LocalFolders))
	}
	if cfg.LocalFolders[0].ID != "media" || cfg.LocalFolders[0].Root != "/srv/media" {
		t.Fatalf("first folder = %#v, want media", cfg.LocalFolders[0])
	}
	if cfg.LocalFolders[1].Root != "/srv/docs" {
		t.Fatalf("second folder root = %q, want trimmed /srv/docs", cfg.LocalFolders[1].Root)
	}
}

func TestLoadAgentRejectsInvalidLocalFoldersJSON(t *testing.T) {
	t.Setenv("HANK_AGENT_ID", "home-main")
	t.Setenv("HANK_AGENT_TOKEN", "secret-token")
	t.Setenv("HANK_AGENT_FILES_ROOTS_JSON", `{not valid json`)

	if _, err := LoadAgent(); err == nil {
		t.Fatal("LoadAgent accepted invalid HANK_AGENT_FILES_ROOTS_JSON, want error")
	}
}

func TestLoadAgentParsesMultipleSMBSharesJSON(t *testing.T) {
	t.Setenv("HANK_AGENT_ID", "home-main")
	t.Setenv("HANK_AGENT_TOKEN", "secret-token")
	t.Setenv("HANK_SMB_SHARES_JSON", `[
		{"id":"media","name":"Media","host":"192.168.1.20","share":"media","username":"aaron","password":"media-secret","domain":"WORKGROUP"},
		{"id":"archive","name":"Archive","host":"192.168.1.21","share":"archive","username":"aaron","password":"archive-secret"}
	]`)

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent error: %v", err)
	}

	if len(cfg.SMBShares) != 2 {
		t.Fatalf("SMBShares count = %d, want 2", len(cfg.SMBShares))
	}
	if cfg.SMBShares[0].ID != "media" || cfg.SMBShares[0].Password != "media-secret" {
		t.Fatalf("first SMB share = %#v, want media with password", cfg.SMBShares[0])
	}
	if cfg.SMBShares[1].ID != "archive" || cfg.SMBShares[1].Share != "archive" {
		t.Fatalf("second SMB share = %#v, want archive", cfg.SMBShares[1])
	}
}

func TestLoadCloudRequiresSeparateAlertmanagerCredential(t *testing.T) {
	t.Setenv("HANK_DB_OPS_INTENT_SECRET", "fixture-intent")
	t.Setenv("HANK_SECRET_ENCRYPTION_KEY", "fixture-encryption")
	t.Setenv("HANK_METRICS_SCRAPE_TOKEN", "fixture-scrape")
	t.Setenv("HANK_ALERTMANAGER_WEBHOOK_TOKEN", "fixture-scrape")
	if _, err := LoadCloud(); err == nil {
		t.Fatal("shared read/write monitoring credential accepted")
	}
	t.Setenv("HANK_ALERTMANAGER_WEBHOOK_TOKEN", "fixture-inbox")
	cfg, err := LoadCloud()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AlertmanagerWebhookToken != "fixture-inbox" {
		t.Fatal("inbox credential not loaded")
	}
}
