package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistantFolderReceiptRecoveryAndScope(t *testing.T) {
	directory, root := t.TempDir(), t.TempDir()
	journal, err := operations.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient("", "agent", "", "", "", nil, agentfiles.New(root), nil, nil, nil)
	client.registeredHomeID = "home"
	client.SetOperationJournal(journal)
	identity := protocol.AssistantOperationIdentity{JournalEpoch: journal.Epoch(), OperationID: "folder-operation", ActionDigest: strings.Repeat("a", 64), HomeID: "home", UserID: "user", AgentID: "agent", Tool: "files.create_folder", ToolVersion: 1}
	args, _ := json.Marshal(protocol.AssistantFolderOperationArguments{SourceID: agentfiles.LocalSourceID, Path: "new"})
	request := protocol.AssistantOperationRequest{Identity: identity, Arguments: args}
	invoke := func(command string, request protocol.AssistantOperationRequest) (protocol.AssistantOperationStatusResponse, error) {
		body, _ := json.Marshal(request)
		return client.executeAssistantOperation(context.Background(), protocol.Envelope{HomeID: "home", AgentID: "agent"}, protocol.RoutedCommand{Command: command, Body: body})
	}
	receipt, err := invoke(protocol.CommandAssistantOperationExecute, request)
	if err != nil || receipt.Outcome != "confirmed" {
		t.Fatalf("create: %#v %v", receipt, err)
	}
	journal.Close()
	journal, err = operations.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	client.SetOperationJournal(journal)
	// Removing the observed object proves recovery uses the immutable receipt,
	// not another create attempt following a lost server acknowledgement.
	if err := os.Remove(filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}
	replay, err := invoke(protocol.CommandAssistantOperationExecute, request)
	if err != nil || replay.Outcome != "confirmed" {
		t.Fatal("lost confirmed receipt")
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatal("replayed completed operation")
	}
	unknown := request
	unknown.Identity.OperationID = "unknown-operation"
	if _, fresh, err := journal.Begin(unknown.Identity); err != nil || !fresh {
		t.Fatal("cannot prepare crash intent")
	}
	result, err := invoke(protocol.CommandAssistantOperationExecute, unknown)
	if err != nil || result.Outcome != "unknown" {
		t.Fatal("uncertain intent replayed")
	}
	for _, mutate := range []func(*protocol.AssistantOperationIdentity){func(i *protocol.AssistantOperationIdentity) { i.HomeID = "other" }, func(i *protocol.AssistantOperationIdentity) { i.AgentID = "other" }, func(i *protocol.AssistantOperationIdentity) { i.JournalEpoch = strings.Repeat("0", 32) }, func(i *protocol.AssistantOperationIdentity) { i.DeviceID = "device" }} {
		forged := request
		mutate(&forged.Identity)
		if _, err := invoke(protocol.CommandAssistantOperationExecute, forged); err == nil {
			t.Fatal("forged identity accepted")
		}
	}
	missing := request
	missing.Identity.OperationID = "missing-operation"
	result, err = invoke(protocol.CommandAssistantOperationStatus, missing)
	if err != nil || result.Outcome != "not_found" {
		t.Fatal("same-store status unavailable")
	}
}

func TestAssistantUploadUsesVerifiedStageAndNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	journal, err := operations.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	client := NewClient("", "agent", "", "", "", nil, agentfiles.New(root), nil, nil, nil)
	client.registeredHomeID = "home"
	client.SetOperationJournal(journal)
	content := []byte("exact uploaded bytes")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	identity := protocol.AssistantOperationIdentity{JournalEpoch: journal.Epoch(), OperationID: "upload-one", ActionDigest: strings.Repeat("a", 64), HomeID: "home", UserID: "user", AgentID: "agent", Tool: "files.upload", ToolVersion: 1}
	invoke := func(identity protocol.AssistantOperationIdentity) (protocol.AssistantOperationStatusResponse, error) {
		args, _ := json.Marshal(protocol.AssistantUploadOperationArguments{SourceID: agentfiles.LocalSourceID, Path: "exact.txt", ChecksumSHA256: digest, SizeBytes: int64(len(content))})
		body, _ := json.Marshal(protocol.AssistantOperationRequest{Identity: identity, Arguments: args})
		return client.executeAssistantOperation(context.Background(), protocol.Envelope{HomeID: "home", AgentID: "agent"}, protocol.RoutedCommand{Command: protocol.CommandAssistantOperationExecute, Body: body})
	}
	if _, err := invoke(identity); err == nil {
		t.Fatal("missing stage dispatched")
	}
	if _, err := os.Stat(filepath.Join(root, "exact.txt")); !os.IsNotExist(err) {
		t.Fatal("missing stage created file")
	}
	if _, err := journal.StageChunk(identity, digest, int64(len(content)), 0, content); err != nil {
		t.Fatal(err)
	}
	result, err := invoke(identity)
	if err != nil || result.Outcome != "confirmed" {
		t.Fatalf("upload failed: %v %#v", err, result)
	}
	actual, err := os.ReadFile(filepath.Join(root, "exact.txt"))
	if err != nil || string(actual) != string(content) {
		t.Fatal("destination differs")
	}
	if err := os.WriteFile(filepath.Join(root, "exact.txt"), []byte("later edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := invoke(identity); err != nil || result.Outcome != "confirmed" {
		t.Fatal("receipt not reused")
	}
	other := identity
	other.OperationID = "upload-two"
	if _, err := journal.StageChunk(other, digest, int64(len(content)), 0, content); err != nil {
		t.Fatal(err)
	}
	if result, err := invoke(other); err != nil || result.Outcome != "failed" {
		t.Fatal("existing file not rejected")
	}
	actual, err = os.ReadFile(filepath.Join(root, "exact.txt"))
	if err != nil || string(actual) != "later edit" {
		t.Fatal("retry overwrote destination")
	}
}
