package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/config"
)

func TestLinuxReleaseVerifyRequiresExplicitValidDirectory(t *testing.T) {
	var output bytes.Buffer
	cfg := config.Cloud{LinuxAgentReleasePublicKey: "not-a-key"}
	for _, args := range [][]string{{"verify"}, {"verify", "--dir", t.TempDir()}, {"unknown"}} {
		err := runLinuxReleaseCommand(context.Background(), cfg, args, &output)
		if err == nil {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
	}
	if strings.Contains(output.String(), "not-a-key") {
		t.Fatal("command leaked key material")
	}
}
