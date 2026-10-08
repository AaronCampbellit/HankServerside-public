package main

import (
	"context"
	"io"
	"testing"
)

func TestAssistantIndexReconciliationRequiresExplicitModeAndBackup(t *testing.T) {
	for _, args := range [][]string{nil, {"--apply"}, {"--check", "--apply"}, {"--apply", "--backup-label=unverified"}, {"--check", "unexpected"}} {
		if err := runAssistantIndexReconciliation(context.Background(), "", args, io.Discard); err == nil {
			t.Fatalf("accepted unsafe args %v", args)
		}
	}
}
