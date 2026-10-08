package cloud

import (
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
)

// mcpToolDef describes one MCP tool: its advertised schema and the scopes that
// authorize it (any-of). The execution lives in executeMCPTool.
type mcpToolDef struct {
	Name         string
	Title        string
	Description  string
	InputSchema  map[string]any
	OutputSchema map[string]any
	Scopes       []string
	Annotations  map[string]any
	Meta         map[string]any
}

func mcpObjectSchema(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func mcpStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func mcpInt(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func mcpBool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func mcpStringArray(desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string"}}
}
func mcpEnum(desc string, values ...string) map[string]any {
	items := make([]any, len(values))
	for index, value := range values {
		items[index] = value
	}
	return map[string]any{"type": "string", "description": desc, "enum": items}
}

var (
	mcpReadOnlyAnnotations    = map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}
	mcpSafeWriteAnnotations   = map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false}
	mcpDestructiveAnnotations = map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false}
)

func mcpToolTitle(def mcpToolDef) string {
	if title := strings.TrimSpace(def.Title); title != "" {
		return title
	}
	words := strings.Split(def.Name, "_")
	for index, word := range words {
		if word != "" {
			words[index] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func mcpToolDefs() []mcpToolDef {
	return append(mcpFleetToolDefs(), mcpCoreToolDefs()...)
}

func mcpCoreToolDefs() []mcpToolDef {
	return []mcpToolDef{
		{Name: "list_context_sources", Description: "List live read-only project context sources enabled for this Hank account.", InputSchema: mcpObjectSchema(map[string]any{}), Scopes: []string{domain.MCPScopeDocsRead}, Annotations: mcpReadOnlyAnnotations},
		{Name: "list_context_files", Description: "List files and folders inside an approved live project context source.", InputSchema: mcpObjectSchema(map[string]any{"source_id": mcpStr("Context source id."), "path": mcpStr("Optional source-relative directory path.")}, "source_id"), Scopes: []string{domain.MCPScopeDocsRead}, Annotations: mcpReadOnlyAnnotations},
		{Name: "search_context", Description: "Search filenames and approved text content in one live project context source.", InputSchema: mcpObjectSchema(map[string]any{"source_id": mcpStr("Context source id."), "query": mcpStr("Search text."), "limit": mcpInt("Maximum results, up to 50.")}, "source_id", "query"), Scopes: []string{domain.MCPScopeDocsRead}, Annotations: mcpReadOnlyAnnotations},
		{Name: "read_context_file", Description: "Read one approved text file from a live project context source.", InputSchema: mcpObjectSchema(map[string]any{"source_id": mcpStr("Context source id."), "path": mcpStr("Source-relative file path.")}, "source_id", "path"), Scopes: []string{domain.MCPScopeDocsRead}, Annotations: mcpReadOnlyAnnotations},
		{
			Name:        "list_docs",
			Description: "List the current HankServerside project documents exposed to this server (README, AGENTS.md, current docs/, schemas/, and a code-reference/ source snapshot; temporary plans are excluded). Returns relative paths to use with read_doc.",
			InputSchema: mcpObjectSchema(map[string]any{}),
			Scopes:      []string{domain.MCPScopeDocsRead},
			Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name:        "search_docs",
			Description: "Full-text search across the HankServerside project documentation. Returns matching document paths with line-numbered snippets.",
			InputSchema: mcpObjectSchema(map[string]any{
				"query": mcpStr("Search terms (space-separated)."),
				"limit": mcpInt("Max documents to return (default 10)."),
			}, "query"),
			Scopes:      []string{domain.MCPScopeDocsRead},
			Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name:        "read_doc",
			Description: "Read the full contents of one project document. path must be a relative path returned by list_docs or search_docs (e.g. 'README.md', 'docs/architecture.md').",
			InputSchema: mcpObjectSchema(map[string]any{
				"path": mcpStr("Relative document path."),
			}, "path"),
			Scopes:      []string{domain.MCPScopeDocsRead},
			Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name:        "list_notes",
			Description: "List your Hank notes (summaries: id, title, updated_at, tags).",
			InputSchema: mcpObjectSchema(map[string]any{}),
			Scopes:      []string{domain.NotesAPIScopeRead},
			Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name:        "search_notes",
			Description: "Search your Hank notes by text. Returns matching notes with previews.",
			InputSchema: mcpObjectSchema(map[string]any{
				"query": mcpStr("Search text."),
				"limit": mcpInt("Max results (default 20)."),
			}, "query"),
			Scopes:      []string{domain.NotesAPIScopeRead},
			Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name:        "list_note_tags",
			Description: "List tags used across your Hank notes, with counts.",
			InputSchema: mcpObjectSchema(map[string]any{}),
			Scopes:      []string{domain.NotesAPIScopeRead},
			Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name:        "get_note",
			Description: "Fetch a single Hank note's full content by id. Returns content and revision; keep the revision if you intend to update.",
			InputSchema: mcpObjectSchema(map[string]any{
				"note_id": mcpStr("The note id."),
			}, "note_id"),
			Scopes:      []string{domain.NotesAPIScopeRead},
			Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name:        "create_note",
			Description: "Create a new Hank note with a title and body content. Use this to save a prompt, plan, or brainstorm output from this session into Hank.",
			InputSchema: mcpObjectSchema(map[string]any{
				"title":       mcpStr("Note title."),
				"content":     mcpStr("Note body (plain text / markdown)."),
				"body_format": mcpStr("Optional body format hint, e.g. 'markdown'."),
			}, "title", "content"),
			Scopes:      []string{domain.NotesAPIScopeWrite},
			Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name:        "append_note",
			Description: "Append text to the end of an existing Hank note without rewriting it.",
			InputSchema: mcpObjectSchema(map[string]any{
				"note_id":           mcpStr("The note id."),
				"content":           mcpStr("Text to append."),
				"separator":         mcpStr("Optional separator inserted before the appended text (e.g. '\\n\\n')."),
				"expected_revision": mcpStr("Optional revision for optimistic concurrency."),
			}, "note_id", "content"),
			Scopes:      []string{domain.NotesAPIScopeAppend, domain.NotesAPIScopeWrite},
			Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name:        "update_note",
			Description: "Replace the full body of an existing Hank note. To avoid clobbering concurrent edits, pass expected_revision from a recent get_note (a conflict means re-fetch first).",
			InputSchema: mcpObjectSchema(map[string]any{
				"note_id":           mcpStr("The note id."),
				"content":           mcpStr("New full body content."),
				"title":             mcpStr("Optional new title."),
				"expected_revision": mcpStr("Optional revision for optimistic concurrency."),
			}, "note_id", "content"),
			Scopes:      []string{domain.NotesAPIScopeWrite},
			Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name:        "delete_note",
			Description: "Delete a Hank note by id. This is irreversible.",
			InputSchema: mcpObjectSchema(map[string]any{
				"note_id": mcpStr("The note id."),
			}, "note_id"),
			Scopes:      []string{domain.NotesAPIScopeDelete},
			Annotations: mcpDestructiveAnnotations,
		},
		{
			Name: "list_note_attachments", Description: "List image or HTML attachments referenced by one exact text note or Kanban card, in Markdown reference order.",
			InputSchema: mcpObjectSchema(map[string]any{"target": mcpAttachmentTargetSchema()}, "target"), OutputSchema: mcpAttachmentListSchema(),
			Scopes: []string{domain.NotesAPIScopeRead}, Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name: "read_note_attachment", Description: "Read an exact referenced attachment. Images return a native MCP image preview; HTML returns inert source text; original_chunk returns base64 chunks.",
			InputSchema: mcpObjectSchema(map[string]any{
				"target": mcpAttachmentTargetSchema(), "attachment_id": mcpStr("Exact attachment ID returned by list_note_attachments."),
				"representation": mcpEnum("Optional representation; auto chooses a safe preview.", "auto", "image_preview", "html_source", "original_chunk"),
				"offset":         map[string]any{"type": "integer", "minimum": 0, "description": "Byte offset for chunked reads."},
				"length":         map[string]any{"type": "integer", "minimum": 1, "description": "Requested bytes; capped at 4 MiB for originals and 256 KiB for HTML source."},
			}, "target", "attachment_id"), OutputSchema: mcpAttachmentReadSchema(), Scopes: []string{domain.NotesAPIScopeRead}, Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name: "start_note_attachment_upload", Description: "Start a resumable image or HTML upload to one exact note or card. Supports PNG, JPEG, GIF, WebP, and HTML up to 100 MiB.",
			InputSchema: mcpObjectSchema(map[string]any{
				"target": mcpAttachmentTargetSchema(), "filename": mcpStr("Display filename."),
				"content_type":    mcpEnum("Declared media type.", "image/png", "image/jpeg", "image/gif", "image/webp", "text/html"),
				"size_bytes":      map[string]any{"type": "integer", "minimum": 1, "maximum": 104857600, "description": "Optional total size."},
				"checksum_sha256": mcpStr("Optional SHA-256 of the complete original."), "expected_revision": mcpStr("Required current note or board revision."),
				"replace_attachment_id": mcpStr("Optional referenced attachment ID to replace in place."),
			}, "target", "filename", "content_type", "expected_revision"), OutputSchema: mcpAttachmentUploadSchema(), Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name: "get_note_attachment_upload", Description: "Get the committed offset and status of an owned resumable attachment upload.",
			InputSchema: mcpObjectSchema(map[string]any{"upload_id": mcpStr("Upload ID.")}, "upload_id"), OutputSchema: mcpAttachmentUploadSchema(),
			Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name: "upload_note_attachment_chunk", Description: "Append one strict-offset chunk to an owned upload. Send either base64 bytes or UTF-8 text, never both; each chunk is at most 4 MiB decoded.",
			InputSchema: mcpObjectSchema(map[string]any{
				"upload_id": mcpStr("Upload ID."), "offset": map[string]any{"type": "integer", "minimum": 0, "description": "Exact committed byte offset."},
				"data_base64": mcpStr("Base64-encoded binary chunk."), "text": mcpStr("UTF-8 HTML source chunk."),
			}, "upload_id", "offset"), OutputSchema: mcpAttachmentUploadSchema(), Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name: "finish_note_attachment_upload", Description: "Validate and atomically commit a fully staged upload, updating only its exact note or card target. Replacement preserves the attachment ID.",
			InputSchema: mcpObjectSchema(map[string]any{
				"upload_id": mcpStr("Upload ID."), "expected_revision": mcpStr("Optional fresh revision when retrying after a conflict."),
				"confirm_shared_replacement": mcpBool("Required when replacing an attachment referenced from multiple locations."),
			}, "upload_id"), OutputSchema: mcpAttachmentFinishSchema(), Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name: "abort_note_attachment_upload", Description: "Abort an owned open attachment upload and remove its staged bytes. Completed uploads cannot be aborted.",
			InputSchema: mcpObjectSchema(map[string]any{"upload_id": mcpStr("Upload ID.")}, "upload_id"), OutputSchema: mcpAttachmentUploadSchema(),
			Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name:         "open_kanban",
			Description:  "Use this when the user asks to open or interact with their Hank Kanban board in ChatGPT. Opens the requested exact MCP-visible board, or the configured default board.",
			InputSchema:  mcpObjectSchema(map[string]any{"board_id": mcpStr("Optional exact MCP-visible board ID.")}),
			OutputSchema: mcpKanbanSnapshotSchema(),
			Scopes:       []string{domain.NotesAPIScopeRead},
			Annotations:  mcpReadOnlyAnnotations,
			Meta: map[string]any{
				"ui": map[string]any{
					"resourceUri": mcpKanbanResourceURI,
					"visibility":  []string{"model", "app"},
				},
				"openai/outputTemplate":          mcpKanbanResourceURI,
				"openai/toolInvocation/invoking": "Opening Kanban…",
				"openai/toolInvocation/invoked":  "Kanban ready.",
			},
		},
		{
			Name: "list_kanban_boards", Description: "List your MCP-visible profile Notes Kanban boards, ordered columns, workflow roles, intake configuration, card counts, revisions, and usable default board.",
			InputSchema: mcpObjectSchema(map[string]any{}), OutputSchema: mcpKanbanBoardListSchema(), Scopes: []string{domain.NotesAPIScopeRead}, Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name: "list_kanban_cards", Description: "List and filter ordered cards on an exact board_id or the configured default Kanban board. Completed cards are hidden unless requested. After a human handoff, list the configured intake column and continue with its first ordered unblocked card.",
			InputSchema: mcpObjectSchema(map[string]any{
				"board_id":         mcpStr("Optional exact board ID; defaults to the configured default board."),
				"column_id":        mcpStr("Optional exact column ID."),
				"role":             mcpEnum("Optional semantic column role.", "planning", "active", "rework", "human", "review", "complete"),
				"query":            mcpStr("Optional case-insensitive title and Markdown details query."),
				"tags":             mcpStringArray("Optional tags; every supplied tag must match."),
				"priority":         mcpEnum("Optional priority filter.", "low", "medium", "high"),
				"due_from":         mcpStr("Optional inclusive YYYY-MM-DD lower due-date bound."),
				"due_through":      mcpStr("Optional inclusive YYYY-MM-DD upper due-date bound."),
				"include_complete": mcpBool("Include cards in a column with the complete role."),
				"limit":            map[string]any{"type": "integer", "description": "Maximum results; defaults to 50.", "minimum": 1, "maximum": 100},
			}), OutputSchema: mcpKanbanCardListSchema(), Scopes: []string{domain.NotesAPIScopeRead}, Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name: "get_kanban_card", Description: "Read one Kanban card by exact card_id from an exact board_id or the configured default board, including full Markdown details, workflow context, and metadata plus authenticated download URLs for screenshots or files referenced by that card.",
			InputSchema: mcpObjectSchema(map[string]any{
				"board_id": mcpStr("Optional exact board ID; defaults to the configured default board."),
				"card_id":  mcpStr("Exact card ID returned by a Kanban read tool."),
			}, "card_id"), OutputSchema: mcpKanbanCardResultSchema(), Scopes: []string{domain.NotesAPIScopeRead}, Annotations: mcpReadOnlyAnnotations,
		},
		{
			Name: "create_kanban_card", Description: "Create a Kanban card. Call only after the user explicitly asks to capture a task. Uses an exact destination or the configured default board and intake column.",
			InputSchema: mcpObjectSchema(map[string]any{
				"board_id":         mcpStr("Optional exact board ID; defaults to the configured default board."),
				"column_id":        mcpStr("Optional exact destination column ID; defaults to intake then first column."),
				"title":            mcpStr("Required card title."),
				"details_markdown": mcpStr("Optional Markdown details."),
				"due_date":         mcpStr("Optional YYYY-MM-DD due date."),
				"tags":             mcpStringArray("Optional card tags."),
				"priority":         mcpEnum("Optional card priority.", "low", "medium", "high"),
			}, "title"), OutputSchema: mcpKanbanCardResultSchema(), Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name: "update_kanban_card", Description: "Patch supplied fields on one exact Kanban card without rewriting unrelated card or board data.",
			InputSchema: mcpObjectSchema(map[string]any{
				"board_id":         mcpStr("Optional exact board ID; defaults to the configured default board."),
				"card_id":          mcpStr("Exact card ID returned by a Kanban read tool."),
				"title":            mcpStr("Optional replacement title; cannot be empty."),
				"details_markdown": mcpStr("Optional replacement Markdown details."),
				"due_date":         mcpStr("Optional YYYY-MM-DD due date; empty clears it."),
				"tags":             mcpStringArray("Optional replacement tags; empty clears them."),
				"priority":         mcpEnum("Optional replacement priority.", "low", "medium", "high"),
			}, "card_id"), OutputSchema: mcpKanbanCardResultSchema(), Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name: "append_kanban_worklog", Description: "Append a server-dated progress, verification, blocker, or outcome entry while preserving the card's original Markdown details. Before a human handoff, use a blocker entry to preserve the decision, approval, or review needed.",
			InputSchema: mcpObjectSchema(map[string]any{
				"board_id":       mcpStr("Optional exact board ID; defaults to the configured default board."),
				"card_id":        mcpStr("Exact card ID returned by a Kanban read tool."),
				"entry_markdown": mcpStr("Markdown work-log entry to append."),
				"kind":           mcpEnum("Work-log entry kind.", "progress", "verification", "blocker", "outcome"),
			}, "card_id", "entry_markdown", "kind"), OutputSchema: mcpKanbanCardResultSchema(), Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name: "move_kanban_card", Description: "Move or reorder one exact Kanban card in an exact destination column. For a human handoff, prefer the human role for unfinished decisions or approvals and the review role for completed work awaiting validation, falling back to the other configured role. Then continue with the next intake card rather than waiting. The server does not impose a workflow order.",
			InputSchema: mcpObjectSchema(map[string]any{
				"board_id":         mcpStr("Optional exact board ID; defaults to the configured default board."),
				"card_id":          mcpStr("Exact card ID returned by a Kanban read tool."),
				"target_column_id": mcpStr("Exact destination column ID returned by a Kanban read tool."),
				"target_index":     map[string]any{"type": "integer", "description": "Optional zero-based destination index; defaults to the end.", "minimum": 0},
			}, "card_id", "target_column_id"), OutputSchema: mcpKanbanCardResultSchema(), Scopes: []string{domain.NotesAPIScopeWrite}, Annotations: mcpSafeWriteAnnotations,
		},
		{
			Name:        "delete_kanban_card",
			Description: "Use this when the user explicitly confirms deleting one exact Hank Kanban card. The deletion is irreversible and does not delete referenced attachments.",
			InputSchema: mcpObjectSchema(map[string]any{
				"board_id":                mcpStr("Exact MCP-visible board ID."),
				"card_id":                 mcpStr("Exact card ID."),
				"expected_board_revision": mcpStr("Authoritative board revision shown when deletion was confirmed."),
				"confirmed":               mcpBool("Must be true after the user confirms inside the widget."),
			}, "board_id", "card_id", "expected_board_revision", "confirmed"),
			OutputSchema: mcpKanbanDeleteSchema(),
			Scopes:       []string{domain.NotesAPIScopeDelete},
			Annotations:  mcpDestructiveAnnotations,
		},
	}
}

func mcpKanbanSnapshotSchema() map[string]any {
	permissionSchema := mcpObjectSchema(map[string]any{
		"can_read": mcpBool("Whether board reads are authorized."), "can_write": mcpBool("Whether card writes are authorized."), "can_delete": mcpBool("Whether card deletion is authorized."),
	}, "can_read", "can_write", "can_delete")
	columnSchema := mcpObjectSchema(map[string]any{
		"column_id": mcpStr("Column ID."), "title": mcpStr("Column title."), "role": mcpStr("Optional workflow role."),
		"cards": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
	}, "column_id", "title", "cards")
	boardSchema := mcpObjectSchema(map[string]any{
		"board_id": mcpStr("Board ID."), "title": mcpStr("Board title."), "revision": mcpStr("Authoritative board revision."),
		"intake_column_id": mcpStr("Optional intake column ID."), "columns": map[string]any{"type": "array", "items": columnSchema},
	}, "board_id", "title", "revision", "columns")
	return mcpObjectSchema(map[string]any{
		"state_version":  mcpStr("Latest authoritative board state version."),
		"boards":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		"selected_board": map[string]any{"anyOf": []any{boardSchema, map[string]any{"type": "null"}}},
		"permissions":    permissionSchema,
	}, "state_version", "boards", "selected_board", "permissions")
}

func mcpKanbanDeleteSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"board_id": mcpStr("Board ID."), "card_id": mcpStr("Deleted card ID."),
		"board_revision": mcpStr("New board revision."), "state_version": mcpStr("New authoritative state version."),
		"deleted": mcpBool("Whether the exact card was deleted."),
	}, "board_id", "card_id", "board_revision", "state_version", "deleted")
}

func mcpAttachmentTargetSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"kind":     mcpEnum("Exact target kind.", "note", "kanban_card"),
		"note_id":  mcpStr("Exact public note ID when kind is note."),
		"board_id": mcpStr("Exact public board ID when kind is kanban_card."),
		"card_id":  mcpStr("Exact card ID when kind is kanban_card."),
	}, "kind")
}

func mcpAttachmentProtocolSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"id": mcpStr("Attachment ID."), "note_id": mcpStr("Owning public note ID."), "note_revision": mcpStr("Current note revision."),
		"filename": mcpStr("Filename."), "content_type": mcpStr("Media type."), "size_bytes": mcpInt("Original byte size."),
		"checksum_sha256": mcpStr("Original SHA-256."), "download_url": mcpStr("Authenticated download URL."),
		"preview_url":        mcpStr("Authenticated locked preview URL."),
		"markdown_reference": mcpStr("Canonical Markdown reference."), "created_at": mcpStr("Creation time."), "updated_at": mcpStr("Update time."),
	}, "id", "note_id", "filename", "content_type", "size_bytes", "checksum_sha256", "created_at", "updated_at")
}

func mcpAttachmentListSchema() map[string]any {
	item := mcpAttachmentProtocolSchema()
	item["properties"].(map[string]any)["reference_count"] = mcpInt("Number of exact target locations referencing this attachment.")
	item["properties"].(map[string]any)["readable"] = mcpBool("Whether MCP can read this media type.")
	item["properties"].(map[string]any)["replaceable"] = mcpBool("Whether MCP can replace this media type.")
	return mcpObjectSchema(map[string]any{"target": mcpAttachmentTargetSchema(), "revision": mcpStr("Current target revision."), "attachments": map[string]any{"type": "array", "items": item}}, "target", "revision", "attachments")
}

func mcpAttachmentReadSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"attachment_id": mcpStr("Attachment ID."), "representation": mcpStr("Returned representation."),
		"offset": mcpInt("Starting byte offset."), "next_offset": mcpInt("Next byte offset."), "total_size": mcpInt("Original size."),
		"checksum_sha256": mcpStr("Original SHA-256."), "eof": mcpBool("Whether the representation read is complete."),
	}, "attachment_id", "representation", "offset", "next_offset", "total_size", "checksum_sha256", "eof")
}

func mcpAttachmentUploadSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"upload_id": mcpStr("Upload ID."), "status": mcpStr("Upload status."), "filename": mcpStr("Filename."), "content_type": mcpStr("Media type."),
		"received_bytes": mcpInt("Committed bytes."), "next_offset": mcpInt("Required next offset."), "size_bytes": mcpInt("Optional declared total size."),
		"complete": mcpBool("Whether all declared bytes are staged."), "expires_at": mcpStr("Expiry time."), "completed_at": mcpStr("Completion time."),
	}, "upload_id", "status", "filename", "content_type", "received_bytes", "next_offset", "complete", "expires_at")
}

func mcpAttachmentFinishSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"attachment": mcpAttachmentProtocolSchema(), "target_revision": mcpStr("New target revision."),
		"reference_count": mcpInt("Number of target locations referencing the committed attachment."), "status": mcpStr("Completion status."),
	}, "attachment", "target_revision", "reference_count", "status")
}

func mcpKanbanColumnSummarySchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"column_id": mcpStr("Column ID."), "title": mcpStr("Column title."), "role": mcpStr("Optional workflow role."),
		"card_count": mcpInt("Number of cards in the column."),
	}, "column_id", "title", "card_count")
}

func mcpKanbanBoardSummarySchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"board_id": mcpStr("Board ID."), "title": mcpStr("Board title."), "default": mcpBool("Whether this is the usable default board."),
		"intake_column_id": mcpStr("Optional intake column ID."), "revision": mcpStr("Board revision."),
		"columns":          map[string]any{"type": "array", "items": mcpKanbanColumnSummarySchema()},
		"total_card_count": mcpInt("Total cards."), "active_card_count": mcpInt("Cards outside complete columns."),
	}, "board_id", "title", "default", "revision", "columns", "total_card_count", "active_card_count")
}

func mcpKanbanAttachmentSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"id": mcpStr("Attachment ID."), "note_id": mcpStr("Owning note ID."), "note_revision": mcpStr("Optional note revision."),
		"filename": mcpStr("Filename."), "content_type": mcpStr("Media type."), "size_bytes": map[string]any{"type": "integer"},
		"checksum_sha256": mcpStr("SHA-256 checksum."), "download_url": mcpStr("Optional authenticated download URL."),
		"preview_url":        mcpStr("Optional authenticated locked preview URL."),
		"markdown_reference": mcpStr("Optional Markdown reference."), "created_at": mcpStr("Creation timestamp."), "updated_at": mcpStr("Update timestamp."),
	}, "id", "note_id", "filename", "content_type", "size_bytes", "checksum_sha256", "created_at", "updated_at")
}

func mcpKanbanCardSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"board_id": mcpStr("Board ID."), "board_title": mcpStr("Board title."), "board_revision": mcpStr("Board revision."),
		"column_id": mcpStr("Column ID."), "column_title": mcpStr("Column title."), "column_role": mcpStr("Optional workflow role."),
		"card_id": mcpStr("Card ID."), "title": mcpStr("Card title."), "details_markdown": mcpStr("Card Markdown details."),
		"due_date": mcpStr("Optional due date."), "tags": mcpStringArray("Card tags."), "priority": mcpStr("Optional priority."), "color": mcpStr("Optional color."),
		"created_at": mcpStr("Optional creation timestamp."), "updated_at": mcpStr("Optional update timestamp."),
		"columns":     map[string]any{"type": "array", "items": mcpKanbanColumnSummarySchema()},
		"attachments": map[string]any{"type": "array", "items": mcpKanbanAttachmentSchema()},
	}, "board_id", "board_title", "board_revision", "column_id", "column_title", "card_id", "title", "details_markdown", "tags")
}

func mcpKanbanBoardListSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"boards":        map[string]any{"type": "array", "items": mcpKanbanBoardSummarySchema()},
		"state_version": mcpStr("Deterministic version of the visible board list."),
	}, "boards", "state_version")
}

func mcpKanbanCardListSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"cards":         map[string]any{"type": "array", "items": mcpKanbanCardSchema()},
		"state_version": mcpStr("Authoritative selected-board revision."),
	}, "cards", "state_version")
}

func mcpKanbanCardResultSchema() map[string]any {
	return mcpObjectSchema(map[string]any{
		"card": mcpKanbanCardSchema(), "state_version": mcpStr("Authoritative selected-board revision."),
	}, "card", "state_version")
}

func mcpOAuthSecuritySchemes(scopes []string) []map[string]any {
	schemes := make([]map[string]any, 0, len(scopes))
	for _, scope := range scopes {
		schemes = append(schemes, map[string]any{
			"type":   "oauth2",
			"scopes": []string{scope},
		})
	}
	return schemes
}

func mcpToolListFromDefs(defs []mcpToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		item := map[string]any{
			"name":        d.Name,
			"title":       mcpToolTitle(d),
			"description": d.Description,
			"inputSchema": d.InputSchema,
		}
		if len(d.Annotations) > 0 {
			item["annotations"] = d.Annotations
		}
		if len(d.OutputSchema) > 0 {
			item["outputSchema"] = d.OutputSchema
		}
		meta := make(map[string]any, len(d.Meta)+1)
		for key, value := range d.Meta {
			meta[key] = value
		}
		if schemes := mcpOAuthSecuritySchemes(d.Scopes); len(schemes) > 0 {
			item["securitySchemes"] = schemes
			meta["securitySchemes"] = schemes
		}
		if len(meta) > 0 {
			item["_meta"] = meta
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) mcpToolList() []map[string]any {
	return mcpToolListFromDefs(mcpToolDefs())
}

func (s *Server) mcpToolByName(name string) (mcpToolDef, bool) {
	for _, d := range mcpToolDefs() {
		if d.Name == name {
			return d, true
		}
	}
	return mcpToolDef{}, false
}
