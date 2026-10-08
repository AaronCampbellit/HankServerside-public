package cloud

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestSanitizeNoteAttachmentHTMLRemovesActiveContent(t *testing.T) {
	source := `<!doctype html><html><head><style>body{color:red;background:url(https://evil.test/x);width:expression(alert(1))}</style><script>alert(1)</script></head><body onload="steal()"><h1>Preview</h1><iframe src="https://evil.test"></iframe><form action="/steal"><input name="secret"></form><img src="https://evil.test/pixel" onerror="steal()"><img src="data:image/png;base64,AAAA"><a href="javascript:steal()">bad</a></body></html>`
	got, err := sanitizeNoteAttachmentHTML(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, forbidden := range []string{"script", "alert(1)", "iframe", "form", "input", "onload", "onerror", "javascript:", "https://evil.test", "url(", "expression("} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("sanitized HTML contains %q: %s", forbidden, text)
		}
	}
	for _, wanted := range []string{"<!DOCTYPE html>", "<h1>Preview</h1>", "data:image/png;base64,AAAA"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("sanitized HTML missing %q: %s", wanted, text)
		}
	}
}

func TestServeHTMLNoteAttachmentSupportsLockedPreviewAndDownload(t *testing.T) {
	root := t.TempDir()
	body := []byte(`<h1>Hello</h1><script>alert(1)</script>`)
	if err := os.WriteFile(filepath.Join(root, "page.html"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{noteAttachmentRoot: root}
	attachment := domain.NoteAttachment{Filename: "page.html", ContentType: "text/html", SizeBytes: int64(len(body)), StorageKey: "page.html", UpdatedAt: time.Now()}

	previewRequest := httptest.NewRequest(http.MethodGet, "/attachment?disposition=preview", nil)
	previewResponse := httptest.NewRecorder()
	server.serveNoteAttachment(previewResponse, previewRequest, attachment)
	if previewResponse.Code != http.StatusOK || !strings.HasPrefix(previewResponse.Header().Get("Content-Disposition"), "inline;") {
		t.Fatalf("preview status/headers = %d/%#v", previewResponse.Code, previewResponse.Header())
	}
	if csp := previewResponse.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
	if strings.Contains(previewResponse.Body.String(), "script") {
		t.Fatalf("preview body was not sanitized: %s", previewResponse.Body.String())
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/attachment?disposition=download", nil)
	downloadResponse := httptest.NewRecorder()
	server.serveNoteAttachment(downloadResponse, downloadRequest, attachment)
	if !strings.HasPrefix(downloadResponse.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download disposition = %q", downloadResponse.Header().Get("Content-Disposition"))
	}
	if downloadResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download nosniff = %q", downloadResponse.Header().Get("X-Content-Type-Options"))
	}
	downloaded, err := io.ReadAll(downloadResponse.Result().Body)
	if err != nil || string(downloaded) != string(body) {
		t.Fatalf("download = %q, err=%v", downloaded, err)
	}
}

func TestServeHTMLNoteAttachmentUsesBoundedProcessingSlot(t *testing.T) {
	root := t.TempDir()
	body := []byte(`<h1>Bounded</h1>`)
	if err := os.WriteFile(filepath.Join(root, "bounded.html"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{noteAttachmentRoot: root}
	attachment := domain.NoteAttachment{Filename: "bounded.html", ContentType: "text/html", SizeBytes: int64(len(body)), StorageKey: "bounded.html", UpdatedAt: time.Now()}
	request := httptest.NewRequest(http.MethodGet, "/attachment?disposition=preview", nil)
	response := httptest.NewRecorder()

	mcpAttachmentProcessingSlots <- struct{}{}
	done := make(chan struct{})
	go func() {
		server.serveNoteAttachment(response, request, attachment)
		close(done)
	}()
	select {
	case <-done:
		<-mcpAttachmentProcessingSlots
		t.Fatal("HTML sanitization bypassed processing limit")
	case <-time.After(50 * time.Millisecond):
	}
	<-mcpAttachmentProcessingSlots
	select {
	case <-done:
		if response.Code != http.StatusOK {
			t.Fatalf("preview after slot release = %d", response.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTML preview did not resume after slot release")
	}
}
