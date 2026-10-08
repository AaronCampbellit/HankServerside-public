package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/google/jsonschema-go/jsonschema"
)

// executionToolContext is server-derived. No schema accepts a user or Home ID.
type executionToolContext struct {
	SessionID string
	Home      domain.Home
	Member    domain.HomeMembership
	UserID    string
	Settings  domain.AssistantSettings
}

type executionItem struct {
	JournalEpoch   string                          `json:"journal_epoch,omitempty"`
	Service        *protocol.AssistantServiceState `json:"service,omitempty"`
	StageID        string                          `json:"stage_id,omitempty"`
	ChecksumSHA256 string                          `json:"checksum_sha256,omitempty"`
	SizeBytes      int64                           `json:"size_bytes,omitempty"`
	Type           string                          `json:"type"`
	ID             string                          `json:"id"`
	Scope          string                          `json:"scope"`
	Title          string                          `json:"title"`
	Text           string                          `json:"text,omitempty"`
	Revision       string                          `json:"revision,omitempty"`
	AgentID        string                          `json:"agent_id,omitempty"`
	SourceID       string                          `json:"source_id,omitempty"`
	Path           string                          `json:"path,omitempty"`
	URI            string                          `json:"uri,omitempty"`
	State          string                          `json:"state,omitempty"`
	DeviceID       string                          `json:"device_id,omitempty"`
	CalendarID     string                          `json:"calendar_id,omitempty"`
	EventID        string                          `json:"event_id,omitempty"`
	StartsAt       string                          `json:"starts_at,omitempty"`
	EndsAt         string                          `json:"ends_at,omitempty"`
	UpdatedAt      string                          `json:"updated_at,omitempty"`
}

type executionData struct {
	Items    []executionItem `json:"items"`
	HasMore  bool            `json:"has_more"`
	Prepared json.RawMessage `json:"prepared,omitempty"`
}

func executionString(max int) map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
}
func executionEnum(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func executionSchema(properties map[string]any, required ...string) *jsonschema.Schema {
	raw, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required})
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		panic("invalid static assistant schema")
	}
	return &schema
}

func executionOutputSchema(prepared *jsonschema.Schema) *jsonschema.Schema {
	properties := map[string]any{"type": executionEnum("note", "file", "folder", "calendar_event", "calendar", "homeassistant_entity", "machine", "project_doc", "conversation", "status", "app", "media_job"), "id": executionString(4096), "scope": executionString(256), "title": map[string]any{"type": "string", "maxLength": 1024}}
	for _, key := range []string{"journal_epoch", "stage_id", "checksum_sha256", "text", "revision", "agent_id", "source_id", "path", "uri", "state", "device_id", "calendar_id", "event_id", "starts_at", "ends_at", "updated_at"} {
		properties[key] = map[string]any{"type": "string", "maxLength": 32768}
	}
	properties["service"] = executionSchema(map[string]any{"unit": executionString(256), "active_state": map[string]any{"type": "string", "maxLength": 256}, "sub_state": map[string]any{"type": "string", "maxLength": 256}, "invocation_id": map[string]any{"type": "string", "maxLength": 256}, "allowed_operations": map[string]any{"type": "array", "maxItems": 3, "items": executionEnum("start", "stop", "restart")}}, "unit", "active_state", "sub_state", "invocation_id", "allowed_operations")
	properties["size_bytes"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 104857600}
	fields := map[string]any{"items": map[string]any{"type": "array", "maxItems": 50, "items": executionSchema(properties, "type", "id", "scope", "title")}, "has_more": map[string]any{"type": "boolean"}}
	if prepared != nil {
		fields["prepared"] = prepared
	}
	return executionSchema(fields, "items", "has_more")
}

// Constructed for one authenticated principal. Each handler reloads membership
// and settings, so registry lifetime does not turn old permissions into a grant.
func (s *Server) newAssistantExecutionTools(homeID, userID string, sessions ...string) (*assistant.Registry, error) {
	registry := assistant.NewRegistry()
	query := map[string]any{"type": "string", "maxLength": 512}
	limit := map[string]any{"type": "integer", "minimum": 1, "maximum": 50}
	type spec struct {
		name, description, policy string
		write                     bool
		input                     *jsonschema.Schema
	}
	specs := []spec{
		{"attachments.list", "List immutable attachments staged by the user in this conversation, with exact IDs and checksums.", "home_member", false, executionSchema(map[string]any{})},
		{"files.upload", "Prepare ONE attachment returned by attachments.list for upload. path is the complete source-relative destination including the filename, not just the folder. Discover the destination with files.sources and files.search first. Never overwrite existing files.", "files_write", true, executionSchema(map[string]any{"agent_id": executionString(256), "source_id": executionString(256), "path": executionString(4096), "stage_id": executionString(256)}, "agent_id", "source_id", "path", "stage_id")},
		{"files.sources", "List configured primary-agent source IDs and permitted starting folders; availability is not verified.", "files_read", false, executionSchema(map[string]any{})},
		{"apps.invoke", "Prepare an explicitly selected installed app command for approval; never retries opaque writes.", "app_command", true, executionSchema(map[string]any{"app_id": executionString(256), "command_id": executionString(256), "version": executionString(128), "query": query}, "app_id", "command_id", "version", "query")},
		{"notes.search", "Find visible Notes and return stable note IDs and revisions.", "notes_read", false, executionSchema(map[string]any{"query": query, "limit": limit}, "query", "limit")},
		{"notes.get", "Read an exact visible note. Use the id field returned by notes.search as note_id, not the ID embedded in its source URI.", "notes_read", false, executionSchema(map[string]any{"note_id": executionString(256)}, "note_id")},
		{"notes.create", "Prepare a personal note for approval; does not create it. title and text are stored literally. Preserve the requested title and body exactly, without added quotes, headings, formatting or commentary.", "notes_write", true, executionSchema(map[string]any{"title": executionString(256), "text": executionString(16000)}, "title", "text")},
		{"notes.append", "Prepare an append to an exact note revision; does not write. Use the id and revision fields from notes.search or notes.get, not an ID parsed from the source URI. text contains only the exact new text, never the existing body. The server inserts one newline before text when the note is nonempty; do not add that separator yourself. Preserve requested content without added quotes or commentary.", "notes_write", true, executionSchema(map[string]any{"note_id": executionString(256), "revision": executionString(256), "text": executionString(16000)}, "note_id", "revision", "text")},
		{"files.search", "Search filenames and folders on one discovered source. Copy agent_id, source_id and starting path from files.sources; use / for the root, never an empty path. Query contains only the name or search terms, never /files. Search other discovered sources if needed.", "files_read", false, executionSchema(map[string]any{"agent_id": executionString(256), "source_id": executionString(256), "path": executionString(4096), "query": query, "limit": limit}, "agent_id", "source_id", "path", "query", "limit")},
		{"files.list", "List a contained folder on an exact source and agent.", "files_read", false, executionSchema(map[string]any{"agent_id": executionString(256), "source_id": executionString(256), "path": executionString(4096), "limit": limit}, "agent_id", "source_id", "path", "limit")},
		{"files.read", "Read bounded permitted text; binary and sensitive file types are excluded.", "files_read", false, executionSchema(map[string]any{"agent_id": executionString(256), "source_id": executionString(256), "path": executionString(4096)}, "agent_id", "source_id", "path")},
		{"files.stat", "Inspect a contained file or folder.", "files_read", false, executionSchema(map[string]any{"agent_id": executionString(256), "source_id": executionString(256), "path": executionString(4096)}, "agent_id", "source_id", "path")},
		{"files.create_folder", "Prepare folder creation only when the user explicitly requests a NEW folder. A failed search is never permission to create a folder. Does not create it.", "files_write", true, executionSchema(map[string]any{"agent_id": executionString(256), "source_id": executionString(256), "path": executionString(4096)}, "agent_id", "source_id", "path")},
		{"homeassistant.search", "Find current Home Assistant entities through the primary agent.", "homeassistant_read", false, executionSchema(map[string]any{"query": query, "limit": limit}, "query", "limit")},
		{"homeassistant.get", "Read one exact entity's current state.", "homeassistant_read", false, executionSchema(map[string]any{"entity_id": executionString(256)}, "entity_id")},
		{"homeassistant.call_service", "Prepare an allowlisted exact entity service for approval.", "homeassistant_write", true, executionSchema(map[string]any{"entity_id": executionString(256), "service": executionEnum("turn_on", "turn_off", "open_cover", "close_cover", "lock", "unlock", "press")}, "entity_id", "service")},
		{"calendar.search", "Search the user's device calendar snapshots; results include freshness.", "calendar_read", false, executionSchema(map[string]any{"query": query, "limit": limit}, "query", "limit")},
		{"calendar.get", "Read an exact calendar snapshot by Hank entry ID.", "calendar_read", false, executionSchema(map[string]any{"entry_id": executionString(256)}, "entry_id")},
		{"calendar.create_event", "Prepare an event in an exact calendar known from the user's snapshots; requires a capable client and approval.", "calendar_write", true, executionSchema(map[string]any{"device_id": executionString(256), "calendar_id": executionString(256), "title": executionString(256), "starts_at": executionString(64), "ends_at": executionString(64), "timezone": executionString(128)}, "device_id", "calendar_id", "title", "starts_at", "ends_at", "timezone")},
		{"calendar.update_event", "Prepare an exact calendar change; requires a capable client and approval.", "calendar_write", true, executionSchema(map[string]any{"entry_id": executionString(256), "revision": executionString(256), "starts_at": executionString(64), "ends_at": executionString(64), "timezone": executionString(128)}, "entry_id", "revision", "starts_at", "ends_at", "timezone")},
		{"calendar.delete_event", "Prepare cancellation of an exact calendar event; requires approval.", "calendar_write", true, executionSchema(map[string]any{"entry_id": executionString(256), "revision": executionString(256)}, "entry_id", "revision")},
		{"machines.services", "Inspect an allowlisted system service on an exact agent; use an empty unit to list configured units and operations first.", "machine_admin", false, executionSchema(map[string]any{"agent_id": executionString(256), "unit": map[string]any{"type": "string", "maxLength": 256}}, "agent_id", "unit")},
		{"machines.service_action", "Prepare an explicitly allowlisted start, stop or restart of an exact service revision for approval.", "machine_admin", true, executionSchema(map[string]any{"agent_id": executionString(256), "unit": executionString(256), "operation": executionEnum("start", "stop", "restart"), "revision": executionString(256)}, "agent_id", "unit", "operation", "revision")},
		{"machines.list", "List enrolled machines in this Home.", "home_member", false, executionSchema(map[string]any{})},
		{"machines.status", "Read exact enrolled machine connection metadata.", "home_member", false, executionSchema(map[string]any{"agent_id": executionString(256)}, "agent_id")},
		{"evidence.search", "Search an enabled permitted source for evidence references.", "evidence", false, executionSchema(map[string]any{"source": executionEnum("notes", "calendar", "project_docs", "conversation"), "query": query, "limit": limit}, "source", "query", "limit")},
		{"evidence.read", "Read an exact authorized document or conversation reference.", "evidence", false, executionSchema(map[string]any{"source": executionEnum("project_docs", "conversation"), "id": executionString(512)}, "source", "id")},
	}
	for _, item := range specs {
		item := item
		def := assistant.Definition{Name: item.name, Version: 1, Description: item.description, InputSchema: item.input, OutputSchema: executionOutputSchema(nil), Effect: "read", Approval: "never", PermissionPolicy: item.policy, Retry: "safe_read", Idempotency: "read_only", Verification: "not_applicable", RequiredCapabilities: []string{}, TimeoutMS: 30000, MaxResultBytes: assistant.MaxResultBytes}
		if item.write {
			def.Effect = "write"
			def.Approval = "required"
			def.Retry = "reconcile"
			def.Idempotency = "reconcile_only"
			def.Verification = "readback"
			def.OutputSchema = executionOutputSchema(item.input)
		}
		switch {
		case item.name == "files.sources":
		case strings.HasPrefix(item.name, "files."):
			capability := item.name
			if item.name == "files.read" {
				capability = protocol.CommandMCPContextRead
			}
			if item.name == "files.upload" {
				capability = protocol.CommandAssistantOperationExecute
			}
			if item.name == "files.create_folder" {
				capability = "files.create_directory"
			}
			def.RequiredCapabilities = []string{capability}
		case strings.HasPrefix(item.name, "homeassistant."):
			def.RequiredCapabilities = []string{"homeassistant.fetch_states"}
			if item.write {
				def.RequiredCapabilities = append(def.RequiredCapabilities, "homeassistant.call_service")
			}
		case strings.HasPrefix(item.name, "machines.service"):
			def.RequiredCapabilities = []string{protocol.CommandAssistantServiceInspect}
			if item.write {
				def.RequiredCapabilities = append(def.RequiredCapabilities, protocol.CommandAssistantOperationExecute)
			}
		case item.name == "apps.invoke":
			def.RequiredCapabilities = []string{protocol.CommandAppsInvoke}
			def.Retry, def.Verification = "never", "unavailable"
		}
		err := registry.Register(def, func(ctx context.Context, call assistant.Call) assistant.Result {
			runtime, err := s.executionToolContext(ctx, homeID, userID, item.policy)
			if err != nil {
				return assistant.Failure(call.ID, "permission_denied")
			}
			if len(sessions) == 1 {
				runtime.SessionID = sessions[0]
			}
			var args map[string]any
			if json.Unmarshal(call.Arguments, &args) != nil {
				return assistant.Failure(call.ID, "invalid_arguments")
			}
			return s.executeAssistantCapability(ctx, runtime, call, args, item.write)
		})
		if err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (s *Server) executionToolContext(ctx context.Context, homeID, userID, policy string) (executionToolContext, error) {
	home, member, err := s.requireSingletonHomeMembership(ctx, userID)
	if err != nil || home.ID != homeID {
		return executionToolContext{}, errors.New("permission_denied")
	}
	settings, err := s.currentAssistantSettings(ctx, homeID, userID)
	if err != nil {
		return executionToolContext{}, err
	}
	allowed := true
	feature := ""
	switch {
	case strings.HasPrefix(policy, "notes_"):
		allowed = settings.ProfileNotesEnabled || settings.HomeNotesEnabled
		feature = domain.HomePermissionFeatureNotes
	case strings.HasPrefix(policy, "files_"):
		allowed = settings.FilesEnabled
		feature = domain.HomePermissionFeatureFiles
	case strings.HasPrefix(policy, "homeassistant_"):
		allowed = settings.HomeAssistantEnabled
		feature = domain.HomePermissionFeatureHomeAssistant
	case strings.HasPrefix(policy, "calendar_"):
		allowed = settings.CalendarEnabled
	case policy == "machine_admin" || policy == "home_admin":
		allowed = member.Role == domain.HomeRoleAdmin
	case policy == "home_member" || policy == "evidence" || policy == "app_command":
	default:
		allowed = false
	}
	if !allowed {
		return executionToolContext{}, errors.New("permission_denied")
	}
	if feature != "" {
		if err := s.requireHomeFeature(ctx, home, member, userID, feature); err != nil {
			return executionToolContext{}, err
		}
	}
	return executionToolContext{Home: home, Member: member, UserID: userID, Settings: settings}, nil
}

func executionArg(args map[string]any, key string) string { v, _ := args[key].(string); return v }
func executionLimit(args map[string]any) int {
	if n, ok := args["limit"].(float64); ok {
		return int(n)
	}
	return 20
}
func executionMatches(query, text string) bool {
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(strings.ToLower(text), term) {
			return false
		}
	}
	return true
}
func executionText(value string) string {
	r := []rune(value)
	if len(r) > 8000 {
		return string(r[:8000])
	}
	return value
}
func executionResult(call assistant.Call, items []executionItem, limit int, prepared bool) assistant.Result {
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	if items == nil {
		items = []executionItem{}
	}
	data := executionData{Items: items, HasMore: more}
	refs := make([]assistant.Resource, 0, len(items))
	for _, item := range items {
		refs = append(refs, assistant.Resource{Type: item.Type, ID: item.ID, Scope: item.Scope, AgentID: item.AgentID, SourceID: item.SourceID, DeviceID: item.DeviceID, Revision: item.Revision})
	}
	if prepared {
		data.Prepared = call.Arguments
	}
	result := assistant.Success(call.ID, data, refs)
	if prepared {
		result.Outcome = "not_started"
		result.Verification = "pending"
	}
	return result
}

func (s *Server) executeAssistantCapability(ctx context.Context, rt executionToolContext, call assistant.Call, args map[string]any, prepared bool) assistant.Result {
	switch {
	case call.Tool == "attachments.list":
		if rt.SessionID == "" {
			return assistant.Failure(call.ID, "not_found")
		}
		stages, err := s.store.ListAssistantStages(ctx, rt.Home.ID, rt.UserID, rt.SessionID)
		if err != nil {
			return assistant.Failure(call.ID, "not_found")
		}
		items := []executionItem{}
		for _, stage := range stages {
			items = append(items, executionItem{Type: "file", ID: stage.ID, StageID: stage.ID, Title: stage.Filename, Scope: "personal", ChecksumSHA256: stage.ChecksumSHA256, SizeBytes: stage.SizeBytes})
		}
		return executionResult(call, items, 50, false)
	case call.Tool == "apps.invoke":
		app, err := s.store.GetHomeApp(ctx, rt.Home.ID, executionArg(args, "app_id"))
		if err != nil || !canUseHomeAgentAppCommand(app, rt.Member, executionArg(args, "command_id")) {
			return assistant.Failure(call.ID, "permission_denied")
		}
		if app.Version != executionArg(args, "version") {
			return assistant.Failure(call.ID, "revision_conflict")
		}
		agent, ok := s.router.ResolveAgent(rt.Home.ID, "")
		if !ok {
			return assistant.Failure(call.ID, "agent_offline")
		}
		if !s.router.supportsCurrentAgent(agent, protocol.CommandAppsInvoke) || !s.router.supportsCurrentAgent(agent, protocol.CapabilityAppsSandboxV1) {
			return assistant.Failure(call.ID, "capability_unavailable")
		}
		return executionResult(call, []executionItem{{Type: "app", ID: app.AppID, Scope: "home", Title: app.Name, AgentID: agent.agent.ID, Revision: app.Version}}, 1, true)
	case strings.HasPrefix(call.Tool, "notes."):
		return s.executionNotes(ctx, rt, call, args, prepared)
	case call.Tool == "files.sources":
		agent, ok := s.router.ResolveAgent(rt.Home.ID, "")
		if !ok {
			return assistant.Failure(call.ID, "agent_offline")
		}
		profile, err := s.store.GetHomeServiceProfile(ctx, rt.Home.ID, domain.ServiceTypeSMB)
		if err != nil {
			return assistant.Failure(call.ID, "capability_unavailable")
		}
		policy := parseFileAccessPolicy(profile.PublicConfigJSON)
		roots := policy.AllowedPrefixes
		if len(roots) == 0 {
			roots = []string{"/"}
		}
		items := []executionItem{}
		for _, sourceID := range assistantFileIndexSourceIDsFromProfileConfig(profile.PublicConfigJSON) {
			if sourceID == "" {
				continue
			}
			for _, root := range roots {
				root = cleanPolicyPath(root)
				if root == "" {
					root = "/"
				}
				if policy.allow("read", root) != nil {
					continue
				}
				items = append(items, executionItem{Type: "folder", ID: root, Scope: "home", Title: sourceID, SourceID: sourceID, AgentID: agent.agent.ID, Path: root, State: "configured"})
			}
		}
		return executionResult(call, items, 50, false)
	case strings.HasPrefix(call.Tool, "files."):
		return s.executionFiles(ctx, rt, call, args, prepared)
	case strings.HasPrefix(call.Tool, "homeassistant."):
		return s.executionHA(ctx, rt, call, args, prepared)
	case strings.HasPrefix(call.Tool, "calendar."):
		return s.executionCalendar(ctx, rt, call, args, prepared)
	case strings.HasPrefix(call.Tool, "machines.service"):
		return s.executionMachineService(ctx, rt, call, args, prepared)
	case strings.HasPrefix(call.Tool, "machines."):
		agents, err := s.store.ListAgentsByHome(ctx, rt.Home.ID)
		if err != nil {
			return assistant.Failure(call.ID, "internal_error")
		}
		items := []executionItem{}
		for _, agent := range agents {
			if id := executionArg(args, "agent_id"); id != "" && id != agent.ID {
				continue
			}
			state := "offline"
			if _, ok := s.router.ResolveAgent(rt.Home.ID, agent.ID); ok {
				state = "online"
			}
			items = append(items, executionItem{Type: "machine", ID: agent.ID, Scope: "home", Title: agent.Name, AgentID: agent.ID, State: state, URI: "/dashboard/agents/" + url.PathEscape(agent.ID)})
		}
		if call.Tool == "machines.status" && len(items) == 0 {
			return assistant.Failure(call.ID, "not_found")
		}
		return executionResult(call, items, 50, false)
	case strings.HasPrefix(call.Tool, "evidence."):
		return s.executionEvidence(ctx, rt, call, args)
	}
	return assistant.Failure(call.ID, "capability_unavailable")
}

func (s *Server) executionNotes(ctx context.Context, rt executionToolContext, call assistant.Call, args map[string]any, prepared bool) assistant.Result {
	if call.Tool == "notes.create" {
		if !rt.Settings.ProfileNotesEnabled {
			return assistant.Failure(call.ID, "permission_denied")
		}
		return executionResult(call, nil, 1, true)
	}
	notes, err := s.assistantVisibleNotes(ctx, rt.Home.ID, rt.UserID, rt.Settings)
	if err != nil {
		return assistant.Failure(call.ID, "internal_error")
	}
	items := []executionItem{}
	for _, note := range uniqueAssistantNotes(notes) {
		if note.DeletedAt != nil || (note.HomeID == "" && !rt.Settings.ProfileNotesEnabled) || (note.HomeID != "" && (!rt.Settings.HomeNotesEnabled || note.HomeID != rt.Home.ID)) {
			continue
		}
		if id := executionArg(args, "note_id"); id != "" && id != note.ID {
			continue
		}
		if !executionMatches(executionArg(args, "query"), note.Title+" "+note.Content) {
			continue
		}
		if prepared && normalizePageType(note.PageType) != protocol.NotePageTypeText {
			return assistant.Failure(call.ID, "invalid_arguments")
		}
		if prepared && executionArg(args, "revision") != note.Revision {
			return assistant.Failure(call.ID, "revision_conflict")
		}
		scope := "personal"
		if note.HomeID != "" {
			scope = "home"
		}
		text := note.Content
		if call.Tool == "notes.search" {
			text = notePreview(text)
		}
		items = append(items, executionItem{Type: "note", ID: note.ID, Scope: scope, Title: note.Title, Text: executionText(text), Revision: note.Revision, URI: "hank://notes/" + url.PathEscape(note.NoteID), UpdatedAt: note.UpdatedAt.Format(time.RFC3339Nano)})
	}
	if call.Tool != "notes.search" && len(items) != 1 {
		return assistant.Failure(call.ID, "not_found")
	}
	return executionResult(call, items, executionLimit(args), prepared)
}

// Agent-side containment still resolves symlinks at the operation itself. The
// server rejects traversal syntax rather than normalizing it into a new target.
func validExecutionPath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\\x00") || strings.TrimSpace(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return true
}

func (s *Server) executionAgentRead(ctx context.Context, rt executionToolContext, agentID, command string, body any) (protocol.Envelope, string) {
	agent, ok := s.router.ResolveAgent(rt.Home.ID, agentID)
	if !ok {
		return protocol.Envelope{}, "agent_offline"
	}
	if !s.router.supportsCurrentAgent(agent, command) {
		return protocol.Envelope{}, "capability_unavailable"
	}
	envelope, err := s.sendAgentCommandTo(ctx, rt.Home.ID, agent.agent.ID, command, body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return envelope, "timeout"
		}
		return envelope, "agent_offline"
	}
	if envelope.Error != nil {
		switch envelope.Error.Code {
		case "permission_denied", "not_found", "invalid_arguments", "capability_unavailable":
			return envelope, envelope.Error.Code
		}
		return envelope, "internal_error"
	}
	return envelope, ""
}

func (s *Server) executionFiles(ctx context.Context, rt executionToolContext, call assistant.Call, args map[string]any, prepared bool) assistant.Result {
	agentID, sourceID, folder := executionArg(args, "agent_id"), executionArg(args, "source_id"), executionArg(args, "path")
	if !validExecutionPath(folder) {
		return assistant.Failure(call.ID, "invalid_arguments")
	}
	agent, err := s.store.GetAgentByID(ctx, agentID)
	if err != nil || agent.HomeID != rt.Home.ID {
		return assistant.Failure(call.ID, "file_target_unavailable")
	}
	command := call.Tool
	if call.Tool == "files.read" {
		command = "files.download"
	}
	if prepared {
		command = "files.create_directory"
		if call.Tool == "files.upload" {
			command = "files.upload"
		}
	}
	body, _ := json.Marshal(args)
	if err := s.authorizeFileCommandPolicy(ctx, rt.Home.ID, protocol.RoutedCommand{Command: command, Body: body}); err != nil {
		return assistant.Failure(call.ID, "permission_denied")
	}
	if prepared {
		connection, ok := s.router.ResolveAgent(rt.Home.ID, agentID)
		if !ok {
			return assistant.Failure(call.ID, "agent_offline")
		}
		if !s.router.supportsCurrentAgent(connection, command) {
			return assistant.Failure(call.ID, "capability_unavailable")
		}
		epoch := s.router.assistantJournalEpoch(connection)
		if epoch == "" || !s.router.supportsCurrentAgent(connection, protocol.CommandAssistantOperationExecute) {
			return assistant.Failure(call.ID, "capability_unavailable")
		}
		parent := path.Dir(folder)
		parentBody := map[string]any{"source_id": sourceID, "path": parent}
		raw, _ := json.Marshal(parentBody)
		if err := s.authorizeFileCommandPolicy(ctx, rt.Home.ID, protocol.RoutedCommand{Command: "files.stat", Body: raw}); err != nil {
			return assistant.Failure(call.ID, "permission_denied")
		}
		envelope, code := s.executionAgentRead(ctx, rt, agentID, "files.stat", parentBody)
		if code != "" {
			return assistant.Failure(call.ID, code)
		}
		payload, err := protocol.DecodePayload[protocol.FilesStatResponse](envelope)
		if err != nil || !payload.Item.IsDirectory || cleanPolicyPath(payload.Item.Path) != cleanPolicyPath(parent) || (payload.Item.SourceID != "" && payload.Item.SourceID != sourceID) {
			return assistant.Failure(call.ID, "not_found")
		}
		target := executionItem{Type: "folder", ID: folder, Scope: "home", Title: path.Base(folder), Path: folder, AgentID: agentID, SourceID: sourceID, Revision: "epoch:" + epoch}
		if call.Tool == "files.upload" {
			stage, err := s.store.GetAssistantStage(ctx, rt.Home.ID, rt.UserID, rt.SessionID, executionArg(args, "stage_id"))
			if err != nil {
				return assistant.Failure(call.ID, "attachment_unavailable")
			}
			if !s.router.supportsCurrentAgent(connection, protocol.CommandAssistantOperationStage) {
				return assistant.Failure(call.ID, "capability_unavailable")
			}
			policyBody, _ := json.Marshal(map[string]any{"source_id": sourceID, "path": folder, "size_bytes": stage.SizeBytes})
			if err := s.authorizeFileCommandPolicy(ctx, rt.Home.ID, protocol.RoutedCommand{Command: "files.upload", Body: policyBody}); err != nil {
				return assistant.Failure(call.ID, "permission_denied")
			}
			target.Type = "file"
			target.StageID = stage.ID
			target.ChecksumSHA256 = stage.ChecksumSHA256
			target.SizeBytes = stage.SizeBytes
		}
		return executionResult(call, []executionItem{target}, 1, true)
	}
	if call.Tool == "files.read" {
		relative := strings.TrimPrefix(folder, "/")
		envelope, code := s.executionAgentRead(ctx, rt, agentID, protocol.CommandMCPContextRead, protocol.MCPContextReadRequest{SourceID: sourceID, RootPath: "", Path: relative})
		if code != "" {
			return assistant.Failure(call.ID, code)
		}
		payload, err := protocol.DecodePayload[protocol.MCPContextReadResponse](envelope)
		if err != nil || payload.Path != relative {
			return assistant.Failure(call.ID, "internal_error")
		}
		result := executionResult(call, []executionItem{{Type: "file", ID: folder, Scope: "home", Title: path.Base(folder), Path: folder, AgentID: agentID, SourceID: sourceID, Text: executionText(payload.Content), URI: executionFileURI(agentID, sourceID, folder, false)}}, 1, false)
		var data executionData
		_ = json.Unmarshal(result.Data, &data)
		data.HasMore = payload.Truncated || len([]rune(payload.Content)) > 8000
		result.Data, _ = json.Marshal(data)
		return result
	}
	envelope, code := s.executionAgentRead(ctx, rt, agentID, command, args)
	if code != "" {
		return assistant.Failure(call.ID, code)
	}
	var payload struct {
		Items []protocol.FileItem `json:"items"`
		Item  protocol.FileItem   `json:"item"`
	}
	if json.Unmarshal(envelope.Payload, &payload) != nil {
		return assistant.Failure(call.ID, "internal_error")
	}
	if call.Tool == "files.stat" {
		payload.Items = []protocol.FileItem{payload.Item}
	}
	items := []executionItem{}
	for _, file := range payload.Items {
		if file.Path == "" && file.IsDirectory && call.Tool == "files.stat" && cleanPolicyPath(folder) == "" {
			file.Path = "/"
		}
		if !validExecutionPath(file.Path) || (file.SourceID != "" && file.SourceID != sourceID) {
			continue
		}
		if call.Tool == "files.stat" && cleanPolicyPath(file.Path) != cleanPolicyPath(folder) {
			continue
		}
		if call.Tool != "files.stat" && !pathHasPolicyPrefix(cleanPolicyPath(file.Path), folder) {
			continue
		}
		check, _ := json.Marshal(protocol.FilesStatRequest{SourceID: sourceID, Path: file.Path})
		if s.authorizeFileCommandPolicy(ctx, rt.Home.ID, protocol.RoutedCommand{Command: "files.stat", Body: check}) != nil {
			continue
		}
		kind := "file"
		if file.IsDirectory {
			kind = "folder"
		}
		items = append(items, executionItem{Type: kind, ID: file.Path, Scope: "home", Title: file.Name, Path: file.Path, SourceID: sourceID, AgentID: agentID, Revision: file.ModifiedAt.Format(time.RFC3339Nano), URI: executionFileURI(agentID, sourceID, file.Path, kind == "folder")})
	}
	if call.Tool == "files.stat" && len(items) != 1 {
		return assistant.Failure(call.ID, "not_found")
	}
	return executionResult(call, items, executionLimit(args), false)
}

func (s *Server) executionHA(ctx context.Context, rt executionToolContext, call assistant.Call, args map[string]any, prepared bool) assistant.Result {
	agent, ok := s.router.ResolveAgent(rt.Home.ID, "")
	if !ok {
		return assistant.Failure(call.ID, "agent_offline")
	}
	if prepared && !s.router.supportsCurrentAgent(agent, "homeassistant.call_service") {
		return assistant.Failure(call.ID, "capability_unavailable")
	}
	epoch := ""
	if prepared {
		epoch = s.router.assistantJournalEpoch(agent)
		if epoch == "" || !s.router.supportsCurrentAgent(agent, protocol.CommandAssistantOperationExecute) {
			return assistant.Failure(call.ID, "capability_unavailable")
		}
	}
	envelope, code := s.executionAgentRead(ctx, rt, agent.agent.ID, "homeassistant.fetch_states", map[string]any{})
	if code != "" {
		return assistant.Failure(call.ID, code)
	}
	payload, err := protocol.DecodePayload[protocol.HomeAssistantFetchStatesResponse](envelope)
	if err != nil {
		return assistant.Failure(call.ID, "internal_error")
	}
	items := []executionItem{}
	for _, state := range payload.States {
		if id := executionArg(args, "entity_id"); id != "" && id != state.EntityID {
			continue
		}
		title := homeAssistantFriendlyName(state)
		if !executionMatches(executionArg(args, "query"), title+" "+state.EntityID) {
			continue
		}
		if prepared && !protocol.AssistantHAServiceAllowed(homeAssistantEntityDomain(state.EntityID), executionArg(args, "service")) {
			return assistant.Failure(call.ID, "invalid_arguments")
		}
		item := executionItem{Type: "homeassistant_entity", ID: state.EntityID, Scope: "home", AgentID: agent.agent.ID, Title: title, State: state.State, URI: "/dashboard/home-assistant?query=" + url.QueryEscape(state.EntityID)}
		if prepared {
			item.Revision = "epoch:" + epoch
		}
		if state.LastUpdated != nil {
			item.UpdatedAt = state.LastUpdated.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	if call.Tool != "homeassistant.search" && len(items) != 1 {
		return assistant.Failure(call.ID, "not_found")
	}
	return executionResult(call, items, executionLimit(args), prepared)
}

func (s *Server) executionCalendar(ctx context.Context, rt executionToolContext, call assistant.Call, args map[string]any, prepared bool) assistant.Result {
	entries, err := s.store.ListAssistantCalendarEntries(ctx, rt.Home.ID, rt.UserID)
	if err != nil {
		return assistant.Failure(call.ID, "internal_error")
	}
	if call.Tool == "calendar.update_event" || call.Tool == "calendar.create_event" {
		start, e1 := time.Parse(time.RFC3339, executionArg(args, "starts_at"))
		end, e2 := time.Parse(time.RFC3339, executionArg(args, "ends_at"))
		_, e3 := time.LoadLocation(executionArg(args, "timezone"))
		if e1 != nil || e2 != nil || e3 != nil || !end.After(start) {
			return assistant.Failure(call.ID, "invalid_arguments")
		}
	}
	if call.Tool == "calendar.create_event" {
		for _, entry := range entries {
			if entry.DeviceID == executionArg(args, "device_id") && entry.CalendarID == executionArg(args, "calendar_id") {
				items := []executionItem{{Type: "calendar", ID: entry.CalendarID, Scope: "personal", Title: executionArg(args, "title"), DeviceID: entry.DeviceID, CalendarID: entry.CalendarID, StartsAt: executionArg(args, "starts_at"), EndsAt: executionArg(args, "ends_at"), State: "proposed"}}
				return executionResult(call, items, 1, true)
			}
		}
		return assistant.Failure(call.ID, "not_found")
	}
	items := []executionItem{}
	for _, entry := range entries {
		if id := executionArg(args, "entry_id"); id != "" && id != entry.ID {
			continue
		}
		if !executionMatches(executionArg(args, "query"), entry.Title+" "+entry.Notes) {
			continue
		}
		revision := entry.UpdatedAt.Format(time.RFC3339Nano)
		if prepared && revision != executionArg(args, "revision") {
			return assistant.Failure(call.ID, "revision_conflict")
		}
		item := executionItem{Type: "calendar_event", ID: entry.ID, Scope: "personal", Title: entry.Title, Text: executionText(entry.Notes), DeviceID: entry.DeviceID, CalendarID: entry.CalendarID, EventID: entry.ExternalEventID, StartsAt: entry.StartsAt.Format(time.RFC3339), EndsAt: entry.EndsAt.Format(time.RFC3339), Revision: revision, UpdatedAt: revision, URI: "hank://calendar/" + url.PathEscape(entry.ExternalEventID) + "?" + url.Values{"device_id": {entry.DeviceID}, "calendar_id": {entry.CalendarID}}.Encode()}
		items = append(items, item)
	}
	if call.Tool != "calendar.search" && len(items) != 1 {
		return assistant.Failure(call.ID, "not_found")
	}
	return executionResult(call, items, executionLimit(args), prepared)
}

func (s *Server) executionEvidence(ctx context.Context, rt executionToolContext, call assistant.Call, args map[string]any) assistant.Result {
	source := executionArg(args, "source")
	if source == "notes" || source == "calendar" {
		policy := source + "_read"
		updated, err := s.executionToolContext(ctx, rt.Home.ID, rt.UserID, policy)
		if err != nil {
			return assistant.Failure(call.ID, "permission_denied")
		}
		nested := call
		nested.Tool = source + ".search"
		return s.executeAssistantCapability(ctx, updated, nested, args, false)
	}
	if source == "project_docs" {
		if !rt.Settings.ProjectDocsEnabled {
			return assistant.Failure(call.ID, "permission_denied")
		}
		docs, err := loadAssistantProjectDocs(s.assistantAI.ProjectDocsDir)
		if err != nil {
			return assistant.Failure(call.ID, "internal_error")
		}
		items := []executionItem{}
		for _, doc := range docs {
			if id := executionArg(args, "id"); id != "" && id != doc.Path {
				continue
			}
			if !executionMatches(executionArg(args, "query"), doc.Title+" "+doc.Content) {
				continue
			}
			text := doc.Content
			if call.Tool == "evidence.search" {
				text = notePreview(text)
			}
			items = append(items, executionItem{Type: "project_doc", ID: doc.Path, Scope: "home", Title: doc.Title, Text: executionText(text), Revision: doc.UpdatedAt.Format(time.RFC3339Nano), URI: "hank://project-docs/" + doc.Path})
		}
		if call.Tool == "evidence.read" && len(items) != 1 {
			return assistant.Failure(call.ID, "not_found")
		}
		return executionResult(call, items, executionLimit(args), false)
	}
	if source == "conversation" {
		if !rt.Settings.ConversationsEnabled {
			return assistant.Failure(call.ID, "permission_denied")
		}
		sessions, err := s.store.ListAssistantSessions(ctx, rt.Home.ID, rt.UserID)
		if err != nil {
			return assistant.Failure(call.ID, "internal_error")
		}
		items := []executionItem{}
		for _, session := range sessions {
			if id := executionArg(args, "id"); id != "" && id != session.ID {
				continue
			}
			messages, err := s.store.ListAssistantMessages(ctx, session.ID)
			if err != nil {
				return assistant.Failure(call.ID, "internal_error")
			}
			text := assistantConversationSearchText(session, messages)
			if !executionMatches(executionArg(args, "query"), text) {
				continue
			}
			if call.Tool == "evidence.search" {
				text = notePreview(text)
			}
			items = append(items, executionItem{Type: "conversation", ID: session.ID, Scope: "personal", Title: session.Title, Text: executionText(text), Revision: session.UpdatedAt.Format(time.RFC3339Nano), URI: "hank://assistant/sessions/" + session.ID})
		}
		if call.Tool == "evidence.read" && len(items) != 1 {
			return assistant.Failure(call.ID, "not_found")
		}
		return executionResult(call, items, executionLimit(args), false)
	}
	return assistant.Failure(call.ID, "invalid_arguments")
}

func executionFileURI(agentID, sourceID, filePath string, directory bool) string {
	query := url.Values{"path": {filePath}, "source_id": {sourceID}, "agent_id": {agentID}}
	if !directory {
		query.Set("preview", "1")
	}
	return "/dashboard/file-server?" + query.Encode()
}
