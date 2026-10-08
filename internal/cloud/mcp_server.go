package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

// MCP Streamable HTTP endpoint (JSON-RPC 2.0). Mirrors the Phase-1 stdio server
// but reuses the cloud notes service and project docs directly, gated by the
// per-user OAuth access token's scopes.

const maxMCPHTTPBodyBytes = 6 << 20

const (
	mcpModernProtocolVersion = "2026-07-28"
	mcpServerVersion         = "0.7.0"
)

var mcpAdvertisedProtocolVersions = []string{
	mcpModernProtocolVersion,
}

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) handleMCPEndpoint(w http.ResponseWriter, r *http.Request) {
	setMCPNoStoreHeaders(w.Header())
	if !s.mcpIsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	s.logger.Info("mcp endpoint request",
		"method", r.Method,
		"has_auth", r.Header.Get("Authorization") != "",
		"accept", r.Header.Get("Accept"),
		"content_type", r.Header.Get("Content-Type"),
		"mcp_protocol", r.Header.Get("MCP-Protocol-Version"),
		"ua", r.UserAgent(),
	)
	if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.mcpOriginAllowed(r) {
		writeJSON(w, http.StatusForbidden, jsonrpcErrorResponse(nil, -32000, "forbidden origin"))
		return
	}
	auth, ok := s.requireMCPAuth(w, r)
	if !ok {
		return
	}
	s.handleMCPAuthenticatedEndpoint(w, r, auth)
}

func (s *Server) handleMCPAuthenticatedEndpoint(w http.ResponseWriter, r *http.Request, auth mcpAuthContext) {
	setMCPNoStoreHeaders(w.Header())
	if r.Method != http.MethodPost {
		if !s.mcpValidateLegacySession(w, r, auth) {
			return
		}
		s.serveMCPLegacy(w, r, auth, "")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPHTTPBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(nil, -32700, "could not read request body"))
		return
	}
	trimmed := bytes.TrimSpace(body)
	r.Body = io.NopCloser(bytes.NewReader(trimmed))
	if r.Header.Get("Mcp-Session-Id") != "" {
		if !s.mcpValidateLegacySession(w, r, auth) {
			return
		}
		s.serveMCPLegacy(w, r, auth, "")
		return
	}
	if version, initialize := mcpLegacyInitializeVersion(trimmed); initialize {
		// Initialization negotiates exclusively from params.protocolVersion. A
		// pre-negotiation header is neither required nor authoritative.
		r.Header.Del("MCP-Protocol-Version")
		s.serveMCPLegacy(w, r, auth, version)
		return
	}
	s.handleMCPModernRequest(w, r, auth, trimmed)
}

func setMCPNoStoreHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
}

func mcpServerInfo() map[string]any {
	return map[string]any{"name": "hank-mcp", "version": mcpServerVersion}
}

func mcpServerInstructions() string {
	return "Hank project context. Use list_docs/search_docs/read_doc to read HankServerside " +
		"documentation, and the *_note tools to read and write your Hank notes (e.g. save a plan with " +
		"create_note, or read one with get_note and act on it). Kanban tools list, read, create, update, " +
		"work-log, and move cards on your configured profile Notes Kanban boards. When an active card " +
		"needs human approval, append a blocker work-log entry and move unfinished work to Needs Human " +
		"or completed work awaiting validation to Review, falling back to the other configured role. " +
		"Then continue with the next ordered intake card rather than waiting. If neither handoff role " +
		"exists, report the configuration issue, skip the blocked card, and continue intake work."
}

// --- tool results / JSON-RPC envelope helpers ---

func jsonrpcResultResponse(id json.RawMessage, result any) map[string]any {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func jsonrpcErrorResponse(id json.RawMessage, code int, message string) map[string]any {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

type mcpToolExecution struct {
	Text              string
	Content           []map[string]any
	StructuredContent map[string]any
	Meta              map[string]any
}

func mcpToolSuccess(result mcpToolExecution) map[string]any {
	content := result.Content
	if len(content) == 0 {
		content = []map[string]any{{"type": "text", "text": result.Text}}
	}
	response := map[string]any{"content": content, "isError": false}
	if len(result.StructuredContent) > 0 {
		response["structuredContent"] = result.StructuredContent
	}
	if len(result.Meta) > 0 {
		response["_meta"] = result.Meta
	}
	return response
}

func mcpToolText(text string) map[string]any {
	return mcpToolSuccess(mcpToolExecution{Text: text})
}

func mcpToolError(message string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": message}}, "isError": true}
}

// --- tool dispatch ---

func (s *Server) mcpToolsCall(ctx context.Context, auth mcpAuthContext, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return mcpToolError("invalid tools/call params")
	}
	def, ok := s.mcpToolByName(p.Name)
	if !ok {
		return mcpToolError("Unknown tool: " + p.Name)
	}
	if !strings.HasPrefix(def.Name, "fleet_") && !mcpAuthHasAnyScope(auth, def.Scopes) {
		return mcpToolError("This connection is not authorized for " + p.Name + " (missing required scope).")
	}
	result, err := s.executeMCPTool(ctx, auth, p.Name, p.Arguments)
	if err != nil {
		return mcpToolFailure(err)
	}
	return mcpToolSuccess(result)
}

func mcpToolFailure(err error) map[string]any {
	message := mcpFriendlyError(err)
	result := mcpToolError(message)
	var attachmentErr *mcpAttachmentError
	if errors.As(err, &attachmentErr) {
		structured, marshalErr := mcpStructuredContent(attachmentErr)
		if marshalErr == nil {
			result["structuredContent"] = structured
		}
	}
	return result
}

func mcpAuthHasAnyScope(auth mcpAuthContext, scopes []string) bool {
	for _, sc := range scopes {
		if auth.hasScope(sc) {
			return true
		}
	}
	return false
}

func mcpFriendlyError(err error) string {
	var attachmentErr *mcpAttachmentError
	if errors.As(err, &attachmentErr) {
		if text, marshalErr := jsonText(attachmentErr); marshalErr == nil {
			return text
		}
	}
	var noDefault *mcpKanbanNoDefaultError
	if errors.As(err, &noDefault) {
		text, marshalErr := jsonText(map[string]any{"error": noDefault.Error(), "boards": noDefault.Boards})
		if marshalErr == nil {
			return text
		}
	}
	var conflict *mcpKanbanConflictError
	if errors.As(err, &conflict) {
		text, marshalErr := jsonText(conflict)
		if marshalErr == nil {
			return text
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		return "not found"
	}
	return err.Error()
}

func (s *Server) executeMCPTool(ctx context.Context, auth mcpAuthContext, name string, rawArgs json.RawMessage) (mcpToolExecution, error) {
	if strings.HasPrefix(name, "fleet_") {
		return s.executeMCPFleet(ctx, auth, name, rawArgs)
	}
	if result, handled, err := s.executeMCPAttachmentTool(ctx, auth, name, rawArgs); handled {
		return result, err
	}
	if name == "delete_kanban_card" {
		var args mcpKanbanDeleteArgs
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return mcpToolExecution{}, err
		}
		result, err := s.kanban.DeleteCard(ctx, auth.User.ID, args)
		if err != nil {
			return mcpToolExecution{}, err
		}
		s.audit(ctx, "mcp.tool_called", auditSeverityInfo, auth.User.ID, "", "", requestIDFromContext(ctx), "mcp_kanban_card", result.CardID, map[string]any{
			"tool": name, "client_id": auth.Token.ClientID, "board_id": result.BoardID, "card_id": result.CardID, "outcome": "deleted",
		})
		structured, err := mcpStructuredContent(result)
		if err != nil {
			return mcpToolExecution{}, err
		}
		text, err := jsonText(result)
		if err != nil {
			return mcpToolExecution{}, err
		}
		return mcpToolExecution{Text: text, StructuredContent: structured}, nil
	}
	if name == "open_kanban" {
		var args struct {
			BoardID string `json:"board_id"`
		}
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return mcpToolExecution{}, err
		}
		snapshot, err := s.kanban.OpenBoard(ctx, auth.User.ID, args.BoardID, mcpKanbanPermissions{
			CanRead: auth.hasScope(domain.NotesAPIScopeRead), CanWrite: auth.hasScope(domain.NotesAPIScopeWrite), CanDelete: auth.hasScope(domain.NotesAPIScopeDelete),
		})
		if err != nil {
			return mcpToolExecution{}, err
		}
		structured, err := mcpStructuredContent(snapshot)
		if err != nil {
			return mcpToolExecution{}, err
		}
		text := "No MCP-visible Kanban boards are available. Create a board in Hank Notes first."
		if snapshot.SelectedBoard != nil {
			cardCount := 0
			for _, column := range snapshot.SelectedBoard.Columns {
				cardCount += len(column.Cards)
			}
			text = fmt.Sprintf("Opened Kanban board %q with %d columns and %d cards.", snapshot.SelectedBoard.Title, len(snapshot.SelectedBoard.Columns), cardCount)
		}
		return mcpToolExecution{Text: text, StructuredContent: structured}, nil
	}
	text, err := s.executeMCPToolText(ctx, auth, name, rawArgs)
	if err != nil {
		return mcpToolExecution{}, err
	}
	structured, err := s.mcpKanbanStructuredResult(ctx, auth.User.ID, name, rawArgs, text)
	if err != nil {
		return mcpToolExecution{}, err
	}
	return mcpToolExecution{Text: text, StructuredContent: structured}, nil
}

func (s *Server) executeMCPAttachmentTool(ctx context.Context, auth mcpAuthContext, name string, rawArgs json.RawMessage) (mcpToolExecution, bool, error) {
	if name != "list_note_attachments" && name != "read_note_attachment" &&
		name != "start_note_attachment_upload" && name != "get_note_attachment_upload" &&
		name != "upload_note_attachment_chunk" && name != "finish_note_attachment_upload" &&
		name != "abort_note_attachment_upload" {
		return mcpToolExecution{}, false, nil
	}
	service := s.mcpAttachments
	if service == nil {
		service = newMCPNoteAttachmentService(s.store, s.noteAttachmentRoot, func() time.Time { return time.Now().UTC() })
	}
	userID := auth.User.ID
	var value any
	var content []map[string]any
	var err error
	switch name {
	case "list_note_attachments":
		var args struct {
			Target mcpAttachmentTargetArgs `json:"target"`
		}
		if err = decodeMCPToolArgs(rawArgs, &args); err == nil {
			value, err = service.List(ctx, userID, args.Target)
		}
	case "read_note_attachment":
		var args mcpAttachmentReadArgs
		if err = decodeMCPToolArgs(rawArgs, &args); err == nil {
			var result mcpAttachmentReadResult
			result, err = service.Read(ctx, userID, args)
			value, content = result, result.Content
		}
	case "start_note_attachment_upload":
		var args mcpAttachmentStartArgs
		if err = decodeMCPToolArgs(rawArgs, &args); err == nil {
			value, err = service.Start(ctx, userID, args)
		}
	case "get_note_attachment_upload":
		var args struct {
			UploadID string `json:"upload_id"`
		}
		if err = decodeMCPToolArgs(rawArgs, &args); err == nil {
			value, err = service.Status(ctx, userID, args.UploadID)
		}
	case "upload_note_attachment_chunk":
		var args mcpAttachmentChunkArgs
		if err = decodeMCPToolArgs(rawArgs, &args); err == nil {
			value, err = service.UploadChunk(ctx, userID, args)
		}
	case "finish_note_attachment_upload":
		var args mcpAttachmentFinishArgs
		if err = decodeMCPToolArgs(rawArgs, &args); err == nil {
			value, err = service.Finish(ctx, userID, args)
		}
	case "abort_note_attachment_upload":
		var args struct {
			UploadID string `json:"upload_id"`
		}
		if err = decodeMCPToolArgs(rawArgs, &args); err == nil {
			value, err = service.Abort(ctx, userID, args.UploadID)
		}
	}
	if err != nil {
		if s.metrics != nil {
			var attachmentErr *mcpAttachmentError
			if errors.As(err, &attachmentErr) {
				s.metrics.IncMCPAttachmentFailure(attachmentErr.Code)
			} else {
				s.metrics.IncMCPAttachmentFailure("unknown")
			}
		}
		return mcpToolExecution{}, true, err
	}
	if s.metrics != nil {
		if finished, ok := value.(mcpAttachmentFinishResult); ok {
			s.metrics.AddMCPAttachmentFinalizedBytes(finished.Attachment.SizeBytes)
		}
		if name != "list_note_attachments" && name != "read_note_attachment" && name != "get_note_attachment_upload" {
			s.refreshMCPAttachmentMetrics(ctx)
		}
	}
	structured, err := mcpStructuredContent(value)
	if err != nil {
		return mcpToolExecution{}, true, err
	}
	text, err := jsonText(value)
	if err != nil {
		return mcpToolExecution{}, true, err
	}
	s.audit(ctx, "mcp.tool_called", auditSeverityInfo, userID, "", "", requestIDFromContext(ctx), "mcp_note_attachment", "", map[string]any{
		"tool": name, "client_id": auth.Token.ClientID, "outcome": "ok",
	})
	return mcpToolExecution{Text: text, Content: content, StructuredContent: structured}, true, nil
}

func (s *Server) refreshMCPAttachmentMetrics(ctx context.Context) {
	if s.metrics == nil || s.store == nil {
		return
	}
	counts, stagedBytes, err := s.store.MCPNoteAttachmentUploadStats(ctx)
	if err != nil {
		return
	}
	for status, count := range counts {
		s.metrics.SetMCPAttachmentSessions(status, count)
	}
	s.metrics.SetMCPAttachmentStagedBytes(stagedBytes)
}

func mcpStructuredContent(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Server) mcpKanbanStructuredResult(ctx context.Context, userID, name string, rawArgs json.RawMessage, text string) (map[string]any, error) {
	var payload map[string]any
	switch name {
	case "list_kanban_boards":
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			return nil, err
		}
		boardsJSON, err := json.Marshal(payload["boards"])
		if err != nil {
			return nil, err
		}
		version := sha256.Sum256(boardsJSON)
		payload["state_version"] = fmt.Sprintf("sha256:%x", version)
		return payload, nil
	case "list_kanban_cards":
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			return nil, err
		}
		version := ""
		if cards, _ := payload["cards"].([]any); len(cards) > 0 {
			card, _ := cards[0].(map[string]any)
			version, _ = card["board_revision"].(string)
		}
		if version == "" {
			var args mcpKanbanListCardsArgs
			if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
				return nil, err
			}
			var err error
			version, err = s.kanban.BoardRevision(ctx, userID, args.BoardID)
			if err != nil {
				return nil, err
			}
		}
		payload["state_version"] = version
		return payload, nil
	case "get_kanban_card", "create_kanban_card", "update_kanban_card", "append_kanban_worklog", "move_kanban_card":
		var card map[string]any
		if err := json.Unmarshal([]byte(text), &card); err != nil {
			return nil, err
		}
		version, _ := card["board_revision"].(string)
		return map[string]any{"card": card, "state_version": version}, nil
	default:
		return nil, nil
	}
}

func (s *Server) executeMCPToolText(ctx context.Context, auth mcpAuthContext, name string, rawArgs json.RawMessage) (string, error) {
	userID := auth.User.ID
	profileNotes := func() ([]domain.UserNote, error) {
		return s.store.ListProfileNotes(ctx, userID, false)
	}
	requireVisibleProfileNote := func(noteID string) error {
		notes, err := profileNotes()
		if err != nil {
			return err
		}
		if !mcpNoteVisible(notes, noteID) {
			return store.ErrNotFound
		}
		return nil
	}
	switch name {
	case "list_kanban_boards":
		boards, err := s.kanban.ListBoards(ctx, userID)
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"boards": boards})
	case "list_kanban_cards":
		var args mcpKanbanListCardsArgs
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return "", err
		}
		cards, err := s.kanban.ListCards(ctx, userID, args)
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"cards": cards})
	case "get_kanban_card":
		var args struct {
			BoardID string `json:"board_id"`
			CardID  string `json:"card_id"`
		}
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return "", err
		}
		card, err := s.kanban.GetCard(ctx, userID, args.BoardID, args.CardID)
		if err != nil {
			return "", err
		}
		return jsonText(card)
	case "create_kanban_card":
		var args mcpKanbanCreateArgs
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return "", err
		}
		card, err := s.kanban.CreateCard(ctx, userID, args)
		if err != nil {
			return "", err
		}
		s.auditMCPKanbanWrite(ctx, auth, name, card, nil)
		return jsonText(card)
	case "update_kanban_card":
		var args mcpKanbanUpdateArgs
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return "", err
		}
		card, err := s.kanban.UpdateCard(ctx, userID, args)
		if err != nil {
			return "", err
		}
		s.auditMCPKanbanWrite(ctx, auth, name, card, nil)
		return jsonText(card)
	case "append_kanban_worklog":
		var args mcpKanbanWorklogArgs
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return "", err
		}
		card, err := s.kanban.AppendWorklog(ctx, userID, args)
		if err != nil {
			return "", err
		}
		s.auditMCPKanbanWrite(ctx, auth, name, card, nil)
		return jsonText(card)
	case "move_kanban_card":
		var args mcpKanbanMoveArgs
		if err := decodeMCPToolArgs(rawArgs, &args); err != nil {
			return "", err
		}
		before, err := s.kanban.GetCard(ctx, userID, args.BoardID, args.CardID)
		if err != nil {
			return "", err
		}
		card, err := s.kanban.MoveCard(ctx, userID, args)
		if err != nil {
			return "", err
		}
		s.auditMCPKanbanWrite(ctx, auth, name, card, map[string]string{"source_column_id": before.ColumnID, "destination_column_id": card.ColumnID})
		return jsonText(card)
	case "list_context_sources":
		sources, err := s.store.ListMCPContextSourcesByUser(ctx, userID, true)
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"sources": sources})
	case "list_context_files":
		var a struct {
			SourceID string `json:"source_id"`
			Path     string `json:"path"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		source, err := s.mcpContextSourceForTool(ctx, userID, a.SourceID)
		if err != nil {
			return "", err
		}
		response, err := s.callMCPContextAgent(ctx, source, protocol.CommandMCPContextList, protocol.MCPContextListRequest{SourceID: source.FileSourceID, RootPath: source.RootPath, Path: a.Path})
		if err != nil {
			return "", err
		}
		payload, err := protocol.DecodePayload[protocol.MCPContextListResponse](response)
		if err != nil {
			return "", err
		}
		return jsonText(payload)
	case "search_context":
		var a struct {
			SourceID string `json:"source_id"`
			Query    string `json:"query"`
			Limit    int    `json:"limit"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		source, err := s.mcpContextSourceForTool(ctx, userID, a.SourceID)
		if err != nil {
			return "", err
		}
		response, err := s.callMCPContextAgent(ctx, source, protocol.CommandMCPContextSearch, protocol.MCPContextSearchRequest{SourceID: source.FileSourceID, RootPath: source.RootPath, Query: a.Query, Limit: a.Limit})
		if err != nil {
			return "", err
		}
		payload, err := protocol.DecodePayload[protocol.MCPContextSearchResponse](response)
		if err != nil {
			return "", err
		}
		return jsonText(payload)
	case "read_context_file":
		var a struct {
			SourceID string `json:"source_id"`
			Path     string `json:"path"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		source, err := s.mcpContextSourceForTool(ctx, userID, a.SourceID)
		if err != nil {
			return "", err
		}
		response, err := s.callMCPContextAgent(ctx, source, protocol.CommandMCPContextRead, protocol.MCPContextReadRequest{SourceID: source.FileSourceID, RootPath: source.RootPath, Path: a.Path})
		if err != nil {
			return "", err
		}
		payload, err := protocol.DecodePayload[protocol.MCPContextReadResponse](response)
		if err != nil {
			return "", err
		}
		return jsonText(payload)
	case "list_docs":
		paths := s.mcpDocs.listPaths()
		if len(paths) == 0 {
			return "No documents are exposed. Check the MCP docs directory configuration.", nil
		}
		return "Exposed documents (" + strconv.Itoa(len(paths)) + "):\n\n" + joinLines(paths), nil
	case "search_docs":
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		return s.mcpDocs.search(a.Query, a.Limit)
	case "read_doc":
		var a struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		return s.mcpDocs.read(a.Path)
	case "list_notes":
		notes, err := profileNotes()
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"notes": noteSummaries(mcpVisibleProfileNotes(notes))})
	case "search_notes":
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		if a.Limit <= 0 {
			a.Limit = 20
		}
		notes, err := profileNotes()
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"results": searchNotes(mcpVisibleProfileNotes(notes), a.Query, a.Limit, "")})
	case "list_note_tags":
		notes, err := profileNotes()
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"tags": noteTags(mcpVisibleProfileNotes(notes))})
	case "get_note":
		var a struct {
			NoteID string `json:"note_id"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		if a.NoteID == "" {
			return "", errors.New("note_id is required")
		}
		if err := requireVisibleProfileNote(a.NoteID); err != nil {
			return "", err
		}
		note, err := s.notes.FetchProfile(ctx, userID, a.NoteID)
		if err != nil {
			return "", err
		}
		return jsonText(note)
	case "create_note":
		var a struct {
			Title      string `json:"title"`
			Content    string `json:"content"`
			BodyFormat string `json:"body_format"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		if a.Title == "" {
			return "", errors.New("title is required")
		}
		resp, err := s.notes.SaveProfile(ctx, userID, "", protocol.NotesSaveRequest{
			Title:      a.Title,
			Content:    a.Content,
			BodyFormat: a.BodyFormat,
		})
		if err != nil {
			return "", err
		}
		s.auditMCPWrite(ctx, auth, "create_note", resp.NoteID)
		return jsonText(resp)
	case "update_note":
		var a struct {
			NoteID           string `json:"note_id"`
			Content          string `json:"content"`
			Title            string `json:"title"`
			ExpectedRevision string `json:"expected_revision"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		if a.NoteID == "" {
			return "", errors.New("note_id is required")
		}
		if err := requireVisibleProfileNote(a.NoteID); err != nil {
			return "", err
		}
		resp, err := s.notes.SaveProfile(ctx, userID, a.NoteID, protocol.NotesSaveRequest{
			NoteID:           a.NoteID,
			Title:            a.Title,
			Content:          a.Content,
			ExpectedRevision: a.ExpectedRevision,
		})
		if err != nil {
			return "", err
		}
		s.auditMCPWrite(ctx, auth, "update_note", resp.NoteID)
		return jsonText(resp)
	case "append_note":
		var a struct {
			NoteID           string  `json:"note_id"`
			Content          string  `json:"content"`
			Separator        *string `json:"separator"`
			ExpectedRevision string  `json:"expected_revision"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		if a.NoteID == "" {
			return "", errors.New("note_id is required")
		}
		if err := requireVisibleProfileNote(a.NoteID); err != nil {
			return "", err
		}
		resp, err := s.notes.AppendProfile(ctx, userID, a.NoteID, protocol.NotesAppendRequest{
			Content:          a.Content,
			Separator:        a.Separator,
			ExpectedRevision: a.ExpectedRevision,
		})
		if err != nil {
			return "", err
		}
		s.auditMCPWrite(ctx, auth, "append_note", resp.NoteID)
		return jsonText(resp)
	case "delete_note":
		var a struct {
			NoteID string `json:"note_id"`
		}
		_ = json.Unmarshal(rawArgs, &a)
		if a.NoteID == "" {
			return "", errors.New("note_id is required")
		}
		if err := requireVisibleProfileNote(a.NoteID); err != nil {
			return "", err
		}
		if err := s.notes.DeleteProfile(ctx, userID, a.NoteID, ""); err != nil {
			return "", err
		}
		s.auditMCPWrite(ctx, auth, "delete_note", a.NoteID)
		return jsonText(map[string]any{"ok": true, "note_id": a.NoteID})
	default:
		return "", errors.New("unknown tool: " + name)
	}
}

func mcpVisibleProfileNotes(notes []domain.UserNote) []domain.UserNote {
	visible := make([]domain.UserNote, 0, len(notes))
	for _, note := range notes {
		if mcpNoteVisible(notes, note.NoteID) {
			visible = append(visible, note)
		}
	}
	return visible
}

func mcpNoteVisible(notes []domain.UserNote, noteID string) bool {
	byID := make(map[string]domain.UserNote, len(notes))
	for _, note := range notes {
		byID[note.NoteID] = note
	}
	note, ok := byID[noteID]
	if !ok || note.DeletedAt != nil || note.MCPExcluded {
		return false
	}
	if note.ParentID == "" {
		return true
	}
	parent, ok := byID[note.ParentID]
	if !ok || parent.DeletedAt != nil {
		return true
	}
	if normalizePageType(parent.PageType) != protocol.NotePageTypeNotebook {
		return true
	}
	return !parent.MCPExcluded
}

func (s *Server) auditMCPWrite(ctx context.Context, auth mcpAuthContext, tool string, noteID string) {
	s.audit(ctx, "mcp.tool_called", auditSeverityInfo, auth.User.ID, "", "", requestIDFromContext(ctx), "mcp_note", noteID, map[string]any{
		"tool":      tool,
		"client_id": auth.Token.ClientID,
	})
}

func (s *Server) auditMCPKanbanWrite(ctx context.Context, auth mcpAuthContext, tool string, result mcpKanbanCardResult, extra map[string]string) {
	metadata := mcpKanbanAuditMetadata(tool, auth.Token.ClientID, result, extra)
	s.audit(ctx, "mcp.tool_called", auditSeverityInfo, auth.User.ID, "", "", requestIDFromContext(ctx), "mcp_kanban_card", result.CardID, metadata)
}

func mcpKanbanAuditMetadata(tool string, clientID string, result mcpKanbanCardResult, extra map[string]string) map[string]any {
	metadata := map[string]any{
		"tool": tool, "client_id": clientID,
		"board_id": result.BoardID, "card_id": result.CardID,
		"column_id": result.ColumnID,
	}
	for key, value := range extra {
		metadata[key] = value
	}
	return metadata
}

func decodeMCPToolArgs(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func jsonText(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func joinLines(items []string) string {
	var b bytes.Buffer
	for i, it := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(it)
	}
	return b.String()
}
