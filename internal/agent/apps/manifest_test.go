package apps

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validContractManifest() Manifest {
	return Manifest{
		SchemaVersion: "hank.app.v1",
		ID:            "sample_app",
		Name:          "Sample App",
		Version:       "1.0.0",
		Publisher:     "Hank",
		Description:   "Exercise the installable app manifest contract.",
		Runtime: Runtime{
			Type:    "stdio",
			Command: "bin/sample-app",
		},
		Assistant: Assistant{
			SlashCommands: []SlashCommand{{
				Command:     "/sample",
				CommandID:   "run",
				Description: "Run the sample command.",
			}},
		},
		Commands: []Command{{
			ID:             "run",
			Mode:           "request_response",
			InputSchema:    "schemas/run.input.schema.json",
			OutputSchema:   "schemas/run.output.schema.json",
			TimeoutSeconds: 120,
			AdminOnly:      true,
		}},
		Config: Config{
			Schema:       "schemas/config.schema.json",
			SecretFields: []string{"api_key"},
			Settings: SettingsSchema{Fields: []SettingsField{
				{Key: "api_base_url", Label: "API URL", Type: "url", Required: true},
				{Key: "api_key", Label: "API key", Type: "password", Secret: true, SecretKey: "api_key"},
			}},
		},
		Permissions: Permissions{
			Network: []NetworkPermission{{
				Kind:  "configured_base_url",
				Field: "api_base_url",
			}},
		},
	}
}

func TestValidateManifestAcceptsContractShape(t *testing.T) {
	t.Parallel()
	if err := ValidateManifest(validContractManifest()); err != nil {
		t.Fatalf("ValidateManifest error: %v", err)
	}
}

func TestValidateManifestAcceptsNeutralContractFixtures(t *testing.T) {
	t.Parallel()
	for name, manifest := range neutralContractManifestFixtures() {
		manifest := manifest
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateManifest(manifest); err != nil {
				t.Fatalf("ValidateManifest error: %v", err)
			}
		})
	}
}

func neutralContractManifestFixtures() map[string]Manifest {
	withConfiguredPermissions := validContractManifest()
	withConfiguredPermissions.Config.Settings = SettingsSchema{Fields: []SettingsField{
		{Key: "enabled", Label: "Enabled", Type: "boolean", Default: json.RawMessage(`true`), Order: 10},
		{Key: "api_base_url", Label: "API URL", Type: "url", Required: true, Default: json.RawMessage(`"https://example.test"`), Order: 20},
		{Key: "api_key", Label: "API key", Type: "password", Secret: true, SecretKey: "api_key", Order: 30},
		{Key: "source_id", Label: "File source", Type: "select", Source: "file_sources", Order: 40},
		{Key: "destination_path", Label: "Destination", Type: "path", SourceField: "source_id", Placeholder: "Inbox", Order: 50},
		{Key: "mode", Label: "Mode", Type: "select", Default: json.RawMessage(`"standard"`), Options: []SettingsOption{
			{Value: json.RawMessage(`"standard"`), Label: "Standard"},
			{Value: json.RawMessage(`"advanced"`), Label: "Advanced"},
		}, Order: 60},
		{Key: "timeout_seconds", Label: "Timeout", Type: "number", Default: json.RawMessage(`120`), Order: 70},
	}}
	withConfiguredPermissions.Permissions = Permissions{
		Network: []NetworkPermission{{Kind: "configured_base_url", Field: "api_base_url"}},
		Files:   []FilePermission{{Kind: "configured_source", Field: "source_id"}},
	}

	multiCommand := validContractManifest()
	multiCommand.ID = "workflow_app"
	multiCommand.Name = "Workflow App"
	multiCommand.Assistant.SlashCommands = []SlashCommand{{
		Command:     "/workflow",
		CommandID:   "start",
		Description: "Start a workflow.",
	}}
	multiCommand.Commands = []Command{
		{ID: "start", Mode: "request_response", InputSchema: "schemas/start.input.schema.json", OutputSchema: "schemas/start.output.schema.json", TimeoutSeconds: 30},
		{ID: "status", Mode: "request_response", InputSchema: "schemas/status.input.schema.json", OutputSchema: "schemas/status.output.schema.json", TimeoutSeconds: 30},
		{ID: "cancel", Mode: "request_response", InputSchema: "schemas/cancel.input.schema.json", OutputSchema: "schemas/cancel.output.schema.json", TimeoutSeconds: 30, AdminOnly: true},
	}

	return map[string]Manifest{
		"configured_permissions": withConfiguredPermissions,
		"multi_command":          multiCommand,
	}
}

func TestValidateManifestAcceptsTypedSettingsAndFileSourcePermission(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	manifest.Config.Settings = SettingsSchema{
		Fields: []SettingsField{
			{
				Key:      "api_base_url",
				Label:    "API URL",
				Type:     "url",
				Required: true,
			},
			{
				Key:       "api_key",
				Label:     "API key",
				Type:      "password",
				Secret:    true,
				SecretKey: "api_key",
			},
			{
				Key:    "source_id",
				Label:  "Media source",
				Type:   "select",
				Source: "file_sources",
			},
			{
				Key:         "destination_path",
				Label:       "Destination folder",
				Type:        "path",
				SourceField: "source_id",
			},
		},
	}
	manifest.Permissions.Files = []FilePermission{{
		Kind:  "configured_source",
		Field: "source_id",
	}}

	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("ValidateManifest error: %v", err)
	}
}

func TestValidateManifestRejectsSecretAndPermissionFieldDrift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{
			name: "secret field missing from config secret_fields",
			mutate: func(m *Manifest) {
				m.Config.SecretFields = nil
				m.Config.Settings = SettingsSchema{Fields: []SettingsField{{
					Key:       "api_key",
					Type:      "password",
					Secret:    true,
					SecretKey: "api_key",
				}}}
			},
			want: "secret_fields",
		},
		{
			name: "file permission references missing settings field",
			mutate: func(m *Manifest) {
				m.Config.Settings = SettingsSchema{Fields: []SettingsField{{Key: "source_id", Type: "select", Source: "file_sources"}}}
				m.Permissions.Network = nil
				m.Permissions.Files = []FilePermission{{Kind: "configured_source", Field: "missing_source"}}
			},
			want: "file permission",
		},
		{
			name: "file permission references non file source field",
			mutate: func(m *Manifest) {
				m.Config.Settings = SettingsSchema{Fields: []SettingsField{{Key: "source_id", Type: "text"}}}
				m.Permissions.Network = nil
				m.Permissions.Files = []FilePermission{{Kind: "configured_source", Field: "source_id"}}
			},
			want: "file_sources",
		},
		{
			name: "network permission references non url field",
			mutate: func(m *Manifest) {
				m.Config.Settings = SettingsSchema{Fields: []SettingsField{{Key: "api_base_url", Type: "text"}}}
				m.Permissions.Network = []NetworkPermission{{Kind: "configured_base_url", Field: "api_base_url"}}
			},
			want: "network permission",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validContractManifest()
			tt.mutate(&manifest)
			err := ValidateManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateManifest error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateManifestRejectsInvalidPathSourceFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		fields []SettingsField
		want   string
	}{
		{
			name: "source field on non path",
			fields: []SettingsField{
				{Key: "source_id", Type: "select", Source: "file_sources"},
				{Key: "destination_path", Type: "text", SourceField: "source_id"},
			},
			want: "path type",
		},
		{
			name: "missing source field",
			fields: []SettingsField{
				{Key: "destination_path", Type: "path", SourceField: "missing_source"},
			},
			want: "missing_source",
		},
		{
			name: "source field references non select",
			fields: []SettingsField{
				{Key: "source_id", Type: "text"},
				{Key: "destination_path", Type: "path", SourceField: "source_id"},
			},
			want: "select",
		},
		{
			name: "source field references static select",
			fields: []SettingsField{
				{Key: "source_id", Type: "select", Options: []SettingsOption{{Value: json.RawMessage(`"local"`)}}},
				{Key: "destination_path", Type: "path", SourceField: "source_id"},
			},
			want: "file_sources",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validContractManifest()
			manifest.Config.Settings = SettingsSchema{Fields: tt.fields}
			manifest.Permissions.Network = nil
			err := ValidateManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateManifest error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateManifestRejectsInvalidSettingsFields(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	manifest.Config.Settings = SettingsSchema{
		Fields: []SettingsField{{
			Key:  "../bad",
			Type: "text",
		}},
	}

	err := ValidateManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "settings field") {
		t.Fatalf("ValidateManifest error = %v, want settings field", err)
	}
}

func TestValidateManifestRejectsUnsafeIDsAndPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"bad app id", func(m *Manifest) { m.ID = "../bad" }, "app id"},
		{"bad command path", func(m *Manifest) { m.Runtime.Command = "../bad" }, "runtime command"},
		{"bad schema path", func(m *Manifest) { m.Commands[0].InputSchema = "/tmp/schema.json" }, "schema path"},
		{"unknown permission", func(m *Manifest) { m.Permissions.Network[0].Kind = "internet" }, "permission"},
		{"missing command", func(m *Manifest) { m.Commands = nil }, "command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validContractManifest()
			tt.mutate(&manifest)
			err := ValidateManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateManifest error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateManifestRejectsWeakMetadataAndUnsupportedCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"missing name", func(m *Manifest) { m.Name = "" }, "name is required"},
		{"long name", func(m *Manifest) { m.Name = strings.Repeat("x", 81) }, "name exceeds"},
		{"missing version", func(m *Manifest) { m.Version = "" }, "version is required"},
		{"long version", func(m *Manifest) { m.Version = strings.Repeat("1", 65) }, "version exceeds"},
		{"long publisher", func(m *Manifest) { m.Publisher = strings.Repeat("x", 81) }, "publisher exceeds"},
		{"long description", func(m *Manifest) { m.Description = strings.Repeat("x", 501) }, "description exceeds"},
		{"unsupported command mode", func(m *Manifest) { m.Commands[0].Mode = "stream" }, "mode"},
		{"zero timeout", func(m *Manifest) { m.Commands[0].TimeoutSeconds = 0 }, "timeout_seconds"},
		{"huge timeout", func(m *Manifest) { m.Commands[0].TimeoutSeconds = 301 }, "timeout_seconds"},
		{"long slash description", func(m *Manifest) { m.Assistant.SlashCommands[0].Description = strings.Repeat("x", 161) }, "slash command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validContractManifest()
			tt.mutate(&manifest)
			err := ValidateManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateManifest error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateManifestRejectsDuplicateSlashCommands(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	manifest.Assistant.SlashCommands = append(manifest.Assistant.SlashCommands, SlashCommand{
		Command:     "/sample",
		CommandID:   "run",
		Description: "Duplicate route.",
	})
	err := ValidateManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "duplicate slash command") {
		t.Fatalf("ValidateManifest error = %v, want duplicate slash command", err)
	}
}

func TestValidateManifestRejectsReservedSlashCommands(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	manifest.Assistant.SlashCommands[0].Command = "/files"
	err := ValidateManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "reserved slash command") {
		t.Fatalf("ValidateManifest error = %v, want reserved slash command", err)
	}
}

func TestValidateManifestRejectsInvalidSettingsDefaultsAndOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		field SettingsField
		want  string
	}{
		{
			name: "boolean default string",
			field: SettingsField{
				Key:     "enabled",
				Type:    "boolean",
				Default: json.RawMessage(`"true"`),
			},
			want: "default",
		},
		{
			name: "number default string",
			field: SettingsField{
				Key:     "timeout_seconds",
				Type:    "number",
				Default: json.RawMessage(`"900"`),
			},
			want: "default",
		},
		{
			name: "text default boolean",
			field: SettingsField{
				Key:     "model",
				Type:    "text",
				Default: json.RawMessage(`false`),
			},
			want: "default",
		},
		{
			name: "select option object",
			field: SettingsField{
				Key:  "format",
				Type: "select",
				Options: []SettingsOption{{
					Value: json.RawMessage(`{"bad":true}`),
					Label: "Bad",
				}},
			},
			want: "option",
		},
		{
			name: "select default outside options",
			field: SettingsField{
				Key:     "format",
				Type:    "select",
				Default: json.RawMessage(`"missing"`),
				Options: []SettingsOption{{
					Value: json.RawMessage(`"best"`),
					Label: "Best",
				}},
			},
			want: "default must match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validContractManifest()
			manifest.Config.Settings = SettingsSchema{Fields: []SettingsField{tt.field}}
			err := ValidateManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateManifest error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateManifestAcceptsScalarSelectDefaultsAndEmptyOptions(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	manifest.Config.Settings = SettingsSchema{
		Fields: []SettingsField{
			{
				Key:     "rate_limit",
				Type:    "select",
				Default: json.RawMessage(`""`),
				Options: []SettingsOption{
					{Value: json.RawMessage(`""`), Label: "No limit"},
					{Value: json.RawMessage(`"10M"`), Label: "10 MB/s"},
				},
			},
			{
				Key:     "timeout_seconds",
				Type:    "select",
				Default: json.RawMessage(`900`),
				Options: []SettingsOption{
					{Value: json.RawMessage(`300`), Label: "5 minutes"},
					{Value: json.RawMessage(`900`), Label: "15 minutes"},
				},
			},
		},
	}
	manifest.Permissions = Permissions{}

	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("ValidateManifest error: %v", err)
	}
}

func TestPreviewArchiveRejectsTraversal(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "bad.hankapp")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	writer, err := zw.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = PreviewArchive(archivePath)
	if err == nil || !strings.Contains(err.Error(), "unsafe archive path") {
		t.Fatalf("PreviewArchive error = %v", err)
	}
}

func TestPreviewArchiveRejectsTrailingManifestJSON(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "trailing-json.hankapp")
	writeArchiveEntries(t, archivePath, samplePackageEntries(string(rawManifest)+"\n{}"))

	_, err = PreviewArchive(archivePath)
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("PreviewArchive error = %v, want trailing JSON", err)
	}
}

func TestPreviewArchiveRejectsOversizedManifest(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "oversized-manifest.hankapp")
	writeArchiveEntries(t, archivePath, samplePackageEntries(string(rawManifest)+strings.Repeat(" ", 128*1024)))

	_, err = PreviewArchive(archivePath)
	if err == nil || !strings.Contains(err.Error(), "app.json too large") {
		t.Fatalf("PreviewArchive error = %v, want app.json too large", err)
	}
}

func TestPreviewArchiveAcceptsSamplePackage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifest := validContractManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "sample.hankapp")
	writeArchiveEntries(t, archivePath, samplePackageEntries(string(rawManifest)))

	preview, err := PreviewArchive(archivePath)
	if err != nil {
		t.Fatalf("PreviewArchive error: %v", err)
	}
	if preview.Manifest.ID != "sample_app" || preview.Manifest.Commands[0].ID != "run" {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestPreviewArchiveRejectsUnsafeArchivePaths(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	validEntries := func() []archiveEntry {
		return archiveEntriesFromMap(samplePackageEntries(string(rawManifest)))
	}
	tests := []struct {
		name    string
		entries []archiveEntry
		want    string
	}{
		{
			name:    "absolute POSIX path",
			entries: []archiveEntry{{Name: "/tmp/escape", Body: "bad"}},
			want:    "unsafe archive path",
		},
		{
			name:    "Windows volume path",
			entries: []archiveEntry{{Name: "C:/tmp/escape", Body: "bad"}},
			want:    "unsafe archive path",
		},
		{
			name:    "backslash path",
			entries: []archiveEntry{{Name: `dir\file`, Body: "bad"}},
			want:    "unsafe archive path",
		},
		{
			name:    "dot relative path",
			entries: []archiveEntry{{Name: "./app.json", Body: "bad"}},
			want:    "unsafe archive path",
		},
		{
			name:    "double slash path",
			entries: []archiveEntry{{Name: "dir//file", Body: "bad"}},
			want:    "unsafe archive path",
		},
		{
			name:    "directory and file same path",
			entries: []archiveEntry{{Name: "dir/", Body: ""}, {Name: "dir", Body: "bad"}},
			want:    "duplicate archive path",
		},
		{
			name:    "file contains child path",
			entries: append(validEntries(), archiveEntry{Name: "bin", Body: "not a directory"}),
			want:    "archive path collision",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), strings.ReplaceAll(tt.name, " ", "-")+".hankapp")
			writeArchiveEntryList(t, archivePath, tt.entries)
			_, err := PreviewArchive(archivePath)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PreviewArchive error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPreviewArchiveRejectsDuplicateAndSymlinkEntries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write func(*testing.T, *zip.Writer)
		want  string
	}{
		{
			name: "duplicate path",
			write: func(t *testing.T, zw *zip.Writer) {
				t.Helper()
				for i := 0; i < 2; i++ {
					writer, err := zw.Create("app.json")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := writer.Write([]byte("{}")); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "duplicate archive path",
		},
		{
			name: "symlink",
			write: func(t *testing.T, zw *zip.Writer) {
				t.Helper()
				header := &zip.FileHeader{Name: "link"}
				header.SetMode(os.ModeSymlink | 0o777)
				writer, err := zw.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write([]byte("app.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "symlink",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "bad.hankapp")
			file, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(file)
			tt.write(t, zw)
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = PreviewArchive(archivePath)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PreviewArchive error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPreviewArchiveRequiresReferencedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifest := validContractManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		entries map[string]string
		want    string
	}{
		{
			name: "missing runtime command",
			entries: map[string]string{
				"app.json": string(rawManifest),
			},
			want: "runtime command",
		},
		{
			name: "missing schema",
			entries: map[string]string{
				"app.json":       string(rawManifest),
				"bin/sample-app": "#!/bin/sh\n",
			},
			want: "schema path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".hankapp")
			writeArchiveEntries(t, archivePath, tt.entries)
			_, err := PreviewArchive(archivePath)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PreviewArchive error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPreviewArchiveRejectsInvalidReferencedSchemaJSON(t *testing.T) {
	t.Parallel()
	manifest := validContractManifest()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entries := samplePackageEntries(string(rawManifest))
	entries["schemas/run.input.schema.json"] = `{"type":"object"`
	archivePath := filepath.Join(t.TempDir(), "bad-schema.hankapp")
	writeArchiveEntries(t, archivePath, entries)

	_, err = PreviewArchive(archivePath)
	if err == nil || !strings.Contains(err.Error(), "schema path") {
		t.Fatalf("PreviewArchive error = %v, want schema path validation error", err)
	}
}

type archiveEntry struct {
	Name string
	Body string
}

func samplePackageEntries(rawManifest string) map[string]string {
	return map[string]string{
		"app.json":                       rawManifest,
		"bin/sample-app":                 "#!/bin/sh\n",
		"schemas/config.schema.json":     `{"type":"object"}`,
		"schemas/run.input.schema.json":  `{"type":"object"}`,
		"schemas/run.output.schema.json": `{"type":"object"}`,
	}
}

func archiveEntriesFromMap(entries map[string]string) []archiveEntry {
	list := make([]archiveEntry, 0, len(entries))
	for name, body := range entries {
		list = append(list, archiveEntry{Name: name, Body: body})
	}
	return list
}

func writeArchiveEntries(t *testing.T, archivePath string, entries map[string]string) {
	t.Helper()
	writeArchiveEntryList(t, archivePath, archiveEntriesFromMap(entries))
}

func writeArchiveEntryList(t *testing.T, archivePath string, entries []archiveEntry) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for _, entry := range entries {
		writer, err := zw.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.Body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictedExamplePackage(t *testing.T) {
	root := filepath.Join("..", "..", "..", "examples", "restricted-app")
	entries := map[string]string{}
	for _, name := range []string{"app.json", "bin/example", "bin/hank_broker.py"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		entries[name] = string(data)
	}
	archive := filepath.Join(t.TempDir(), "example.hankapp")
	writeArchiveEntries(t, archive, entries)
	preview, err := PreviewArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Manifest.ID != "restricted_example" {
		t.Fatal("wrong example manifest")
	}
}
