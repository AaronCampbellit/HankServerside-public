package domain

import "time"

type UserNote struct {
	ID            string     `json:"id"`
	NoteID        string     `json:"note_id"`
	OwnerUserID   string     `json:"owner_user_id"`
	HomeID        string     `json:"home_id,omitempty"`
	ParentID      string     `json:"parent_id,omitempty"`
	MCPExcluded   bool       `json:"mcp_excluded"`
	Pinned        bool       `json:"pinned"`
	SortOrder     int        `json:"sort_order"`
	Title         string     `json:"title"`
	Content       string     `json:"content"`
	BodyMarkdown  string     `json:"body_markdown,omitempty"`
	BodyFormat    string     `json:"body_format,omitempty"`
	PageType      string     `json:"page_type"`
	BoardJSON     string     `json:"board_json,omitempty"`
	Revision      string     `json:"revision"`
	Checksum      string     `json:"checksum"`
	CRDTStateJSON string     `json:"crdt_state_json,omitempty"`
	CollabVersion int64      `json:"collab_version"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	UpdatedBy     string     `json:"updated_by"`
}

type NoteAttachmentInventoryRecord struct {
	Attachment   NoteAttachment `json:"attachment"`
	NoteID       string         `json:"note_id"`
	NoteTitle    string         `json:"note_title"`
	NoteScope    string         `json:"note_scope"`
	NotePageType string         `json:"note_page_type"`
	OwnerEmail   string         `json:"owner_email"`
	BodyMarkdown string         `json:"body_markdown"`
	BoardJSON    string         `json:"board_json"`
}

type NoteShare struct {
	NoteID       string    `json:"note_id"`
	HomeID       string    `json:"home_id"`
	TargetUserID string    `json:"target_user_id"`
	SharedBy     string    `json:"shared_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type NoteShareMember struct {
	NoteShare
	Email string `json:"email"`
}

type NoteOperation struct {
	NoteID         string    `json:"note_id"`
	OpID           string    `json:"op_id"`
	ActorUserID    string    `json:"actor_user_id"`
	SessionID      string    `json:"session_id"`
	BaseVersion    int64     `json:"base_version"`
	AppliedVersion int64     `json:"applied_version"`
	OpJSON         string    `json:"op_json"`
	CreatedAt      time.Time `json:"created_at"`
}
