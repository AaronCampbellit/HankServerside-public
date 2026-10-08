package cloud

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
)

const (
	maxMCPAttachmentChunkBytes        = 4 << 20
	maxMCPHTMLSourceBytes             = 256 << 10
	maxMCPImagePixels           int64 = 40_000_000
	maxMCPImagePreviewDimension       = 2048
	maxMCPImagePreviewBytes           = 5 << 20
)

var mcpAttachmentProcessingSlots = make(chan struct{}, 1)
var mcpAttachmentOpaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type mcpValidatedAttachment struct {
	ContentType    string
	SizeBytes      int64
	ChecksumSHA256 string
	Width          int
	Height         int
}

type mcpImagePreview struct {
	Data           []byte
	ContentType    string
	SizeBytes      int64
	ChecksumSHA256 string
}

func mcpAttachmentStagingKey(uploadID string) (string, error) {
	if !mcpAttachmentOpaqueIDPattern.MatchString(uploadID) || strings.HasPrefix(uploadID, ".") {
		return "", errors.New("invalid upload id")
	}
	return filepath.Join(".mcp-staging", uploadID+".part"), nil
}

func reconcileStagingFile(path string, committedBytes int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < committedBytes {
		return fmt.Errorf("staging file is shorter than committed offset")
	}
	if info.Size() == committedBytes {
		return nil
	}
	return file.Truncate(committedBytes)
}

func copyMCPAttachmentBytes(destination io.Writer, source io.Reader, currentBytes int64) (int64, error) {
	data, err := io.ReadAll(io.LimitReader(source, maxMCPAttachmentChunkBytes+1))
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, errors.New("attachment chunk is empty")
	}
	if len(data) > maxMCPAttachmentChunkBytes {
		return 0, errors.New("attachment chunk is too large")
	}
	if currentBytes < 0 || int64(len(data)) > maxNoteAttachmentBytes-currentBytes {
		return 0, errors.New("attachment is too large")
	}
	written, err := destination.Write(data)
	if err != nil {
		return int64(written), err
	}
	if written != len(data) {
		return int64(written), io.ErrShortWrite
	}
	return int64(written), nil
}

func validateMCPAttachment(path, declaredContentType string) (mcpValidatedAttachment, error) {
	declaredContentType = strings.ToLower(strings.TrimSpace(declaredContentType))
	if !supportedMCPAttachmentContentType(declaredContentType) {
		return mcpValidatedAttachment{}, errors.New("unsupported attachment content type")
	}
	file, err := os.Open(path)
	if err != nil {
		return mcpValidatedAttachment{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return mcpValidatedAttachment{}, err
	}
	if info.Size() <= 0 {
		return mcpValidatedAttachment{}, errors.New("attachment body is empty")
	}
	if info.Size() > maxNoteAttachmentBytes {
		return mcpValidatedAttachment{}, errors.New("attachment is too large")
	}

	peek := make([]byte, 512)
	peekCount, readErr := io.ReadFull(file, peek)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return mcpValidatedAttachment{}, readErr
	}
	peek = peek[:peekCount]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return mcpValidatedAttachment{}, err
	}

	detected := strings.ToLower(strings.TrimSpace(http.DetectContentType(peek)))
	if semicolon := strings.IndexByte(detected, ';'); semicolon >= 0 {
		detected = detected[:semicolon]
	}
	if declaredContentType == "text/html" {
		lowerPeek := strings.ToLower(strings.TrimSpace(string(peek)))
		if strings.HasPrefix(lowerPeek, "<svg") || (strings.HasPrefix(lowerPeek, "<?xml") && strings.Contains(lowerPeek, "<svg")) {
			return mcpValidatedAttachment{}, errors.New("svg is not supported")
		}
		if detected != "text/html" && detected != "text/plain" {
			return mcpValidatedAttachment{}, errors.New("attachment content does not match text/html")
		}
		reader := bufio.NewReader(file)
		for {
			r, size, runeErr := reader.ReadRune()
			if errors.Is(runeErr, io.EOF) {
				break
			}
			if runeErr != nil {
				return mcpValidatedAttachment{}, runeErr
			}
			if r == utf8.RuneError && size == 1 {
				return mcpValidatedAttachment{}, errors.New("html attachment is not valid UTF-8")
			}
		}
	} else {
		if detected != declaredContentType && !(declaredContentType == "image/jpeg" && detected == "image/jpg") {
			return mcpValidatedAttachment{}, fmt.Errorf("attachment content does not match %s", declaredContentType)
		}
		mcpAttachmentProcessingSlots <- struct{}{}
		defer func() { <-mcpAttachmentProcessingSlots }()
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return mcpValidatedAttachment{}, err
		}
		config, _, err := image.DecodeConfig(file)
		if err != nil {
			return mcpValidatedAttachment{}, fmt.Errorf("decode image metadata: %w", err)
		}
		if config.Width <= 0 || config.Height <= 0 || int64(config.Width) > maxMCPImagePixels/int64(config.Height) {
			return mcpValidatedAttachment{}, errors.New("image dimensions are unsafe")
		}
		if int64(config.Width)*int64(config.Height) > maxMCPImagePixels {
			return mcpValidatedAttachment{}, errors.New("image dimensions are unsafe")
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return mcpValidatedAttachment{}, err
		}
		if _, _, err := image.Decode(file); err != nil {
			return mcpValidatedAttachment{}, fmt.Errorf("decode image: %w", err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return mcpValidatedAttachment{}, err
		}
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return mcpValidatedAttachment{}, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return mcpValidatedAttachment{}, err
	}
	result := mcpValidatedAttachment{
		ContentType: declaredContentType, SizeBytes: info.Size(),
		ChecksumSHA256: hex.EncodeToString(hasher.Sum(nil)),
	}
	if declaredContentType != "text/html" {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return mcpValidatedAttachment{}, err
		}
		config, _, err := image.DecodeConfig(file)
		if err != nil {
			return mcpValidatedAttachment{}, err
		}
		result.Width, result.Height = config.Width, config.Height
	}
	return result, nil
}

func generateMCPImagePreview(path, contentType string) (mcpImagePreview, error) {
	if contentType == "text/html" || !supportedMCPAttachmentContentType(contentType) {
		return mcpImagePreview{}, errors.New("attachment is not a supported image")
	}
	mcpAttachmentProcessingSlots <- struct{}{}
	defer func() { <-mcpAttachmentProcessingSlots }()
	file, err := os.Open(path)
	if err != nil {
		return mcpImagePreview{}, err
	}
	imageValue, _, err := image.Decode(file)
	_ = file.Close()
	if err != nil {
		return mcpImagePreview{}, err
	}
	sourceBounds := imageValue.Bounds()
	width, height := boundedPreviewDimensions(sourceBounds.Dx(), sourceBounds.Dy(), maxMCPImagePreviewDimension)
	for {
		previewImage := resizeNearest(imageValue, width, height)
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, previewImage); err != nil {
			return mcpImagePreview{}, err
		}
		if buffer.Len() <= maxMCPImagePreviewBytes {
			sum := sha256.Sum256(buffer.Bytes())
			data := append([]byte(nil), buffer.Bytes()...)
			return mcpImagePreview{Data: data, ContentType: "image/png", SizeBytes: int64(len(data)), ChecksumSHA256: hex.EncodeToString(sum[:])}, nil
		}
		if width <= 1 && height <= 1 {
			return mcpImagePreview{}, errors.New("image preview exceeds size limit")
		}
		width = max(1, width*3/4)
		height = max(1, height*3/4)
	}
}

func boundedPreviewDimensions(width, height, limit int) (int, int) {
	if width <= limit && height <= limit {
		return width, height
	}
	if width >= height {
		return limit, max(1, height*limit/width)
	}
	return max(1, width*limit/height), limit
}

func resizeNearest(source image.Image, width, height int) *image.RGBA {
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	for y := 0; y < height; y++ {
		sourceY := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/width
			destination.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	return destination
}

func supportedMCPAttachmentContentType(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "text/html":
		return true
	default:
		return false
	}
}
