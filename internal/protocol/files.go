package protocol

import "time"

const (
	FileTransferOperationDownload = "download"
	FileTransferOperationUpload   = "upload"
)

const (
	FileOperationMove     = "move"
	FileOperationCopy     = "copy"
	FileOperationDelete   = "delete"
	FileOperationUpload   = "upload"
	FileOperationDownload = "download"
)

type FileItem struct {
	SourceID    string    `json:"source_id,omitempty"`
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	IsDirectory bool      `json:"is_directory"`
	Size        int64     `json:"size"`
	ModifiedAt  time.Time `json:"modified_at"`
}

type FilesListRequest struct {
	SourceID string `json:"source_id,omitempty"`
	Path     string `json:"path"`
}

type FilesListResponse struct {
	Items []FileItem `json:"items"`
}

// FilesListPageRequest is used by the background catalog worker. The cursor
// refers to a short-lived agent-side snapshot of one directory listing.
type FilesListPageRequest struct {
	SourceID string `json:"source_id,omitempty"`
	Path     string `json:"path"`
	Cursor   string `json:"cursor,omitempty"`
}

type FilesListPageResponse struct {
	Items      []FileItem `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// FileSourceInfo contains only the fields needed to route and authorize search.
// Revision is a keyed digest of the source configuration. Credentials and
// local root paths never leave the agent for indexing.
type FileSourceInfo struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Revision        string   `json:"revision,omitempty"`
	Enabled         bool     `json:"enabled"`
	Readable        bool     `json:"readable"`
	AllowedPrefixes []string `json:"allowed_prefixes,omitempty"`
	BlockedPrefixes []string `json:"blocked_prefixes,omitempty"`
}

type FilesSourcesResponse struct {
	Sources         []FileSourceInfo `json:"sources"`
	DefaultSourceID string           `json:"default_source_id,omitempty"`
}

type FilesStatRequest struct {
	SourceID string `json:"source_id,omitempty"`
	Path     string `json:"path"`
}

type FilesStatResponse struct {
	Item FileItem `json:"item"`
}

type FilesSearchRequest struct {
	SourceID string `json:"source_id,omitempty"`
	Query    string `json:"query"`
	Limit    int    `json:"limit,omitempty"`
}

type FilesSearchResponse struct {
	Items []FileItem `json:"items"`
}

type FilesCreateDirectoryRequest struct {
	SourceID string `json:"source_id,omitempty"`
	Path     string `json:"path"`
}

type FilesRenameRequest struct {
	SourceID string `json:"source_id,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`
}

type FilesMoveRequest struct {
	SourceID            string `json:"source_id,omitempty"`
	DestinationSourceID string `json:"destination_source_id,omitempty"`
	JobID               string `json:"job_id,omitempty"`
	From                string `json:"from"`
	To                  string `json:"to"`
	IsDirectory         bool   `json:"is_directory"`
}

type FileOperationJobResponse struct {
	OK         bool   `json:"ok"`
	JobID      string `json:"job_id,omitempty"`
	Status     string `json:"status,omitempty"`
	BytesTotal int64  `json:"bytes_total,omitempty"`
	BytesDone  int64  `json:"bytes_done,omitempty"`
	FilesTotal int64  `json:"files_total,omitempty"`
	FilesDone  int64  `json:"files_done,omitempty"`
}

type FileOperationJobEvent struct {
	JobID               string `json:"job_id"`
	Status              string `json:"status"`
	SourceID            string `json:"source_id,omitempty"`
	DestinationSourceID string `json:"destination_source_id,omitempty"`
	From                string `json:"from,omitempty"`
	To                  string `json:"to,omitempty"`
	IsDirectory         bool   `json:"is_directory,omitempty"`
	BytesTotal          int64  `json:"bytes_total,omitempty"`
	BytesDone           int64  `json:"bytes_done,omitempty"`
	FilesTotal          int64  `json:"files_total,omitempty"`
	FilesDone           int64  `json:"files_done,omitempty"`
	ErrorMessage        string `json:"error_message,omitempty"`
}

type FilesMoveCancelRequest struct {
	JobID string `json:"job_id"`
}

type FilesMoveRollbackRequest struct {
	JobID               string `json:"job_id"`
	DestinationSourceID string `json:"destination_source_id,omitempty"`
	To                  string `json:"to"`
	IsDirectory         bool   `json:"is_directory"`
}

type FilesDeleteRequest struct {
	SourceID    string `json:"source_id,omitempty"`
	Path        string `json:"path"`
	IsDirectory bool   `json:"is_directory"`
}

type FilesDownloadRequest struct {
	SourceID string `json:"source_id,omitempty"`
	Path     string `json:"path"`
}

type FilesDownloadResponse struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

type FilesUploadRequest struct {
	SourceID      string `json:"source_id,omitempty"`
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

const FileTransferFlowControlV1 = "ack.v1"
const FileTransferWindowBytes int64 = 8 * 32 * 1024
const FileTransferMaxChunkBytes = 32 * 1024

type FileTransferAck struct {
	Offset int64 `json:"offset"`
}

type FileTransferOpen struct {
	FlowControl string `json:"flow_control,omitempty"`
	WindowBytes int64  `json:"window_bytes,omitempty"`
	Operation   string `json:"operation"`
	SourceID    string `json:"source_id,omitempty"`
	Path        string `json:"path"`
	Offset      int64  `json:"offset,omitempty"`
	Length      int64  `json:"length,omitempty"`
}

type FileTransferCancel struct {
	Reason string `json:"reason,omitempty"`
}

type FileTransferReady struct {
	FlowControl string `json:"flow_control,omitempty"`
	WindowBytes int64  `json:"window_bytes,omitempty"`
	Operation   string `json:"operation"`
	SourceID    string `json:"source_id,omitempty"`
	Path        string `json:"path"`
	Offset      int64  `json:"offset,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type FileTransferChunk struct {
	Offset        int64  `json:"offset,omitempty"`
	ContentBase64 string `json:"content_base64"`
}

type FileTransferComplete struct {
	Operation string `json:"operation"`
	SourceID  string `json:"source_id,omitempty"`
	Path      string `json:"path"`
	Offset    int64  `json:"offset,omitempty"`
	Size      int64  `json:"size"`
}

type EmptyResponse struct {
	OK bool `json:"ok"`
}
