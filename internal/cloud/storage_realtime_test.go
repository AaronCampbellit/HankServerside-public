package cloud

import (
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/storageops"
)

func TestStorageRealtimePayloadRedactsSensitiveData(t *testing.T) {
	payload := storageRealtimePayload(storageops.Event{
		ID:          "sto_1",
		Operation:   storageops.EventOperationBackup,
		Status:      storageops.EventStatusFailed,
		Severity:    storageops.EventSeverityError,
		Message:     "backup failed for postgres://hank:db-secret@postgres/hank with repo1-cipher-pass=cipher-secret",
		BackupLabel: "20260430-010101F",
		Details: map[string]any{
			"output": "token=raw-token",
		},
	})

	if _, ok := payload["details"]; ok {
		t.Fatal("storage websocket payload should not include event details")
	}
	message, _ := payload["message"].(string)
	for _, leaked := range []string{"db-secret", "cipher-secret", "raw-token"} {
		if strings.Contains(message, leaked) {
			t.Fatalf("storage websocket payload leaked %q: %+v", leaked, payload)
		}
	}
	if payload["backup_label"] != "20260430-010101F" {
		t.Fatalf("backup label missing from payload: %+v", payload)
	}
}
