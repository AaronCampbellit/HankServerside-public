package cloud

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDesktopAuditNeverIncludesSensitivePayloads(t *testing.T) {
	metadata := desktopAuditMetadata("operator_device", "did_0001", "fingerprint", 1, []string{"operator.approve"}, "approved")
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_key", "recovery_secret", "recovery_envelope", "join_credential", "clipboard", "keystroke", "video", "ciphertext", "certificate", "signature", "challenge"} {
		if bytes.Contains(bytes.ToLower(encoded), []byte(forbidden)) {
			t.Fatalf("audit metadata contains %q: %s", forbidden, encoded)
		}
	}
}
