package protocol

const (
	CommandMCPContextList   = "mcp.context.list"
	CommandMCPContextSearch = "mcp.context.search"
	CommandMCPContextRead   = "mcp.context.read"
	CommandMCPContextTest   = "mcp.context.test"
)

type MCPContextListRequest struct {
	SourceID string `json:"source_id"`
	RootPath string `json:"root_path"`
	Path     string `json:"path,omitempty"`
}
type MCPContextEntry struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	IsDirectory bool   `json:"is_directory"`
	Size        int64  `json:"size,omitempty"`
}
type MCPContextListResponse struct {
	Entries []MCPContextEntry `json:"entries"`
}
type MCPContextSearchRequest struct {
	SourceID string `json:"source_id"`
	RootPath string `json:"root_path"`
	Query    string `json:"query"`
	Limit    int    `json:"limit,omitempty"`
}
type MCPContextSearchResult struct {
	Path    string `json:"path"`
	Preview string `json:"preview"`
}
type MCPContextSearchResponse struct {
	Results   []MCPContextSearchResult `json:"results"`
	Truncated bool                     `json:"truncated,omitempty"`
}
type MCPContextReadRequest struct {
	SourceID string `json:"source_id"`
	RootPath string `json:"root_path"`
	Path     string `json:"path"`
}
type MCPContextReadResponse struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}
type MCPContextTestRequest struct {
	SourceID string `json:"source_id"`
	RootPath string `json:"root_path"`
}
type MCPContextTestResponse struct {
	OK bool `json:"ok"`
}
