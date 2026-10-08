package apps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/dropfile/HankServerside/internal/protocol"
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var slashCommandPattern = regexp.MustCompile(`^/[A-Za-z][A-Za-z0-9_-]*$`)

const networkPermissionConfiguredBaseURL = "configured_base_url"
const filePermissionConfiguredSource = "configured_source"
const maxCommandTimeoutSeconds = 300

var allowedSettingsFieldTypes = map[string]struct{}{
	"boolean":  {},
	"number":   {},
	"password": {},
	"path":     {},
	"select":   {},
	"text":     {},
	"url":      {},
}

var allowedSettingsFieldSources = map[string]struct{}{
	"file_sources": {},
}

var allowedCommandModes = map[string]struct{}{
	"request_response": {},
}

var reservedSlashCommands = map[string]struct{}{
	"/append":   {},
	"/calendar": {},
	"/docs":     {},
	"/files":    {},
	"/ha":       {},
	"/notes":    {},
	"/status":   {},
}

type SettingsSchema = protocol.AppSettingsSchema
type SettingsField = protocol.AppSettingsField
type SettingsOption = protocol.AppSettingsOption

type Manifest struct {
	SchemaVersion string      `json:"schema_version"`
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Version       string      `json:"version"`
	Publisher     string      `json:"publisher,omitempty"`
	Description   string      `json:"description,omitempty"`
	Runtime       Runtime     `json:"runtime"`
	Assistant     Assistant   `json:"assistant,omitempty"`
	Commands      []Command   `json:"commands"`
	Config        Config      `json:"config,omitempty"`
	Permissions   Permissions `json:"permissions,omitempty"`
}

type Runtime struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type Assistant struct {
	SlashCommands []SlashCommand `json:"slash_commands,omitempty"`
}

type SlashCommand struct {
	Command     string `json:"command"`
	CommandID   string `json:"command_id"`
	Description string `json:"description,omitempty"`
}

type Command struct {
	ID             string `json:"id"`
	Mode           string `json:"mode"`
	InputSchema    string `json:"input_schema,omitempty"`
	OutputSchema   string `json:"output_schema,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	AdminOnly      bool   `json:"admin_only,omitempty"`
}

type Config struct {
	Schema       string         `json:"schema,omitempty"`
	SecretFields []string       `json:"secret_fields,omitempty"`
	Settings     SettingsSchema `json:"settings_schema,omitempty"`
}

type Permissions struct {
	Network []NetworkPermission `json:"network,omitempty"`
	Files   []FilePermission    `json:"files,omitempty"`
	Events  []json.RawMessage   `json:"events,omitempty"`
}

type NetworkPermission struct {
	Kind  string `json:"kind"`
	Field string `json:"field,omitempty"`
}

type FilePermission struct {
	Kind  string `json:"kind"`
	Field string `json:"field,omitempty"`
	Label string `json:"label,omitempty"`
}

func ValidateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != protocol.AppSchemaVersion {
		return fmt.Errorf("schema_version must be %q", protocol.AppSchemaVersion)
	}
	if !identifierPattern.MatchString(manifest.ID) {
		return fmt.Errorf("app id %q must match %s", manifest.ID, identifierPattern.String())
	}
	if len(manifest.ID) > 64 {
		return fmt.Errorf("app id exceeds 64 characters")
	}
	if err := validateTextField("name", manifest.Name, true, 80); err != nil {
		return err
	}
	if err := validateTextField("version", manifest.Version, true, 64); err != nil {
		return err
	}
	if err := validateTextField("publisher", manifest.Publisher, false, 80); err != nil {
		return err
	}
	if err := validateTextField("description", manifest.Description, false, 500); err != nil {
		return err
	}
	if manifest.Runtime.Type != protocol.AppRuntimeStdio {
		return fmt.Errorf("runtime type %q is not supported", manifest.Runtime.Type)
	}
	if err := validatePackagePath("runtime command", manifest.Runtime.Command, true); err != nil {
		return err
	}
	if len(manifest.Commands) == 0 {
		return fmt.Errorf("at least one command is required")
	}

	commandIDs := make(map[string]struct{}, len(manifest.Commands))
	for i, command := range manifest.Commands {
		if !identifierPattern.MatchString(command.ID) {
			return fmt.Errorf("command id %q must match %s", command.ID, identifierPattern.String())
		}
		if _, ok := commandIDs[command.ID]; ok {
			return fmt.Errorf("duplicate command id %q", command.ID)
		}
		commandIDs[command.ID] = struct{}{}
		if _, ok := allowedCommandModes[command.Mode]; !ok {
			return fmt.Errorf("command %d mode %q is not supported", i, command.Mode)
		}
		if command.TimeoutSeconds < 1 || command.TimeoutSeconds > maxCommandTimeoutSeconds {
			return fmt.Errorf("command %d timeout_seconds must be between 1 and %d", i, maxCommandTimeoutSeconds)
		}
		if err := validatePackagePath("schema path", command.InputSchema, false); err != nil {
			return fmt.Errorf("command %d input %w", i, err)
		}
		if err := validatePackagePath("schema path", command.OutputSchema, false); err != nil {
			return fmt.Errorf("command %d output %w", i, err)
		}
	}

	slashCommands := make(map[string]struct{}, len(manifest.Assistant.SlashCommands))
	for i, slashCommand := range manifest.Assistant.SlashCommands {
		if !slashCommandPattern.MatchString(slashCommand.Command) {
			return fmt.Errorf("slash command %q must match %s", slashCommand.Command, slashCommandPattern.String())
		}
		if err := validateTextField(fmt.Sprintf("slash command %d description", i), slashCommand.Description, false, 160); err != nil {
			return err
		}
		if _, ok := reservedSlashCommands[strings.ToLower(slashCommand.Command)]; ok {
			return fmt.Errorf("reserved slash command %q is owned by HankAI", slashCommand.Command)
		}
		if _, ok := slashCommands[slashCommand.Command]; ok {
			return fmt.Errorf("duplicate slash command %q", slashCommand.Command)
		}
		slashCommands[slashCommand.Command] = struct{}{}
		if _, ok := commandIDs[slashCommand.CommandID]; !ok {
			return fmt.Errorf("slash command %d references unknown command id %q", i, slashCommand.CommandID)
		}
	}

	if err := validatePackagePath("schema path", manifest.Config.Schema, false); err != nil {
		return fmt.Errorf("config %w", err)
	}
	secretFields := make(map[string]struct{}, len(manifest.Config.SecretFields))
	for i, field := range manifest.Config.SecretFields {
		if !identifierPattern.MatchString(field) {
			return fmt.Errorf("secret_fields[%d] %q must match %s", i, field, identifierPattern.String())
		}
		if _, ok := secretFields[field]; ok {
			return fmt.Errorf("duplicate secret_fields entry %q", field)
		}
		secretFields[field] = struct{}{}
	}
	settingsFields := make(map[string]SettingsField, len(manifest.Config.Settings.Fields))
	for i, field := range manifest.Config.Settings.Fields {
		if !identifierPattern.MatchString(field.Key) {
			return fmt.Errorf("settings field %d key %q must match %s", i, field.Key, identifierPattern.String())
		}
		if _, ok := settingsFields[field.Key]; ok {
			return fmt.Errorf("duplicate settings field key %q", field.Key)
		}
		settingsFields[field.Key] = field
		if _, ok := allowedSettingsFieldTypes[field.Type]; !ok {
			return fmt.Errorf("settings field %d type %q is not supported", i, field.Type)
		}
		if field.Secret && field.SecretKey != "" && !identifierPattern.MatchString(field.SecretKey) {
			return fmt.Errorf("settings field %d secret_key %q must match %s", i, field.SecretKey, identifierPattern.String())
		}
		if field.Secret {
			if field.SecretKey == "" {
				return fmt.Errorf("settings field %d secret_key is required for secret fields", i)
			}
			if _, ok := secretFields[field.SecretKey]; !ok {
				return fmt.Errorf("settings field %d secret_key %q must be listed in config.secret_fields", i, field.SecretKey)
			}
		}
		if field.Source != "" {
			if _, ok := allowedSettingsFieldSources[field.Source]; !ok {
				return fmt.Errorf("settings field %d source %q is not supported", i, field.Source)
			}
			if field.Type != "select" {
				return fmt.Errorf("settings field %d source requires select type", i)
			}
		}
		if err := validateSettingsDefault(i, field); err != nil {
			return err
		}
		if field.Type != "select" && len(field.Options) > 0 {
			return fmt.Errorf("settings field %d options require select type", i)
		}
		optionValues := make(map[string]struct{}, len(field.Options))
		for optionIndex, option := range field.Options {
			normalizedOption, err := normalizeSettingsScalarJSON(option.Value)
			if err != nil {
				return fmt.Errorf("settings field %d option %d value %w", i, optionIndex, err)
			}
			optionValues[normalizedOption] = struct{}{}
		}
		if field.Type == "select" && len(optionValues) > 0 && len(bytes.TrimSpace(field.Default)) > 0 {
			normalizedDefault, err := normalizeSettingsScalarJSON(field.Default)
			if err != nil {
				return fmt.Errorf("settings field %d default %w", i, err)
			}
			if _, ok := optionValues[normalizedDefault]; !ok {
				return fmt.Errorf("settings field %d default must match one of the static options", i)
			}
		}
	}
	for i, field := range manifest.Config.Settings.Fields {
		if field.SourceField == "" {
			continue
		}
		if field.Type != "path" {
			return fmt.Errorf("settings field %d source_field requires path type", i)
		}
		sourceField, ok := settingsFields[field.SourceField]
		if !ok {
			return fmt.Errorf("settings field %d source_field references unknown field %q", i, field.SourceField)
		}
		if sourceField.Type != "select" {
			return fmt.Errorf("settings field %d source_field %q must reference a select field", i, field.SourceField)
		}
		if sourceField.Source != "file_sources" {
			return fmt.Errorf("settings field %d source_field %q must reference a file_sources select field", i, field.SourceField)
		}
	}
	if len(manifest.Permissions.Network) > 16 || len(manifest.Permissions.Files) > 16 {
		return fmt.Errorf("at most 16 entries per permission kind are supported")
	}
	seenPermissions := map[string]bool{}
	for i, permission := range manifest.Permissions.Network {
		key := "network:" + permission.Field
		if seenPermissions[key] {
			return fmt.Errorf("duplicate network permission field")
		}
		seenPermissions[key] = true
		if permission.Kind != networkPermissionConfiguredBaseURL {
			return fmt.Errorf("network permission %d kind %q is not supported", i, permission.Kind)
		}
		if !identifierPattern.MatchString(permission.Field) {
			return fmt.Errorf("network permission %d field %q must match %s", i, permission.Field, identifierPattern.String())
		}
		field, ok := settingsFields[permission.Field]
		if !ok || field.Type != "url" {
			return fmt.Errorf("network permission %d field %q must reference a url settings field", i, permission.Field)
		}
	}
	for i, permission := range manifest.Permissions.Files {
		key := "files:" + permission.Field
		if seenPermissions[key] {
			return fmt.Errorf("duplicate file permission field")
		}
		seenPermissions[key] = true
		if permission.Kind != filePermissionConfiguredSource {
			return fmt.Errorf("file permission %d kind %q is not supported", i, permission.Kind)
		}
		if !identifierPattern.MatchString(permission.Field) {
			return fmt.Errorf("file permission %d field %q must match %s", i, permission.Field, identifierPattern.String())
		}
		field, ok := settingsFields[permission.Field]
		if !ok || field.Type != "select" || field.Source != "file_sources" {
			return fmt.Errorf("file permission %d field %q must reference a select settings field with source file_sources", i, permission.Field)
		}
	}
	if len(manifest.Permissions.Events) > 0 {
		return fmt.Errorf("event permission entries are not supported")
	}

	return nil
}

func validateTextField(label string, value string, required bool, maxLength int) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return fmt.Errorf("%s is required", label)
	}
	if value != "" && trimmed == "" {
		return fmt.Errorf("%s must not be blank", label)
	}
	if len(value) > maxLength {
		return fmt.Errorf("%s exceeds %d characters", label, maxLength)
	}
	return nil
}

func validateSettingsDefault(fieldIndex int, field SettingsField) error {
	if len(bytes.TrimSpace(field.Default)) == 0 {
		return nil
	}
	kind, err := settingsJSONKind(field.Default)
	if err != nil {
		return fmt.Errorf("settings field %d default %w", fieldIndex, err)
	}
	switch field.Type {
	case "boolean":
		if kind != "boolean" {
			return fmt.Errorf("settings field %d default must be a boolean", fieldIndex)
		}
	case "number":
		if kind != "number" {
			return fmt.Errorf("settings field %d default must be a number", fieldIndex)
		}
	case "password", "path", "text", "url":
		if kind != "string" {
			return fmt.Errorf("settings field %d default must be a string", fieldIndex)
		}
	case "select":
		if kind != "boolean" && kind != "number" && kind != "string" {
			return fmt.Errorf("settings field %d default must be a string, number, or boolean", fieldIndex)
		}
	}
	return nil
}

func normalizeSettingsScalarJSON(raw json.RawMessage) (string, error) {
	kind, err := settingsJSONKind(raw)
	if err != nil {
		return "", err
	}
	if kind != "boolean" && kind != "number" && kind != "string" {
		return "", fmt.Errorf("must be a string, number, or boolean")
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, bytes.TrimSpace(raw)); err != nil {
		return "", fmt.Errorf("must be valid JSON")
	}
	return compacted.String(), nil
}

func settingsJSONKind(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("is required")
	}
	if !json.Valid(trimmed) {
		return "", fmt.Errorf("must be valid JSON")
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("must be valid JSON")
	}
	switch value.(type) {
	case nil:
		return "null", nil
	case bool:
		return "boolean", nil
	case json.Number:
		return "number", nil
	case string:
		return "string", nil
	case []interface{}:
		return "array", nil
	case map[string]interface{}:
		return "object", nil
	default:
		return "unknown", nil
	}
}

func validatePackagePath(label string, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
	if _, ok := cleanPackagePath(value); !ok {
		return fmt.Errorf("%s %q must be a relative clean path inside the app package", label, value)
	}
	return nil
}

func cleanPackagePath(value string) (string, bool) {
	if value == "" || strings.Contains(value, "\x00") || strings.Contains(value, "\\") {
		return "", false
	}
	if path.IsAbs(value) || hasWindowsVolume(value) {
		return "", false
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned != value {
		return "", false
	}
	return cleaned, true
}

func hasWindowsVolume(value string) bool {
	return len(value) >= 2 && value[1] == ':' &&
		((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z'))
}

func referencedSchemaPaths(manifest Manifest) []string {
	paths := make([]string, 0, 1+len(manifest.Commands)*2)
	if manifest.Config.Schema != "" {
		paths = append(paths, manifest.Config.Schema)
	}
	for _, command := range manifest.Commands {
		if command.InputSchema != "" {
			paths = append(paths, command.InputSchema)
		}
		if command.OutputSchema != "" {
			paths = append(paths, command.OutputSchema)
		}
	}
	return paths
}
