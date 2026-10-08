package operations

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/dropfile/HankServerside/internal/protocol"
	"io"
	"strings"
	"testing"
)

func TestStageChunksResumeAndVerifyBeforeUse(t *testing.T) {
	dir := t.TempDir()
	journal, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity := protocol.AssistantOperationIdentity{JournalEpoch: journal.Epoch(), OperationID: "upload", ActionDigest: strings.Repeat("a", 64), HomeID: "home", UserID: "user", AgentID: "agent", Tool: "files.upload", ToolVersion: 1}
	content := []byte(strings.Repeat("content", 50000))
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	size := int64(len(content))
	if _, err := journal.StageChunk(identity, digest, size, 10, content[:10]); err == nil {
		t.Fatal("gap accepted")
	}
	if next, err := journal.StageChunk(identity, digest, size, 0, content[:MaxStageChunk]); err != nil || next != MaxStageChunk {
		t.Fatal("first chunk failed")
	}
	if _, err := journal.OpenStage(identity, digest, size); err == nil {
		t.Fatal("partial object exposed")
	}
	journal.Close()
	journal, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if next, err := journal.StageChunk(identity, digest, size, 0, content[:MaxStageChunk]); err != nil || next != MaxStageChunk {
		t.Fatal("lost acknowledgement cannot resume")
	}
	if _, err := journal.StageChunk(identity, digest, size, 0, []byte("different")); err == nil {
		t.Fatal("changed chunk accepted")
	}
	if next, err := journal.StageChunk(identity, digest, size, MaxStageChunk, content[MaxStageChunk:]); err != nil || next != size {
		t.Fatal("completion failed")
	}
	file, err := journal.OpenStage(identity, digest, size)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(actual) != string(content) {
		t.Fatal("verified bytes changed")
	}
	wrong := identity
	wrong.UserID = "other"
	if _, err := journal.OpenStage(wrong, digest, size); err == nil {
		t.Fatal("stage crossed identity")
	}
	bad := identity
	bad.OperationID = "bad-checksum"
	if _, err := journal.StageChunk(bad, strings.Repeat("b", 64), 3, 0, []byte("abc")); err == nil {
		t.Fatal("bad checksum exposed")
	}
	if _, err := journal.OpenStage(bad, strings.Repeat("b", 64), 3); err == nil {
		t.Fatal("corrupt stage opened")
	}
}
