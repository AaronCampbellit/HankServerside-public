package cloud

import (
	"encoding/hex"
	"github.com/dropfile/HankServerside/internal/assistant/staging"
	"github.com/dropfile/HankServerside/internal/domain"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The existing Home API supplies authentication, membership and CSRF checks.
// Metadata stays in headers; attachment bytes are never JSON or log fields.
func (s *Server) handleAssistantStageUpload(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, session string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.assistantAI.ExecutionEnabled {
		writeJSON(w, 409, map[string]string{"error": "execution_version_unavailable"})
		return
	}
	size, err := strconv.ParseInt(r.Header.Get("X-Hank-Size-Bytes"), 10, 64)
	filename, nameErr := url.PathUnescape(r.Header.Get("X-Hank-Filename"))
	digest := r.Header.Get("X-Hank-Checksum-SHA256")
	_, digestErr := hex.DecodeString(digest)
	clientID := r.Header.Get("X-Hank-Attachment-ID")
	contentType := r.Header.Get("X-Hank-Content-Type")
	if err != nil || size < 1 || size > staging.MaxBytes || nameErr != nil || filename == "" || len(filename) > 1024 || !utf8.ValidString(filename) || strings.ContainsAny(filename, "\x00\r\n") || len(digest) != 64 || digestErr != nil || digest != strings.ToLower(digest) || clientID == "" || len(clientID) > 128 || contentType == "" || len(contentType) > 256 {
		http.Error(w, "invalid attachment metadata", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, size)
	stage, err := s.stageAssistantAttachment(r.Context(), home.ID, auth.User.ID, session, domain.AssistantStage{ClientAttachmentID: clientID, Filename: filename, ContentType: contentType, SizeBytes: size, ChecksumSHA256: digest}, r.Body)
	if err != nil {
		http.Error(w, "attachment could not be staged or verified", 400)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, stage)
}
