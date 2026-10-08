package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"golang.org/x/crypto/bcrypt"

	"github.com/dropfile/HankServerside/internal/cloud"
	"github.com/dropfile/HankServerside/internal/config"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/storageops"
	"github.com/dropfile/HankServerside/internal/store"
)

// buildVersion is stamped at build time via
// -ldflags "-X main.buildVersion=<version>"; it stays "dev" for local
// go run/go build workflows.
var buildVersion = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	// This explicit maintenance command must also work before the environment
	// naming migration. It needs only the canonical database URL, never server
	// startup, app settings, or secret-storage initialization.
	if len(os.Args) >= 3 && os.Args[1] == "migrate" && os.Args[2] == "reconcile-assistant-index" {
		if err := runAssistantIndexReconciliation(context.Background(), os.Getenv("HANK_CLOUD_DATABASE_URL"), os.Args[3:], os.Stdout); err != nil {
			logger.Error("migration reconciliation failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "web-push-keys" {
		if err := runWebPushKeysCommand(os.Stdout); err != nil {
			logger.Error("Web Push key generation failed", "error", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.LoadCloud()
	if err != nil {
		logger.Error("failed to load cloud config", "error", err)
		os.Exit(1)
	}
	for _, warning := range config.RuntimeEnvFileWarnings(".env.cloud") {
		logger.Warn("runtime env file is group/world-readable; run chmod 600", "path", warning.Path, "mode", warning.Mode.String())
	}

	ctx := context.Background()
	if len(os.Args) >= 2 && os.Args[1] == "migrate" {
		if err := runMigrateCommand(ctx, cfg, os.Args[2:]); err != nil {
			logger.Error("migration command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "secrets" {
		if err := runSecretsCommand(ctx, cfg, os.Args[2:]); err != nil {
			logger.Error("secret storage command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "users" {
		if err := runUsersCommand(ctx, cfg, os.Args[2:]); err != nil {
			logger.Error("user command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "linux-release" {
		if err := runLinuxReleaseCommand(ctx, cfg, os.Args[2:], os.Stdout); err != nil {
			logger.Error("Linux release command failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if err := storageops.CheckPrimaryRestoreStartup(cfg.DBOpsStateDir); err != nil {
		logger.Error("primary restore startup guard", "error", err)
		os.Exit(1)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to open cloud storage", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.ConfigureSecretEncryption(cfg.SecretKey); err != nil {
		logger.Error("failed to configure secret encryption", "error", err)
		os.Exit(1)
	}
	if cfg.AllowPlaintextSecrets {
		logger.Warn("plaintext secret storage is explicitly enabled; set HANK_SECRET_ENCRYPTION_KEY before production use")
	}

	entraConfig := cloud.EntraConfig{
		Enabled: cfg.Entra.Enabled, TenantID: cfg.Entra.TenantID, ClientID: cfg.Entra.ClientID,
		ClientSecret: cfg.Entra.ClientSecret, PublicBaseURL: cfg.Entra.PublicBaseURL,
	}
	if home, err := db.GetSingletonHome(ctx); err == nil {
		if settings, err := db.GetEntraSSOSettings(ctx, home.ID); err == nil {
			entraConfig = cloud.EntraConfig{Enabled: settings.Enabled, TenantID: settings.TenantID, ClientID: settings.ClientID, ClientSecret: settings.ClientSecret, PublicBaseURL: settings.PublicBaseURL}
		} else if !errors.Is(err, store.ErrNotFound) {
			logger.Error("failed to load Entra SSO settings", "error", err)
			os.Exit(1)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		logger.Error("failed to load home for Entra SSO settings", "error", err)
		os.Exit(1)
	}
	server := cloud.NewServer(cfg.Addr, db, cfg.SessionTTL, cfg.RequestTimeout, logger)
	if err := server.ConfigureLinuxAgentRelease(cfg.LinuxAgentReleaseDir, cfg.LinuxAgentReleasePublicKey); err != nil {
		logger.Error("failed to configure Linux agent release", "error", err)
		os.Exit(1)
	}
	if err := server.ConfigureEntra(ctx, entraConfig); err != nil {
		logger.Error("failed to configure Entra SSO", "error", err)
		os.Exit(1)
	}
	server.ConfigureSecretStorageStatus(cfg.SecretKey != "", cfg.AllowPlaintextSecrets)
	server.ConfigureMetricsScrapeToken(cfg.MetricsScrapeToken)
	server.ConfigureAlertmanagerWebhookToken(cfg.AlertmanagerWebhookToken)
	if err := server.ConfigureMonitoringDelivery(cfg.MonitoringConfigDir, cfg.AlertmanagerURL, 65534); err != nil {
		logger.Error("invalid monitoring configuration")
		os.Exit(1)
	}
	if err := server.ConfigureMonitoringCollection(cfg.PrometheusURL); err != nil {
		logger.Error("invalid monitoring collection configuration")
		os.Exit(1)
	}
	server.ConfigureAPNS(cloud.APNSConfig{
		TeamID:      cfg.APNS.TeamID,
		KeyID:       cfg.APNS.KeyID,
		PrivateKey:  cfg.APNS.PrivateKey,
		Topic:       cfg.APNS.Topic,
		Environment: cfg.APNS.Environment,
	})
	if err := server.ConfigureWebPush(cloud.WebPushConfig{
		Enabled: cfg.WebPush.Enabled, PublicKey: cfg.WebPush.PublicKey,
		PrivateKey: cfg.WebPush.PrivateKey, Subject: cfg.WebPush.Subject,
	}); err != nil {
		logger.Error("failed to configure Web Push", "error", err)
		os.Exit(1)
	}
	server.ConfigureAssistantAI(cloud.AssistantAIConfig{
		ExecutionEnabled:      cfg.AssistantAI.ExecutionEnabled,
		Provider:              cfg.AssistantAI.Provider,
		OllamaBaseURL:         cfg.AssistantAI.OllamaBaseURL,
		OllamaChatModel:       cfg.AssistantAI.OllamaChatModel,
		OllamaEmbeddingModel:  cfg.AssistantAI.OllamaEmbeddingModel,
		OpenAIBaseURL:         cfg.AssistantAI.OpenAIBaseURL,
		OpenAIAPIKey:          cfg.AssistantAI.OpenAIAPIKey,
		OpenAIChatModel:       cfg.AssistantAI.OpenAIChatModel,
		OpenAIEmbeddingModel:  cfg.AssistantAI.OpenAIEmbeddingModel,
		ChatGPTOAuthEnabled:   cfg.AssistantAI.ChatGPTOAuthEnabled,
		ChatGPTAuthIssuer:     cfg.AssistantAI.ChatGPTAuthIssuer,
		ChatGPTBackendBaseURL: cfg.AssistantAI.ChatGPTBackendBaseURL,
		ChatGPTClientID:       cfg.AssistantAI.ChatGPTClientID,
		ChatGPTChatModel:      cfg.AssistantAI.ChatGPTChatModel,
		ProjectDocsDir:        cfg.AssistantAI.ProjectDocsDir,
		EmbeddingDimension:    cfg.AssistantAI.EmbeddingDimension,
	})
	server.ConfigureStorageOps(cfg.DBOpsStateDir, cfg.DBOpsLogDir, cfg.DBOpsIntentSecret)
	server.ConfigureNoteAttachmentStorage(cfg.NoteAttachmentDir)
	server.ConfigureMCP(cloud.MCPConfig{
		Enabled:       cfg.MCPEnabled,
		PublicBaseURL: cfg.MCPPublicBaseURL,
		DocsDir:       cfg.MCPDocsDir,
	})
	if err := server.StartRuntime(ctx, buildVersion); err != nil {
		logger.Error("failed to start runtime coordination", "error", err)
		os.Exit(1)
	}
	server.StartMaintenance(cfg.MaintenanceInterval, cfg.MaintenanceRetention)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	shutdownSignalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("cloud server failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownSignalCtx.Done():
		logger.Info("shutting down cloud server")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("cloud shutdown failed", "error", err)
		os.Exit(1)
	}
}

func runWebPushKeysCommand(output io.Writer) error {
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "HANK_WEB_PUSH_VAPID_PUBLIC_KEY=%s\n", publicKey); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "HANK_WEB_PUSH_VAPID_PRIVATE_KEY=%s\n", privateKey)
	return err
}

func runUsersCommand(ctx context.Context, cfg config.Cloud, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hank-server users reset-password --email user@example.com [--force-change] [--stdin] [--admin-only]")
	}
	switch args[0] {
	case "reset-password":
		return runResetPasswordCommand(ctx, cfg, args[1:])
	default:
		return errors.New("unknown users command")
	}
}

func runResetPasswordCommand(ctx context.Context, cfg config.Cloud, args []string) error {
	var email string
	forceChange := false
	useStdin := false
	adminOnly := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--email":
			if i+1 >= len(args) {
				return fmt.Errorf("--email requires a value")
			}
			email = strings.TrimSpace(strings.ToLower(args[i+1]))
			i++
		case "--force-change":
			forceChange = true
		case "--stdin":
			useStdin = true
		case "--admin-only":
			adminOnly = true
		default:
			return fmt.Errorf("unknown reset-password option %s", args[i])
		}
	}
	if email == "" {
		return fmt.Errorf("--email is required")
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	user, err := db.GetUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if adminOnly {
		home, err := db.GetSingletonHomeForUser(ctx, user.ID)
		if err != nil {
			return fmt.Errorf("admin-only membership check: %w", err)
		}
		membership, err := db.GetHomeMembership(ctx, home.ID, user.ID)
		if err != nil {
			return fmt.Errorf("admin-only role check: %w", err)
		}
		if membership.Role != domain.HomeRoleAdmin {
			return fmt.Errorf("user is not a home admin")
		}
	}

	password := ""
	generated := false
	if useStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		password = strings.TrimRight(string(data), "\r\n")
	} else {
		password = randomCLISecret(18)
		generated = true
	}
	if len(password) < 8 {
		return fmt.Errorf("temporary password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := db.UpdateUserPassword(ctx, user.ID, string(hash), forceChange, "cli", true, ""); err != nil {
		return err
	}
	_ = db.CreateAuditEvent(ctx, store.AuditEvent{
		ID:         "audit_" + randomCLISecret(12),
		OccurredAt: time.Now().UTC(),
		EventType:  "password.reset",
		Severity:   "warning",
		TargetType: "user",
		TargetID:   user.ID,
		MetadataJSON: fmt.Sprintf(`{"actor":"cli","email_hash":"%s","password_change_required":%t,"generated":%t}`,
			stableCLIHash(email),
			forceChange,
			generated,
		),
	})

	fmt.Printf("user_id=%s\n", user.ID)
	fmt.Printf("email=%s\n", user.Email)
	fmt.Printf("password_change_required=%t\n", forceChange)
	fmt.Println("sessions_revoked=true")
	if generated {
		fmt.Printf("temporary_password=%s\n", password)
	}
	return nil
}

func randomCLISecret(size int) string {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		panic(fmt.Sprintf("random generation failed: %v", err))
	}
	return hex.EncodeToString(data)
}

func stableCLIHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(value))))
	return hex.EncodeToString(sum[:12])
}

func runMigrateCommand(ctx context.Context, cfg config.Cloud, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hank-server migrate [up|status|baseline]")
	}

	switch args[0] {
	case "up":
		db, err := store.OpenMigrating(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.CheckMigrations(ctx)
	case "baseline":
		return store.BaselineExisting(ctx, cfg.DatabaseURL)
	case "status":
		statuses, err := store.MigrationStatuses(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		for _, status := range statuses {
			fmt.Printf("%06d %s checksum=%s applied_at=%s duration_ms=%d\n", status.Version, status.Name, status.Checksum, status.AppliedAt.Format(time.RFC3339), status.DurationMS)
		}
		if len(args) > 1 && args[1] == "--strict" {
			return store.CheckMigrations(ctx, cfg.DatabaseURL)
		}
		return nil
	default:
		return errors.New("unknown migrate command")
	}
}

func runSecretsCommand(ctx context.Context, cfg config.Cloud, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hank-server secrets [status|reencrypt]")
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ConfigureSecretEncryption(cfg.SecretKey); err != nil {
		return err
	}

	switch args[0] {
	case "status":
		report, err := db.SecretStorageReport(ctx)
		if err != nil {
			return err
		}
		printSecretStorageReport(report)
		if len(args) > 1 && args[1] == "--strict" && report.PlaintextTotal() > 0 {
			return fmt.Errorf("plaintext secret rows detected")
		}
		return nil
	case "reencrypt":
		report, err := db.ReencryptPlaintextSecrets(ctx)
		if err != nil {
			return err
		}
		printSecretStorageReport(report)
		fmt.Printf("secret_columns_reencrypted=%d\n", report.ReencryptedSecretColumns)
		if report.PlaintextTotal() > 0 {
			return fmt.Errorf("plaintext secret rows remain after re-encryption")
		}
		return nil
	default:
		return errors.New("unknown secrets command")
	}
}

func printSecretStorageReport(report store.SecretStorageReport) {
	printSecretStorageReportTo(os.Stdout, report)
}

func printSecretStorageReportTo(output io.Writer, report store.SecretStorageReport) {
	fmt.Fprintf(output, "plaintext_openai_access_tokens=%d\n", report.OpenAIAccessTokens)
	fmt.Fprintf(output, "plaintext_openai_refresh_tokens=%d\n", report.OpenAIRefreshTokens)
	fmt.Fprintf(output, "plaintext_apns_device_tokens=%d\n", report.APNSDeviceTokens)
	fmt.Fprintf(output, "plaintext_monitoring_credentials=%d\n", report.MonitoringCredentials)
	fmt.Fprintf(output, "plaintext_web_push_subscriptions=%d\n", report.WebPushSubscriptions)
	fmt.Fprintf(output, "plaintext_user_profile_secret_vaults=%d\n", report.UserProfileSecretVaults)
	fmt.Fprintf(output, "plaintext_total=%d\n", report.PlaintextTotal())
}
