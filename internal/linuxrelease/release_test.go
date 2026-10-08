package linuxrelease

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureArtifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type fixtureManifest struct {
	Version     string            `json:"version"`
	PublishedAt time.Time         `json:"published_at,omitempty"`
	Artifacts   []fixtureArtifact `json:"artifacts"`
	Signature   string            `json:"signature,omitempty"`
}

func TestSourceCommitFromEmbeddedMarker(t *testing.T) {
	commit := strings.Repeat("a", 40)
	got, err := sourceCommitFromBinary([]byte("prefix hank-source-commit:" + commit + " suffix"))
	if err != nil {
		t.Fatal(err)
	}
	if got != commit {
		t.Fatalf("source commit = %q, want %q", got, commit)
	}
}

func TestLoadAcceptsSignedRelativeArtifacts(t *testing.T) {
	dir, publicKey := writeSignedReleaseFixture(t, "0.3.0", map[string][]byte{
		"hankagent-linux-amd64": []byte("amd64 binary"),
		"hankagent-linux-arm64": []byte("arm64 binary"),
	})
	stubSourceCommits(t, map[string]string{
		"amd64 binary": strings.Repeat("a", 40),
		"arm64 binary": strings.Repeat("a", 40),
	})

	release, err := Load(dir, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if release.Version() != "0.3.0" {
		t.Fatalf("version = %q", release.Version())
	}
	if release.SourceCommit() != strings.Repeat("a", 40) {
		t.Fatalf("source commit = %q", release.SourceCommit())
	}
	artifact, ok := release.Artifact("hankagent-linux-amd64")
	if !ok || string(artifact.Data) != "amd64 binary" {
		t.Fatalf("artifact = %#v, %v", artifact, ok)
	}
	artifact.Data[0] = 'X'
	again, _ := release.Artifact("hankagent-linux-amd64")
	if string(again.Data) != "amd64 binary" {
		t.Fatal("release artifact bytes were mutable")
	}
}

func TestLoadRejectsInvalidReleaseContents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fixtureManifest, string)
		trail  string
		want   string
	}{
		{name: "path traversal", mutate: func(m *fixtureManifest, _ string) { m.Artifacts[0].URL = "../hankagent-linux-amd64" }, want: "filename"},
		{name: "missing architecture", mutate: func(m *fixtureManifest, _ string) { m.Artifacts = m.Artifacts[:1] }, want: "arm64"},
		{name: "hash mismatch", mutate: func(m *fixtureManifest, _ string) { m.Artifacts[0].SHA256 = strings.Repeat("0", 64) }, want: "signed metadata"},
		{name: "trailing JSON", trail: `{}`, want: "trailing JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, publicKey := writeCustomReleaseFixture(t, "0.3.0", defaultFixtureFiles(), tc.mutate, tc.trail)
			stubSourceCommits(t, map[string]string{"amd64 binary": strings.Repeat("a", 40), "arm64 binary": strings.Repeat("a", 40)})
			_, err := Load(dir, publicKey)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadRejectsBadSignatureAndMismatchedSourceCommits(t *testing.T) {
	dir, publicKey := writeSignedReleaseFixture(t, "0.3.0", map[string][]byte{
		"hankagent-linux-amd64": []byte("amd64 binary"),
		"hankagent-linux-arm64": []byte("arm64 binary"),
	})
	raw, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-2] ^= 1
	if err := os.WriteFile(filepath.Join(dir, "release.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, publicKey); err == nil {
		t.Fatal("invalid signature accepted")
	}

	dir, publicKey = writeSignedReleaseFixture(t, "0.3.0", map[string][]byte{
		"hankagent-linux-amd64": []byte("amd64 binary"),
		"hankagent-linux-arm64": []byte("arm64 binary"),
	})
	stubSourceCommits(t, map[string]string{
		"amd64 binary": strings.Repeat("a", 40),
		"arm64 binary": strings.Repeat("b", 40),
	})
	if _, err := Load(dir, publicKey); err == nil || !strings.Contains(err.Error(), "source commit") {
		t.Fatalf("mismatched source commit error = %v", err)
	}
}

func writeCustomReleaseFixture(t *testing.T, version string, files map[string][]byte, mutate func(*fixtureManifest, string), trail string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	manifest := fixtureManifest{Version: version}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "hankagent-linux-" + arch
		data := files[name]
		digest := sha256.Sum256(data)
		manifest.Artifacts = append(manifest.Artifacts, fixtureArtifact{OS: "linux", Arch: arch, Kind: "binary", URL: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))})
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if mutate != nil {
		mutate(&manifest, dir)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical))
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), append(raw, trail...), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, base64.StdEncoding.EncodeToString(publicKey)
}

func writeSignedReleaseFixture(t *testing.T, version string, files map[string][]byte) (string, string) {
	t.Helper()
	return writeCustomReleaseFixture(t, version, files, nil, "")
}

func defaultFixtureFiles() map[string][]byte {
	return map[string][]byte{
		"hankagent-linux-amd64": []byte("amd64 binary"),
		"hankagent-linux-arm64": []byte("arm64 binary"),
	}
}

func stubSourceCommits(t *testing.T, commits map[string]string) {
	t.Helper()
	previous := readSourceCommit
	readSourceCommit = func(data []byte) (string, error) {
		commit, ok := commits[string(data)]
		if !ok {
			return "", errors.New("unknown test binary")
		}
		return commit, nil
	}
	t.Cleanup(func() { readSourceCommit = previous })
}
