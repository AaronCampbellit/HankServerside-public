package cloud

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cloudReleaseArtifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type cloudReleaseManifest struct {
	Version     string                 `json:"version"`
	PublishedAt time.Time              `json:"published_at,omitempty"`
	Artifacts   []cloudReleaseArtifact `json:"artifacts"`
	Signature   string                 `json:"signature,omitempty"`
}

func TestConfigureLinuxAgentReleaseServesOnlyVerifiedArtifacts(t *testing.T) {
	dir := t.TempDir()
	artifacts := buildCloudReleaseBinaries(t, dir)
	publicKey, raw := signCloudRelease(t, dir, artifacts)

	server := &Server{}
	if err := server.ConfigureLinuxAgentRelease(dir, publicKey); err != nil {
		t.Fatal(err)
	}

	name := "hankagent-linux-amd64"
	digest := sha256.Sum256(artifacts[name])
	for _, test := range []struct {
		path string
		want []byte
	}{
		{"/install/linux-release/" + name, artifacts[name]},
		{"/install/linux-release/" + name + ".sha256", []byte(hex.EncodeToString(digest[:]) + "  " + name + "\n")},
		{"/install/linux-release/release.json", raw},
	} {
		recorder := httptest.NewRecorder()
		server.handleLinuxAgentRelease(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", test.path, recorder.Code)
		}
		if string(recorder.Body.Bytes()) != string(test.want) {
			t.Fatalf("GET %s returned different bytes", test.path)
		}
	}

	recorder := httptest.NewRecorder()
	server.handleLinuxAgentRelease(recorder, httptest.NewRequest(http.MethodGet, "/install/linux-release/not-approved", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unapproved artifact status = %d, want 404", recorder.Code)
	}
}

func TestConfigureLinuxAgentReleaseRejectsTamperedArtifact(t *testing.T) {
	dir := t.TempDir()
	artifacts := map[string][]byte{
		"hankagent-linux-amd64": []byte("approved amd64"),
		"hankagent-linux-arm64": []byte("approved arm64"),
	}
	publicKey, _ := signCloudRelease(t, dir, artifacts)
	if err := os.WriteFile(filepath.Join(dir, "hankagent-linux-amd64"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}

	server := &Server{}
	if err := server.ConfigureLinuxAgentRelease(dir, publicKey); err == nil {
		t.Fatal("expected tampered artifact to be rejected")
	}
}

func buildCloudReleaseBinaries(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for _, arch := range []string{"amd64", "arm64"} {
		name := "hankagent-linux-" + arch
		output := filepath.Join(dir, name)
		ldflags := "-X main.buildSourceCommit=hank-source-commit:" + strings.Repeat("a", 40)
		command := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", output, "./cmd/hank-agent")
		command.Dir = filepath.Join("..", "..")
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		if combined, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s fixture: %v\n%s", arch, err, combined)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		result[name] = data
	}
	return result
}

func signCloudRelease(t *testing.T, dir string, artifacts map[string][]byte) (string, []byte) {
	t.Helper()
	manifest := cloudReleaseManifest{Version: "0.3.0", PublishedAt: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "hankagent-linux-" + arch
		data := artifacts[name]
		digest := sha256.Sum256(data)
		manifest.Artifacts = append(manifest.Artifacts, cloudReleaseArtifact{OS: "linux", Arch: arch, Kind: "binary", URL: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))})
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
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
	if err := os.WriteFile(filepath.Join(dir, "release.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(publicKey), raw
}
