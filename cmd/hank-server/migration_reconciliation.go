package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/store"
)

func runAssistantIndexReconciliation(ctx context.Context, databaseURL string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("migrate reconcile-assistant-index", flag.ContinueOnError)
	flags.SetOutput(output)
	check := flags.Bool("check", false, "inspect the exact supported history without writes")
	apply := flags.Bool("apply", false, "reassign the verified assistant index from migration 35 to 39")
	backup := flags.String("backup-label", "", "verified pgBackRest backup retained before the maintenance window")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *check == *apply {
		return errors.New("specify exactly one of --check or --apply")
	}
	if *apply && !regexp.MustCompile(`^[0-9]{8}-[0-9]{6}F(_[0-9]{8}-[0-9]{6}[DI])?$`).MatchString(*backup) {
		return errors.New("--apply requires --backup-label for a verified retained database and attachment backup")
	}
	if strings.TrimSpace(databaseURL) == "" {
		return errors.New("HANK_CLOUD_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	report, err := store.ReconcileAssistantIndexMigration(ctx, databaseURL, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(report)
}
