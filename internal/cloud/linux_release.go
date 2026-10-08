package cloud

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/linuxrelease"
)

// ConfigureLinuxAgentRelease verifies the signed manifest and every Linux
// binary before making the immutable release available to installer clients.
// An invalid configured release is a startup error; private signing keys never
// belong on the server.
func (s *Server) ConfigureLinuxAgentRelease(dir, publicKeyBase64 string) error {
	dir = strings.TrimSpace(dir)
	publicKeyBase64 = strings.TrimSpace(publicKeyBase64)
	if dir == "" && publicKeyBase64 == "" {
		s.linuxAgentRelease = nil
		return nil
	}
	if dir == "" || publicKeyBase64 == "" {
		return errors.New("Linux agent release directory and public key must both be configured")
	}
	release, err := linuxrelease.Load(dir, publicKeyBase64)
	if err != nil {
		return err
	}
	s.linuxAgentRelease = release
	return nil
}

func (s *Server) handleLinuxAgentRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.linuxAgentRelease == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/install/linux-release/")
	if name == "release.json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(s.linuxAgentRelease.Manifest()))
		return
	}
	if strings.HasSuffix(name, ".sha256") {
		artifactName := strings.TrimSuffix(name, ".sha256")
		artifact, ok := s.linuxAgentRelease.Artifact(artifactName)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, artifact.SHA256+"  "+artifact.Name+"\n")
		return
	}
	artifact, ok := s.linuxAgentRelease.Artifact(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, artifact.Name, time.Time{}, bytes.NewReader(artifact.Data))
}
