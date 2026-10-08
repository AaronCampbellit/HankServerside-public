package apps

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxManifestBytes = 64 * 1024
const maxSchemaBytes = 128 * 1024

type PackagePreview struct {
	Manifest Manifest
	Warnings []string
}

func PreviewArchive(path string) (PackagePreview, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return PackagePreview{}, err
	}
	defer reader.Close()

	rootPrefix, err := archiveRootPrefix(reader.File)
	if err != nil {
		return PackagePreview{}, err
	}
	paths := make(map[string]bool, len(reader.File))
	files := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().Mode()&os.ModeSymlink != 0 {
			return PackagePreview{}, fmt.Errorf("archive contains symlink entry %q", file.Name)
		}
		cleaned, isDir, keep, err := normalizeArchivePath(file.Name, rootPrefix)
		if err != nil {
			return PackagePreview{}, err
		}
		if !keep {
			continue
		}
		if file.FileInfo().IsDir() {
			isDir = true
		}
		if _, ok := paths[cleaned]; ok {
			return PackagePreview{}, fmt.Errorf("duplicate archive path %q", cleaned)
		}
		if err := validateArchivePathCollision(paths, cleaned, isDir); err != nil {
			return PackagePreview{}, err
		}
		paths[cleaned] = isDir
		if !isDir {
			files[cleaned] = file
		}
	}

	manifestFile, ok := files["app.json"]
	if !ok {
		return PackagePreview{}, fmt.Errorf("app.json is required")
	}

	manifest, err := decodeManifest(manifestFile)
	if err != nil {
		return PackagePreview{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return PackagePreview{}, err
	}

	if _, ok := files[manifest.Runtime.Command]; !ok {
		return PackagePreview{}, fmt.Errorf("runtime command %q is missing from archive", manifest.Runtime.Command)
	}
	for _, schemaPath := range referencedSchemaPaths(manifest) {
		schemaFile, ok := files[schemaPath]
		if !ok {
			return PackagePreview{}, fmt.Errorf("schema path %q is missing from archive", schemaPath)
		}
		if err := validateSchemaFile(schemaPath, schemaFile); err != nil {
			return PackagePreview{}, err
		}
	}

	return PackagePreview{Manifest: manifest}, nil
}

func validateSchemaFile(schemaPath string, file *zip.File) error {
	if file.UncompressedSize64 > maxSchemaBytes {
		return fmt.Errorf("schema path %q too large: %d bytes exceeds %d", schemaPath, file.UncompressedSize64, maxSchemaBytes)
	}
	reader, err := file.Open()
	if err != nil {
		return fmt.Errorf("open schema path %q: %w", schemaPath, err)
	}
	defer reader.Close()
	limited := &io.LimitedReader{R: reader, N: maxSchemaBytes + 1}
	decoder := json.NewDecoder(limited)
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil {
		return fmt.Errorf("schema path %q must be a valid JSON object: %w", schemaPath, err)
	}
	var trailing json.RawMessage
	switch err := decoder.Decode(&trailing); {
	case err == nil:
		return fmt.Errorf("schema path %q has trailing JSON token", schemaPath)
	case err == io.EOF:
		if limited.N == 0 {
			return fmt.Errorf("schema path %q too large: exceeds %d bytes", schemaPath, maxSchemaBytes)
		}
		return nil
	default:
		return fmt.Errorf("schema path %q must be valid JSON: %w", schemaPath, err)
	}
}

func validateArchivePathCollision(paths map[string]bool, cleaned string, isDir bool) error {
	for existing, existingIsDir := range paths {
		if !existingIsDir && archivePathHasParent(cleaned, existing) {
			return fmt.Errorf("archive path collision: %q is inside file path %q", cleaned, existing)
		}
		if !isDir && archivePathHasParent(existing, cleaned) {
			return fmt.Errorf("archive path collision: %q contains existing path %q", cleaned, existing)
		}
	}
	return nil
}

func archivePathHasParent(value string, parent string) bool {
	return strings.HasPrefix(value, parent+"/")
}

func cleanArchivePath(value string) (string, bool, error) {
	trimmed := strings.TrimSuffix(value, "/")
	cleaned, ok := cleanPackagePath(trimmed)
	if !ok {
		return "", false, fmt.Errorf("unsafe archive path %q", value)
	}
	return cleaned, strings.HasSuffix(value, "/"), nil
}

func archiveRootPrefix(files []*zip.File) (string, error) {
	root := ""
	for _, file := range files {
		cleaned, isDir, err := cleanArchivePath(file.Name)
		if err != nil {
			return "", err
		}
		if isArchiveMetadataPath(cleaned) {
			continue
		}
		parts := strings.Split(cleaned, "/")
		if len(parts) == 1 && !isDir && !file.FileInfo().IsDir() {
			return "", nil
		}
		candidate := parts[0]
		if root == "" {
			root = candidate
			continue
		}
		if root != candidate {
			return "", nil
		}
	}
	if root == "" {
		return "", nil
	}
	return root + "/", nil
}

func normalizeArchivePath(value string, rootPrefix string) (string, bool, bool, error) {
	cleaned, isDir, err := cleanArchivePath(value)
	if err != nil {
		return "", false, false, err
	}
	if isArchiveMetadataPath(cleaned) {
		return "", false, false, nil
	}
	if rootPrefix == "" {
		return cleaned, isDir, true, nil
	}
	root := strings.TrimSuffix(rootPrefix, "/")
	if cleaned == root && isDir {
		return "", false, false, nil
	}
	cleaned = strings.TrimPrefix(cleaned, rootPrefix)
	if cleaned == "" {
		return "", false, false, fmt.Errorf("unsafe archive path %q", value)
	}
	return cleaned, isDir, true, nil
}

func isArchiveMetadataPath(value string) bool {
	for _, part := range strings.Split(value, "/") {
		if part == "__MACOSX" || part == ".DS_Store" || strings.HasPrefix(part, "._") {
			return true
		}
	}
	return false
}

func decodeManifest(file *zip.File) (Manifest, error) {
	if file.UncompressedSize64 > maxManifestBytes {
		return Manifest{}, manifestTooLargeError(file.UncompressedSize64)
	}
	reader, err := file.Open()
	if err != nil {
		return Manifest{}, fmt.Errorf("open app.json: %w", err)
	}
	defer reader.Close()

	var manifest Manifest
	limited := &io.LimitedReader{R: reader, N: maxManifestBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&manifest); err != nil {
		if limited.N == 0 {
			return Manifest{}, manifestTooLargeError(0)
		}
		return Manifest{}, fmt.Errorf("decode app.json: %w", err)
	}
	var trailing json.RawMessage
	switch err := decoder.Decode(&trailing); {
	case err == nil:
		return Manifest{}, fmt.Errorf("decode app.json: trailing JSON token after manifest object")
	case err == io.EOF:
		if limited.N == 0 {
			return Manifest{}, manifestTooLargeError(0)
		}
		return manifest, nil
	default:
		if limited.N == 0 {
			return Manifest{}, manifestTooLargeError(0)
		}
		return Manifest{}, fmt.Errorf("decode app.json: %w", err)
	}
}

func manifestTooLargeError(size uint64) error {
	if size > 0 {
		return fmt.Errorf("app.json too large: %d bytes exceeds %d", size, maxManifestBytes)
	}
	return fmt.Errorf("app.json too large: exceeds %d bytes", maxManifestBytes)
}
