package protocol

import (
	"encoding/json"
	"time"
)

const (
	NotePageTypeText     = "text"
	NotePageTypeKanban   = "kanban"
	NotePageTypeNotebook = "notebook"
)

type KanbanCard struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	SortOrder int       `json:"sort_order"`
	Color     string    `json:"color,omitempty"`
	DueDate   string    `json:"due_date,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Priority  string    `json:"priority,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type KanbanColumn struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Role      string       `json:"role,omitempty"`
	SortOrder int          `json:"sort_order"`
	Cards     []KanbanCard `json:"cards"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type KanbanBoard struct {
	IntakeColumnID string         `json:"intake_column_id,omitempty"`
	Columns        []KanbanColumn `json:"columns"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type NoteSummary struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Revision    string     `json:"revision"`
	Size        int64      `json:"size"`
	Attachments int        `json:"attachments,omitempty"`
	StorageKey  string     `json:"storage_key,omitempty"`
	PageType    string     `json:"page_type,omitempty"`
	ParentID    string     `json:"parent_id,omitempty"`
	MCPExcluded bool       `json:"mcp_excluded"`
	Pinned      bool       `json:"pinned"`
	SortOrder   int        `json:"sort_order"`
	BodyFormat  string     `json:"body_format,omitempty"`
	OwnerUserID string     `json:"owner_user_id,omitempty"`
	Shared      bool       `json:"shared,omitempty"`
	Preview     string     `json:"preview,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

type NotesListResponse struct {
	Notes []NoteSummary `json:"notes"`
}

type NotesFetchRequest struct {
	NoteID string `json:"note_id"`
}

type NotesFetchResponse struct {
	NoteID       string           `json:"note_id"`
	Title        string           `json:"title"`
	Content      string           `json:"content"`
	BodyMarkdown string           `json:"body_markdown,omitempty"`
	BodyFormat   string           `json:"body_format,omitempty"`
	Revision     string           `json:"revision"`
	UpdatedAt    time.Time        `json:"updated_at"`
	PageType     string           `json:"page_type,omitempty"`
	ParentID     string           `json:"parent_id,omitempty"`
	MCPExcluded  bool             `json:"mcp_excluded"`
	Pinned       bool             `json:"pinned"`
	SortOrder    int              `json:"sort_order"`
	OwnerUserID  string           `json:"owner_user_id,omitempty"`
	Shared       bool             `json:"shared,omitempty"`
	Preview      string           `json:"preview,omitempty"`
	Tags         []string         `json:"tags,omitempty"`
	Board        *KanbanBoard     `json:"board,omitempty"`
	Attachments  []NoteAttachment `json:"attachments,omitempty"`
}

type NoteAttachment struct {
	ID             string    `json:"id"`
	NoteID         string    `json:"note_id"`
	NoteRevision   string    `json:"note_revision,omitempty"`
	Filename       string    `json:"filename"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	DownloadURL    string    `json:"download_url,omitempty"`
	PreviewURL     string    `json:"preview_url,omitempty"`
	MarkdownRef    string    `json:"markdown_reference,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type NoteAttachmentsResponse struct {
	Attachments []NoteAttachment `json:"attachments"`
}

type NoteAttachmentDeleteResponse struct {
	OK              bool   `json:"ok"`
	NoteRevision    string `json:"note_revision"`
	CleanupComplete bool   `json:"cleanup_complete"`
}

type NotesSaveRequest struct {
	NoteID           string       `json:"note_id"`
	Title            string       `json:"title"`
	Content          string       `json:"content"`
	BodyMarkdown     string       `json:"body_markdown,omitempty"`
	BodyFormat       string       `json:"body_format,omitempty"`
	ExpectedRevision string       `json:"expected_revision,omitempty"`
	PageType         string       `json:"page_type,omitempty"`
	ParentID         *string      `json:"parent_id,omitempty"`
	MCPExcluded      *bool        `json:"mcp_excluded,omitempty"`
	Pinned           *bool        `json:"pinned,omitempty"`
	SortOrder        *int         `json:"sort_order,omitempty"`
	Board            *KanbanBoard `json:"board,omitempty"`
}

type NotesSaveResponse struct {
	NoteID    string    `json:"note_id"`
	Revision  string    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
	PageType  string    `json:"page_type,omitempty"`
}

type NotesAppendRequest struct {
	Content          string  `json:"content"`
	BodyMarkdown     string  `json:"body_markdown,omitempty"`
	Separator        *string `json:"separator,omitempty"`
	ExpectedRevision string  `json:"expected_revision,omitempty"`
}

type NotesRenameRequest struct {
	NoteID string `json:"note_id"`
	Title  string `json:"title"`
}

type NotesDeleteRequest struct {
	NoteID string `json:"note_id"`
}

type NotesSearchRequest struct {
	Query      string `json:"query"`
	Limit      int    `json:"limit,omitempty"`
	NotebookID string `json:"notebook_id,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
}

type NoteSearchResult struct {
	NoteID        string `json:"note_id"`
	Title         string `json:"title"`
	PageType      string `json:"page_type,omitempty"`
	ParentID      string `json:"parent_id,omitempty"`
	Preview       string `json:"preview"`
	MatchLocation int    `json:"match_location"`
	LineIndex     int    `json:"line_index"`
}

type NotesSearchResponse struct {
	Results []NoteSearchResult `json:"results"`
}

type NoteTagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

type NotesTagsResponse struct {
	Tags []NoteTagCount `json:"tags"`
}

type NotesTagRollupRequest struct {
	Tag string `json:"tag"`
}

type TaggedLineRollupItem struct {
	NoteID    string `json:"note_id"`
	NoteTitle string `json:"note_title"`
	PageType  string `json:"page_type,omitempty"`
	Tag       string `json:"tag"`
	LineText  string `json:"line_text"`
	LineIndex int    `json:"line_index"`
}

type NotesTagRollupResponse struct {
	Items []TaggedLineRollupItem `json:"items"`
}

type NotesSyncResponse struct {
	Notes []NoteSummary `json:"notes"`
}

type NotesShareSummary struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	SharedBy  string    `json:"shared_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type NotesSharesResponse struct {
	Shares []NotesShareSummary `json:"shares"`
}

type NotesShareCreateRequest struct {
	UserID string `json:"user_id"`
}

type NoteCollaborationJoinRequest struct {
	NoteID    string `json:"note_id"`
	SessionID string `json:"session_id"`
	Scope     string `json:"scope,omitempty"`
}

type NoteCollaborationLeaveRequest struct {
	NoteID    string `json:"note_id"`
	SessionID string `json:"session_id"`
	Scope     string `json:"scope,omitempty"`
}

type NoteCollaborationSyncRequest struct {
	NoteID        string `json:"note_id"`
	SessionID     string `json:"session_id"`
	Scope         string `json:"scope,omitempty"`
	AfterVersion  int64  `json:"after_version,omitempty"`
	MaxOperations int    `json:"max_operations,omitempty"`
}

type NoteCollaborationOperation struct {
	OpID        string          `json:"op_id"`
	Type        string          `json:"type"`
	Field       string          `json:"field,omitempty"`
	Value       json.RawMessage `json:"value,omitempty"`
	Index       int             `json:"index,omitempty"`
	DeleteCount int             `json:"delete_count,omitempty"`
	Text        string          `json:"text,omitempty"`
	ID          string          `json:"id,omitempty"`
	ParentID    string          `json:"parent_id,omitempty"`
	ToIndex     int             `json:"to_index,omitempty"`
}

type NoteCollaborationSubmitOpsRequest struct {
	NoteID      string                       `json:"note_id"`
	SessionID   string                       `json:"session_id"`
	Scope       string                       `json:"scope,omitempty"`
	BaseVersion int64                        `json:"base_version"`
	Ops         []NoteCollaborationOperation `json:"ops"`
}

type NoteCollaborationAck struct {
	NoteID         string `json:"note_id"`
	SessionID      string `json:"session_id"`
	AppliedVersion int64  `json:"applied_version"`
	AcceptedOps    int    `json:"accepted_ops"`
	Revision       string `json:"revision"`
}

type NoteCollaborationPresenceUser struct {
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	JoinedAt  time.Time `json:"joined_at"`
}

type NoteCollaborationSnapshot struct {
	NoteID         string                          `json:"note_id"`
	AppliedVersion int64                           `json:"applied_version"`
	Revision       string                          `json:"revision"`
	Note           NotesFetchResponse              `json:"note"`
	Presence       []NoteCollaborationPresenceUser `json:"presence,omitempty"`
}

type NoteCollaborationAppliedOp struct {
	NoteCollaborationOperation
	ActorUserID    string    `json:"actor_user_id"`
	SessionID      string    `json:"session_id"`
	BaseVersion    int64     `json:"base_version"`
	AppliedVersion int64     `json:"applied_version"`
	CreatedAt      time.Time `json:"created_at"`
}

type NoteCollaborationOpsEvent struct {
	NoteID         string                       `json:"note_id"`
	AppliedVersion int64                        `json:"applied_version"`
	Revision       string                       `json:"revision"`
	Ops            []NoteCollaborationAppliedOp `json:"ops"`
}

type NoteCollaborationPresenceEvent struct {
	NoteID   string                          `json:"note_id"`
	Presence []NoteCollaborationPresenceUser `json:"presence"`
}

type NoteCollaborationRevokedEvent struct {
	NoteID string `json:"note_id"`
	Reason string `json:"reason"`
}
