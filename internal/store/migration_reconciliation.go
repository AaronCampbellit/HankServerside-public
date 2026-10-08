package store

import (
	"context"

	"github.com/dropfile/HankServerside/internal/migrations"
)

func ReconcileAssistantIndexMigration(ctx context.Context, databaseURL string, apply bool) (migrations.AssistantIndexReconciliation, error) {
	db, err := openDatabasePool(databaseURL)
	if err != nil {
		return migrations.AssistantIndexReconciliation{}, err
	}
	defer db.Close()
	return migrations.ReconcileAssistantIndex(ctx, db, apply)
}
