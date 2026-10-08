package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dropfile/HankServerside/internal/agent"
	agentapps "github.com/dropfile/HankServerside/internal/agent/apps"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	agentha "github.com/dropfile/HankServerside/internal/agent/homeassistant"
	agentnotes "github.com/dropfile/HankServerside/internal/agent/notes"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/config"
)

// buildSourceCommit is stamped for immutable builds with:
// -ldflags "-X main.buildSourceCommit=hank-source-commit:<full-commit>".
// The marker lets the signed Linux release verifier recover provenance even
// when Go cannot auto-stamp VCS metadata (for example, inside a Docker build).
var buildSourceCommit = "hank-source-commit:unknown"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	logger.Info("starting Hank Agent", "source_commit", strings.TrimPrefix(buildSourceCommit, "hank-source-commit:"))

	cfg, err := config.LoadAgent()
	if err != nil {
		logger.Error("failed to load agent config", "error", err)
		os.Exit(1)
	}
	envPaths := []string{".env.agent"}
	if cfg.ConfigPath != "" {
		envPaths = append(envPaths, cfg.ConfigPath)
	}
	for _, warning := range config.RuntimeEnvFileWarnings(envPaths...) {
		logger.Warn("runtime env file is group/world-readable; run chmod 600", "path", warning.Path, "mode", warning.Mode.String())
	}

	ha := agentha.New(cfg.HA.BaseURL, cfg.HA.Token, cfg.HA.Timeout)
	files := agentfiles.NewWithConfig(agentfiles.Config{
		Root:         cfg.FilesRoot,
		LocalSources: agentLocalFolders(cfg.LocalFolders),
		Shares:       agentSMBShares(cfg.SMBShares),
	})
	notes := agentnotes.New(cfg.NotesRoot)
	appManager := agentapps.NewManager(cfg.AppsDir, cfg.AppStagingDir, agentapps.Runner{
		RuntimeDir:     cfg.AppRuntimeDir,
		CgroupRoot:     cfg.AppCgroupRoot,
		MaxOutputBytes: 1 << 20,
		MaxStderrBytes: 16 << 10,
	})
	if err := appManager.Load(context.Background()); err != nil {
		logger.Warn("failed to load agent apps", "error", err)
	}

	client := agent.NewClient(cfg.CloudURL, cfg.AgentID, cfg.Token, cfg.HomeName, cfg.ConfigPath, ha, files, notes, appManager, logger)
	client.SetShellEnabled(cfg.ShellEnabled)
	if err := client.SetAssistantServiceOperations(cfg.AssistantServiceOperationsJSON); err != nil {
		logger.Error("invalid assistant service operation allowlist")
		os.Exit(1)
	}
	journal, err := operations.Open(cfg.OperationDir)
	if err != nil {
		logger.Warn("durable assistant operations unavailable; receipt store could not be opened")
	} else {
		defer journal.Close()
		client.SetOperationJournal(journal)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("agent exited with error", "error", err)
		os.Exit(1)
	}
}

func agentSMBShares(shares []config.SMB) []agentfiles.SMBConfig {
	configs := make([]agentfiles.SMBConfig, 0, len(shares))
	for _, share := range shares {
		configs = append(configs, agentfiles.SMBConfig{
			ID:       share.ID,
			Name:     share.Name,
			Host:     share.Host,
			Share:    share.Share,
			Username: share.Username,
			Password: share.Password,
			Domain:   share.Domain,
			Policy:   agentFilePolicy(share.Policy),
		})
	}
	return configs
}

func agentLocalFolders(folders []config.LocalFolder) []agentfiles.LocalConfig {
	configs := make([]agentfiles.LocalConfig, 0, len(folders))
	for _, folder := range folders {
		configs = append(configs, agentfiles.LocalConfig{
			ID:     folder.ID,
			Name:   folder.Name,
			Root:   folder.Root,
			Policy: agentFilePolicy(folder.Policy),
		})
	}
	return configs
}

func agentFilePolicy(policy config.FileAccessPolicy) agentfiles.AccessPolicy {
	return agentfiles.AccessPolicy{
		Read:            policy.Read,
		Write:           policy.Write,
		Delete:          policy.Delete,
		AllowedPrefixes: append([]string(nil), policy.AllowedPrefixes...),
		BlockedPrefixes: append([]string(nil), policy.BlockedPrefixes...),
		MaxUploadBytes:  policy.MaxUploadBytes,
	}
}
