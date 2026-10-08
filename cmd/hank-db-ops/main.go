package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dropfile/HankServerside/internal/config"
	"github.com/dropfile/HankServerside/internal/storageops"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg, err := config.LoadDBOps()
	if err != nil {
		logger.Error("failed to load db-ops config", "error", err)
		os.Exit(1)
	}

	worker := storageops.NewWorker(storageops.WorkerOptions{
		StateDir:             cfg.StateDir,
		LogDir:               cfg.LogDir,
		IntentSecret:         cfg.IntentSecret,
		RepoCipherPass:       cfg.RepoCipherPass,
		DatabaseURL:          cfg.DatabaseURL,
		Stanza:               cfg.Stanza,
		PGDataPath:           cfg.PGDataPath,
		RestoreDataPath:      cfg.RestoreDataPath,
		RestoreDatabaseURL:   cfg.RestoreDatabaseURL,
		NoteAttachmentDir:    cfg.NoteAttachmentDir,
		AttachmentRestoreDir: cfg.AttachmentRestoreDir,
		ComposeFile:          cfg.ComposeFile,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if os.Getenv("HANK_DB_OPS_RUN_ONCE") == "true" {
		if err := worker.RunOnce(ctx); err != nil {
			logger.Error("db-ops run once failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("db-ops worker failed", "error", err)
		os.Exit(1)
	}
}
