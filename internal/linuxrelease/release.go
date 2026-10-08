package linuxrelease

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxManifestBytes = 1 << 20
	maxArtifactBytes = 256 << 20
)

var (
	versionPattern        = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	fullCommitPattern     = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	embeddedCommitPattern = regexp.MustCompile(`hank-source-commit:([0-9a-f]{40}([0-9a-f]{24})?)`)
	readSourceCommit      = sourceCommitFromBinary
)

type manifestArtifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	Version     string             `json:"version"`
	PublishedAt time.Time          `json:"published_at,omitempty"`
	Artifacts   []manifestArtifact `json:"artifacts"`
	Signature   string             `json:"signature,omitempty"`
}

// Artifact is one immutable, verified release file.
type Artifact struct {
	Name   string
	SHA256 string
	Data   []byte
}

// Release contains only bytes approved by the signed manifest.
type Release struct {
	version      string
	sourceCommit string
	manifest     []byte
	files        map[string]Artifact
}

func (r *Release) Version() string { return r.version }

func (r *Release) SourceCommit() string { return r.sourceCommit }

func (r *Release) Manifest() []byte { return append([]byte(nil), r.manifest...) }

func (r *Release) Artifact(name string) (Artifact, bool) {
	artifact, ok := r.files[name]
	if !ok {
		return Artifact{}, false
	}
	artifact.Data = append([]byte(nil), artifact.Data...)
	return artifact, true
}

// Load verifies a complete two-architecture Linux release before returning
// any bytes to callers.
func Load(dir, publicKeyBase64 string) (*Release, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("Linux agent release directory is required")
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("Linux agent release public key is invalid")
	}

	manifestRaw, err := readRegularFile(filepath.Join(dir, "release.json"), maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("read Linux agent release manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestRaw))
	decoder.DisallowUnknownFields()
	var signed manifest
	if err := decoder.Decode(&signed); err != nil {
		return nil, fmt.Errorf("decode Linux agent release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("Linux agent release manifest contains trailing JSON")
	}
	if !versionPattern.MatchString(signed.Version) || len(signed.Artifacts) == 0 {
		return nil, errors.New("Linux agent release manifest is incomplete")
	}
	signature, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("Linux agent release manifest signature is invalid")
	}
	unsigned := signed
	unsigned.Signature = ""
	canonical, err := json.Marshal(unsigned)
	if err != nil {
		return nil, fmt.Errorf("canonicalize Linux agent release manifest: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), canonical, signature) {
		return nil, errors.New("Linux agent release manifest signature verification failed")
	}

	release := &Release{version: signed.Version, manifest: append([]byte(nil), manifestRaw...), files: make(map[string]Artifact)}
	seenArchitectures := make(map[string]struct{})
	for _, entry := range signed.Artifacts {
		if entry.OS != "linux" || entry.Kind != "binary" || (entry.Arch != "amd64" && entry.Arch != "arm64") {
			return nil, fmt.Errorf("unsupported Linux agent release artifact %s/%s/%s", entry.OS, entry.Arch, entry.Kind)
		}
		if _, exists := seenArchitectures[entry.Arch]; exists {
			return nil, fmt.Errorf("duplicate Linux agent release artifact linux/%s/binary", entry.Arch)
		}
		seenArchitectures[entry.Arch] = struct{}{}
		name := "hankagent-linux-" + entry.Arch
		if entry.URL != name {
			return nil, fmt.Errorf("Linux agent release artifact linux/%s/binary URL must be the filename %s", entry.Arch, name)
		}
		digestBytes, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(digestBytes) != sha256.Size || entry.Size <= 0 || entry.Size > maxArtifactBytes {
			return nil, fmt.Errorf("Linux agent release artifact linux/%s/binary metadata is invalid", entry.Arch)
		}
		data, err := readRegularFile(filepath.Join(dir, name), maxArtifactBytes)
		if err != nil {
			return nil, fmt.Errorf("read Linux agent release artifact linux/%s/binary: %w", entry.Arch, err)
		}
		actual := sha256.Sum256(data)
		if int64(len(data)) != entry.Size || !strings.EqualFold(hex.EncodeToString(actual[:]), entry.SHA256) {
			return nil, fmt.Errorf("Linux agent release artifact linux/%s/binary does not match its signed metadata", entry.Arch)
		}
		commit, err := readSourceCommit(data)
		if err != nil {
			return nil, fmt.Errorf("read Linux agent release artifact linux/%s/binary source commit: %w", entry.Arch, err)
		}
		if release.sourceCommit == "" {
			release.sourceCommit = commit
		} else if release.sourceCommit != commit {
			return nil, errors.New("Linux agent release binaries have different source commits")
		}
		release.files[name] = Artifact{Name: name, SHA256: strings.ToLower(entry.SHA256), Data: append([]byte(nil), data...)}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, ok := seenArchitectures[arch]; !ok {
			return nil, fmt.Errorf("Linux agent release manifest is missing the %s binary", arch)
		}
	}
	return release, nil
}

func readRegularFile(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("release file is not a regular file")
	}
	if info.Size() > limit {
		return nil, errors.New("release file is too large")
	}
	return os.ReadFile(name)
}

func sourceCommitFromBinary(data []byte) (string, error) {
	info, err := buildinfo.Read(bytes.NewReader(data))
	if err == nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				commit := strings.ToLower(strings.TrimSpace(setting.Value))
				if !fullCommitPattern.MatchString(commit) {
					return "", errors.New("binary does not contain a full Git source commit")
				}
				return commit, nil
			}
		}
	}
	for _, match := range embeddedCommitPattern.FindAllSubmatch(data, -1) {
		commit := string(match[1])
		if !fullCommitPattern.MatchString(commit) {
			continue
		}
		return commit, nil
	}
	return "", errors.New("binary does not contain a Git source commit")
}
