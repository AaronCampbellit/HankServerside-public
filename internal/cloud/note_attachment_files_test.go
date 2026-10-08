package cloud

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReconcileStagingFileTruncatesUncommittedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.part")
	if err := os.WriteFile(path, []byte("committed-tail"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileStagingFile(path, 9); err != nil {
		t.Fatalf("reconcileStagingFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "committed" {
		t.Fatalf("staging bytes = %q, want committed", got)
	}
}

func TestMCPAttachmentStagingKeyRejectsPathInput(t *testing.T) {
	if got, err := mcpAttachmentStagingKey("mcpup_abc-123"); err != nil || got != filepath.Join(".mcp-staging", "mcpup_abc-123.part") {
		t.Fatalf("valid staging key = %q, err=%v", got, err)
	}
	for _, value := range []string{"../escape", "nested/file", "", ".hidden"} {
		if _, err := mcpAttachmentStagingKey(value); err == nil {
			t.Fatalf("mcpAttachmentStagingKey(%q) succeeded", value)
		}
	}
}

func TestValidateMCPAttachmentRejectsSpoofedAndActiveTypes(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "png declared as html", contentType: "text/html", body: pngBytes(t, 2, 2)},
		{name: "invalid utf8 html", contentType: "text/html", body: []byte{0xff, 0xfe}},
		{name: "svg alias", contentType: "text/html", body: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{name: "html declared as png", contentType: "image/png", body: []byte("<!doctype html><p>nope</p>")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "attachment")
			if err := os.WriteFile(path, test.body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := validateMCPAttachment(path, test.contentType); err == nil {
				t.Fatal("validateMCPAttachment succeeded, want error")
			}
		})
	}
}

func TestValidateMCPAttachmentAcceptsStaticHTMLAndPNG(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "site.html")
	if err := os.WriteFile(htmlPath, []byte("<!doctype html><style>body{color:red}</style><p>Hello</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	htmlInfo, err := validateMCPAttachment(htmlPath, "text/html")
	if err != nil {
		t.Fatalf("validate html: %v", err)
	}
	if htmlInfo.SizeBytes != 57 || len(htmlInfo.ChecksumSHA256) != 64 {
		t.Fatalf("html info = %#v", htmlInfo)
	}

	pngPath := filepath.Join(t.TempDir(), "image.png")
	body := pngBytes(t, 4, 3)
	if err := os.WriteFile(pngPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	pngInfo, err := validateMCPAttachment(pngPath, "image/png")
	if err != nil {
		t.Fatalf("validate png: %v", err)
	}
	if pngInfo.Width != 4 || pngInfo.Height != 3 || pngInfo.SizeBytes != int64(len(body)) {
		t.Fatalf("png info = %#v", pngInfo)
	}
}

func TestValidateMCPImageUsesBoundedProcessingSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, pngBytes(t, 4, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	mcpAttachmentProcessingSlots <- struct{}{}
	done := make(chan error, 1)
	go func() {
		_, err := validateMCPAttachment(path, "image/png")
		done <- err
	}()
	select {
	case err := <-done:
		<-mcpAttachmentProcessingSlots
		t.Fatalf("image validation bypassed processing limit: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	<-mcpAttachmentProcessingSlots
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("validation after slot release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("image validation did not resume after slot release")
	}
}

func TestGenerateMCPImagePreviewBoundsDimensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide.png")
	if err := os.WriteFile(path, pngBytes(t, 2050, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := generateMCPImagePreview(path, "image/png")
	if err != nil {
		t.Fatalf("generateMCPImagePreview: %v", err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(preview.Data))
	if err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if config.Width > maxMCPImagePreviewDimension || config.Height > maxMCPImagePreviewDimension {
		t.Fatalf("preview dimensions = %dx%d", config.Width, config.Height)
	}
	if preview.ContentType != "image/png" || preview.SizeBytes != int64(len(preview.Data)) || len(preview.ChecksumSHA256) != 64 {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestCopyMCPAttachmentBytesRejectsCumulativeLimit(t *testing.T) {
	var destination bytes.Buffer
	written, err := copyMCPAttachmentBytes(&destination, strings.NewReader("ab"), maxNoteAttachmentBytes-1)
	if err == nil || written != 0 || destination.Len() != 0 {
		t.Fatalf("written=%d len=%d err=%v", written, destination.Len(), err)
	}
}

func TestMCPAttachmentExactHundredMiBChunkReconstruction(t *testing.T) {
	chunk := bytes.Repeat([]byte("hank"), maxMCPAttachmentChunkBytes/4)
	actual := sha256.New()
	expected := sha256.New()
	var total int64
	for total < maxNoteAttachmentBytes {
		written, err := copyMCPAttachmentBytes(actual, bytes.NewReader(chunk), total)
		if err != nil {
			t.Fatalf("chunk at %d: %v", total, err)
		}
		total += written
		_, _ = expected.Write(chunk)
	}
	if total != maxNoteAttachmentBytes || !bytes.Equal(actual.Sum(nil), expected.Sum(nil)) {
		t.Fatalf("reconstruction total/hash mismatch: %d", total)
	}
	if written, err := copyMCPAttachmentBytes(actual, strings.NewReader("x"), total); err == nil || written != 0 {
		t.Fatalf("100 MiB plus one byte accepted: written=%d err=%v", written, err)
	}
}

func TestValidateMCPAttachmentAcceptsHundredMiBAndRejectsPlusOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maximum.html")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("a"), maxMCPAttachmentChunkBytes)
	for written := int64(0); written < maxNoteAttachmentBytes; written += int64(len(chunk)) {
		if _, err := file.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	validated, err := validateMCPAttachment(path, "text/html")
	if err != nil || validated.SizeBytes != maxNoteAttachmentBytes {
		t.Fatalf("100 MiB validation = %#v, err=%v", validated, err)
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := validateMCPAttachment(path, "text/html"); err == nil {
		t.Fatal("100 MiB plus one byte validated")
	}
}

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x66, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buffer.Bytes()
}
