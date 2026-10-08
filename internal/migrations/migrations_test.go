package migrations

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestCheckStatusesAcceptsLegacyVersionOneBaselineChecksum(t *testing.T) {
	t.Parallel()

	if err := CheckStatuses([]Status{{
		Version:    1,
		Name:       "baseline",
		Checksum:   "50f7f293c05c9e0636f07dc8ddba6e0a682ef58d31686c820719a411a07ca035",
		AppliedAt:  time.Now(),
		DurationMS: 0,
	}}); !errors.Is(err, ErrPendingMigrations) {
		t.Fatalf("CheckStatuses legacy baseline = %v, want ErrPendingMigrations for later migrations only", err)
	}
}

func TestHomeServiceProfileConstraintIncludesSupportedServiceTypes(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var constraintText strings.Builder
	for _, migration := range migrations {
		for _, statement := range migration.Statements {
			if strings.Contains(statement, "home_service_profiles_service_type_check") {
				constraintText.WriteString(statement)
				constraintText.WriteByte('\n')
			}
		}
	}
	body := constraintText.String()
	if body == "" {
		t.Fatal("home_service_profiles_service_type_check migration statement missing")
	}
	for _, serviceType := range []string{domain.ServiceTypeHomeAssistant, domain.ServiceTypeSMB, domain.ServiceTypeHermes} {
		if !strings.Contains(body, "'"+serviceType+"'") {
			t.Fatalf("home_service_profiles_service_type_check missing %q in:\n%s", serviceType, body)
		}
	}
}

func TestRequiredPostgresExtensionsAreCreatedByMigrations(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var body strings.Builder
	for _, migration := range migrations {
		for _, statement := range migration.Statements {
			body.WriteString(statement)
			body.WriteByte('\n')
		}
	}
	all := body.String()
	for _, extension := range []string{
		"vector",
		"pg_trgm",
		"pg_stat_statements",
		"pg_buffercache",
		"amcheck",
	} {
		if !strings.Contains(all, "CREATE EXTENSION IF NOT EXISTS "+extension) {
			t.Fatalf("required extension %q is not created by migrations", extension)
		}
	}
	for _, extension := range []string{
		"pgaudit",
		"pgmq",
		"pg_partman",
	} {
		if strings.Contains(all, "CREATE EXTENSION IF NOT EXISTS "+extension) {
			t.Fatalf("deferred extension %q should not be created by migrations", extension)
		}
	}
}

func TestLinuxAgentRolloutMigrationIsRegistered(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version == 31 && strings.Contains(migration.Name, "linux_agent_rollouts") {
			return
		}
	}
	t.Fatal("migration 31 linux_agent_rollouts is not registered")
}

func TestNoteNotebookIntegrityMigrationDefinesDatabaseRules(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	var body strings.Builder
	for _, migration := range migrations {
		if migration.Version != 26 {
			continue
		}
		for _, statement := range migration.Statements {
			body.WriteString(statement)
			body.WriteByte('\n')
		}
	}
	text := body.String()
	if text == "" {
		t.Fatal("migration 26 missing")
	}
	for _, required := range []string{
		"user_notes_owner_parent_fkey",
		"FOREIGN KEY (owner_user_id, parent_id)",
		"validate_user_note_parent",
		"protect_user_note_notebook_parent",
		"page_type <> 'notebook'",
		"deleted_at IS NOT NULL",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration 26 missing %q in:\n%s", required, text)
		}
	}
}

func TestNotePinningMigrationDefinesSyncedChildOnlyPins(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	var body strings.Builder
	for _, migration := range migrations {
		if migration.Version != 27 {
			continue
		}
		for _, statement := range migration.Statements {
			body.WriteString(statement)
			body.WriteByte('\n')
		}
	}
	text := body.String()
	if text == "" {
		t.Fatal("migration 27 missing")
	}
	for _, required := range []string{
		"pinned BOOLEAN NOT NULL DEFAULT FALSE",
		"user_notes_pinned_child_check",
		"NOT pinned",
		"parent_id IS NOT NULL",
		"page_type <> 'notebook'",
		"idx_user_notes_owner_parent_pinned_updated",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration 27 missing %q in:\n%s", required, text)
		}
	}
}

func TestUserDisplayNameMigrationDefinesPresentationOnlyField(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	var body strings.Builder
	for _, migration := range migrations {
		if migration.Version != 28 {
			continue
		}
		for _, statement := range migration.Statements {
			body.WriteString(statement)
			body.WriteByte('\n')
		}
	}
	text := body.String()
	if text == "" {
		t.Fatal("migration 28 missing")
	}
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT ''",
		"users_display_name_length_check",
		"char_length(display_name) <= 80",
		"users_display_name_trimmed_check",
		"display_name = btrim(display_name)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration 28 missing %q in:\n%s", required, text)
		}
	}
}

func TestMCPSettingsMigration(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version != 29 {
			continue
		}
		text := strings.Join(migration.Statements, "\n")
		for _, required := range []string{"CREATE TABLE IF NOT EXISTS mcp_settings", "home_id", "kanban_app_enabled", "updated_by"} {
			if !strings.Contains(text, required) {
				t.Fatalf("migration 29 missing %q in:\n%s", required, text)
			}
		}
		return
	}
	t.Fatal("migration 29 missing")
}

func TestMCPNoteAttachmentTransferMigrationDefinesBoundedSessions(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version != 32 {
			continue
		}
		text := strings.Join(migration.Statements, "\n")
		for _, required := range []string{
			"CREATE TABLE IF NOT EXISTS mcp_note_attachment_uploads",
			"preview_storage_key",
			"received_bytes <= 104857600",
			"declared_size_bytes <= 104857600",
			"target_kind IN ('note', 'kanban_card')",
			"status IN ('open', 'completed', 'aborted', 'failed', 'expired')",
			"mcp_note_attachment_uploads_owner_status_expiry_idx",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("migration 32 missing %q in:\n%s", required, text)
			}
		}
		return
	}
	t.Fatal("migration 32 missing")
}

func TestBrowserPWANotificationMigrationDefinesDurableInboxAndOutbox(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version != 33 {
			continue
		}
		text := strings.Join(migration.Statements, "\n")
		for _, required := range []string{
			"ADD COLUMN IF NOT EXISTS connector_enabled BOOLEAN NOT NULL DEFAULT TRUE",
			"ADD COLUMN IF NOT EXISTS quick_links_enabled BOOLEAN NOT NULL DEFAULT TRUE",
			"CREATE TABLE IF NOT EXISTS user_notifications",
			"category IN ('connector', 'quick_links', 'storage', 'notes', 'dashboard_entities')",
			"CREATE TABLE IF NOT EXISTS notification_event_receipts",
			"PRIMARY KEY (user_id, event_key)",
			"CREATE TABLE IF NOT EXISTS notification_source_states",
			"CREATE TABLE IF NOT EXISTS notification_source_events",
			"notification_source_events_pending_idx",
			"CREATE TABLE IF NOT EXISTS web_push_subscriptions",
			"endpoint_fingerprint TEXT NOT NULL UNIQUE",
			"encrypted_subscription TEXT NOT NULL",
			"CREATE TABLE IF NOT EXISTS web_push_deliveries",
			"state IN ('pending', 'claimed', 'delivered', 'cancelled', 'terminal')",
			"UNIQUE (notification_id, subscription_id)",
			"user_notifications_user_timeline_idx",
			"user_notifications_user_unread_idx",
			"user_notifications_retention_idx",
			"web_push_deliveries_due_idx",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("migration 33 missing %q in:\n%s", required, text)
			}
		}
		return
	}
	t.Fatal("migration 33 browser_pwa_notifications is not registered")
}

func TestNotificationAuditLinkMigrationDefinesNullableReference(t *testing.T) {
	t.Parallel()
	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version != 34 {
			continue
		}
		text := strings.Join(migration.Statements, "\n")
		for _, required := range []string{
			"ADD COLUMN IF NOT EXISTS audit_event_id TEXT REFERENCES audit_events(id) ON DELETE SET NULL",
			"user_notifications_audit_event_idx",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("migration 34 missing %q in:\n%s", required, text)
			}
		}
		return
	}
	t.Fatal("migration 34 notification_audit_links is not registered")
}

func TestAgentHealthNamingMigrationRewritesPersistedContracts(t *testing.T) {
	t.Parallel()
	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version != 35 {
			continue
		}
		text := strings.Join(migration.Statements, "\n")
		for _, required := range []string{
			"RENAME COLUMN connector_enabled TO agent_health_enabled",
			"SET category = 'agent_health'",
			"WHEN 'connector.offline' THEN 'agent.offline'",
			"WHEN 'connector.recovered' THEN 'agent.recovered'",
			"UPDATE notification_source_events",
			"UPDATE audit_events",
			"jsonb_array_elements_text(device.enabled_categories)",
			"category IN ('agent_health', 'quick_links', 'storage', 'notes', 'dashboard_entities')",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("migration 35 missing %q in:\n%s", required, text)
			}
		}
		return
	}
	t.Fatal("migration 35 agent_health_naming is not registered")
}

func TestAgentHealthNamingMigrationRewritesPersistedRowsPostgres(t *testing.T) {
	db, err := sql.Open("pgx", testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := EnsureTable(ctx, db); err != nil {
		t.Fatalf("ensure migration table: %v", err)
	}
	migrations, err := All()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	var naming Migration
	for _, migration := range migrations {
		if migration.Version == 35 {
			naming = migration
			break
		}
		if err := applyOne(ctx, db, migration); err != nil {
			t.Fatalf("apply prerequisite migration %d: %v", migration.Version, err)
		}
	}
	if naming.Version != 35 {
		t.Fatal("migration 35 is missing")
	}

	now := time.Now().UTC()
	statements := []string{
		`INSERT INTO users (id, email, password_hash, created_at, updated_at) VALUES ('usr_naming', 'naming@example.com', 'hash', $1, $1)`,
		`INSERT INTO homes (id, user_id, name, created_at, updated_at) VALUES ('home_naming', 'usr_naming', 'Naming', $1, $1)`,
		`INSERT INTO app_sessions (id, user_id, token_hash, expires_at, created_at) VALUES ('session_naming', 'usr_naming', 'session-hash', $1::timestamp + interval '1 day', $1)`,
		`INSERT INTO notification_settings (user_id, connector_enabled, quick_links_enabled, storage_enabled, notes_enabled, dashboard_entities_enabled, updated_at) VALUES ('usr_naming', false, true, true, true, true, $1)`,
		`INSERT INTO user_notifications (id, user_id, home_id, category, event_kind, severity, title, body, target_path, collapse_key, outcome, first_occurred_at, last_occurred_at, created_at) VALUES ('ntf_naming', 'usr_naming', 'home_naming', 'connector', 'connector.offline', 'warning', 'Offline', 'Offline', '/dashboard/agents/one', 'agent-one', 'offline', $1, $1, $1)`,
		`INSERT INTO notification_source_states (source_key, home_id, resource_id, observed_state, created_at, updated_at) VALUES ('agent-one', 'home_naming', 'agent-one', 'offline', $1, $1)`,
		`INSERT INTO notification_source_events (id, event_key, source_key, home_id, resource_id, event_kind, outcome, severity, occurred_at) VALUES ('source_naming', 'event-naming', 'agent-one', 'home_naming', 'agent-one', 'connector.recovered', 'recovered', 'info', $1)`,
		`INSERT INTO audit_events (id, occurred_at, home_id, event_type, severity) VALUES ('audit_naming', $1, 'home_naming', 'connector.offline', 'warning')`,
		`INSERT INTO apns_devices (user_id, session_id, device_id, token, environment, bundle_id, enabled_categories, created_at, updated_at, last_registered_at) VALUES ('usr_naming', 'session_naming', 'device-naming', 'token', 'sandbox', 'com.example.hank', '["connector", "notes"]', $1, $1, $1)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement, now); err != nil {
			t.Fatalf("seed legacy naming: %v\n%s", err, statement)
		}
	}
	if err := applyOne(ctx, db, naming); err != nil {
		t.Fatalf("apply naming migration: %v", err)
	}

	var category, notificationKind, sourceKind, auditKind, enabledCategories, constraintDefinition string
	var agentHealthEnabled bool
	if err := db.QueryRowContext(ctx, `SELECT category, event_kind FROM user_notifications WHERE id = 'ntf_naming'`).Scan(&category, &notificationKind); err != nil {
		t.Fatalf("read notification: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT event_kind FROM notification_source_events WHERE id = 'source_naming'`).Scan(&sourceKind); err != nil {
		t.Fatalf("read source event: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT event_type FROM audit_events WHERE id = 'audit_naming'`).Scan(&auditKind); err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT enabled_categories::text FROM apns_devices WHERE device_id = 'device-naming'`).Scan(&enabledCategories); err != nil {
		t.Fatalf("read APNS categories: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT agent_health_enabled FROM notification_settings WHERE user_id = 'usr_naming'`).Scan(&agentHealthEnabled); err != nil {
		t.Fatalf("read notification settings: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'user_notifications_category_check'`).Scan(&constraintDefinition); err != nil {
		t.Fatalf("read category constraint: %v", err)
	}
	if category != "agent_health" || notificationKind != "agent.offline" || sourceKind != "agent.recovered" || auditKind != "agent.offline" {
		t.Fatalf("persisted naming was not rewritten: category=%q notification=%q source=%q audit=%q", category, notificationKind, sourceKind, auditKind)
	}
	if agentHealthEnabled {
		t.Fatal("renamed notification setting did not preserve false value")
	}
	if enabledCategories != `["agent_health", "notes"]` {
		t.Fatalf("APNS categories = %s", enabledCategories)
	}
	if !strings.Contains(constraintDefinition, "agent_health") || strings.Contains(constraintDefinition, "connector") {
		t.Fatalf("category constraint was not replaced: %s", constraintDefinition)
	}
}

func TestAgentTypeMigrationEnforcesOnePrimaryPerHome(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var body strings.Builder
	for _, migration := range migrations {
		if migration.Version != 20 {
			continue
		}
		for _, statement := range migration.Statements {
			body.WriteString(statement)
			body.WriteByte('\n')
		}
	}
	text := body.String()
	for _, required := range []string{
		"ROW_NUMBER() OVER (PARTITION BY home_id",
		"CHECK (agent_type IN ('primary', 'worker'))",
		"CREATE UNIQUE INDEX IF NOT EXISTS agents_one_primary_per_home_idx",
		"WHERE agent_type = 'primary'",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("agent type migration missing %q in:\n%s", required, text)
		}
	}
}

func TestRemoteDesktopFoundationMigrationHasSecurityConstraints(t *testing.T) {
	t.Parallel()

	migrations, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var body strings.Builder
	for _, migration := range migrations {
		if migration.Version != 21 {
			continue
		}
		for _, statement := range migration.Statements {
			body.WriteString(statement)
			body.WriteByte('\n')
		}
	}
	text := body.String()
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS desktop_trust_roots",
		"CREATE TABLE IF NOT EXISTS desktop_identities",
		"CREATE TABLE IF NOT EXISTS desktop_sessions",
		"CREATE TABLE IF NOT EXISTS desktop_join_credentials",
		"CREATE TABLE IF NOT EXISTS desktop_session_events",
		"desktop_sessions_one_live_operator_idx",
		"credential_hash BYTEA NOT NULL UNIQUE",
		"CHECK (side IN ('browser', 'agent'))",
		"CHECK (state IN ('requested', 'offered', 'agent_ready', 'joining', 'active', 'reconnecting', 'denied', 'failed', 'expired', 'terminated'))",
		"CHECK (requested_permissions <@ ARRAY['desktop.view'",
		"CHECK (key_epoch > 0)",
		"FOREIGN KEY (home_id, agent_id) REFERENCES agents(home_id, id)",
		"FOREIGN KEY (home_id, operator_user_id, operator_device_identity_id)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration 21 missing %q", required)
		}
	}
}
