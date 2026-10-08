package maintenance

import (
	"os"
	"strings"
	"testing"
)

func TestMaintenanceLogsNotificationCleanupCountsOnly(t *testing.T) {
	source, err := os.ReadFile("maintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, field := range []string{"user_notifications_deleted", "notification_source_events_deleted", "web_push_deliveries_deleted", "web_push_claims_reclaimed"} {
		if !strings.Contains(text, field) {
			t.Fatalf("maintenance diagnostics are missing %q", field)
		}
	}
	if strings.Contains(text, "Endpoint") || strings.Contains(text, "P256DH") || strings.Contains(text, "PrivateKey") {
		t.Fatal("maintenance diagnostics must not reference Web Push capability material")
	}
}
