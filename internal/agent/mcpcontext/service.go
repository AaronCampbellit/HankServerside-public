package mcpcontext

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const maxReadBytes int64 = 400_000
const maxSearchFiles = 10_000
const maxSearchBytes int64 = 20_000_000

var allowedExtensions = map[string]bool{".go": true, ".swift": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".py": true, ".rs": true, ".java": true, ".c": true, ".h": true, ".cpp": true, ".cs": true, ".sh": true, ".sql": true, ".html": true, ".css": true, ".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true, ".proto": true, ".mod": true, ".sum": true, ".lock": true}
var blockedDirectories = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, ".build": true, "target": true, "coverage": true, ".cache": true, "__pycache__": true}

type Service struct{ files *agentfiles.Service }

func New(files *agentfiles.Service) *Service { return &Service{files: files} }

func cleanRelative(raw string, allowEmpty bool) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if strings.HasPrefix(raw, "/") {
		return "", errors.New("absolute paths are not allowed")
	}
	clean := path.Clean(raw)
	if clean == "." && allowEmpty {
		return "", nil
	}
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path escapes context root")
	}
	return clean, nil
}

func joined(root, rel string) (string, error) {
	root, err := cleanRelative(strings.TrimLeft(root, "/"), false)
	if err != nil {
		return "", err
	}
	rel, err = cleanRelative(rel, true)
	if err != nil {
		return "", err
	}
	if rel == "" {
		return root, nil
	}
	return path.Join(root, rel), nil
}

func allowedRelative(rel string, directory bool) bool {
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") || blockedDirectories[strings.ToLower(part)] {
			return false
		}
	}
	if directory {
		return true
	}
	name := strings.ToLower(path.Base(rel))
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		return false
	}
	return allowedExtensions[strings.ToLower(path.Ext(name))]
}

func (s *Service) List(ctx context.Context, request protocol.MCPContextListRequest) (protocol.MCPContextListResponse, error) {
	base, err := joined(request.RootPath, request.Path)
	if err != nil {
		return protocol.MCPContextListResponse{}, err
	}
	items, err := s.files.ListSource(ctx, request.SourceID, base)
	if err != nil {
		return protocol.MCPContextListResponse{}, err
	}
	entries := make([]protocol.MCPContextEntry, 0, len(items))
	for _, item := range items {
		rel := strings.TrimPrefix(strings.TrimPrefix(item.Path, base), "/")
		if !allowedRelative(rel, item.IsDirectory) {
			continue
		}
		entries = append(entries, protocol.MCPContextEntry{Path: path.Join(request.Path, rel), Name: item.Name, IsDirectory: item.IsDirectory, Size: item.Size})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDirectory != entries[j].IsDirectory {
			return entries[i].IsDirectory
		}
		return entries[i].Path < entries[j].Path
	})
	return protocol.MCPContextListResponse{Entries: entries}, nil
}

func (s *Service) Read(ctx context.Context, request protocol.MCPContextReadRequest) (protocol.MCPContextReadResponse, error) {
	rel, err := cleanRelative(request.Path, false)
	if err != nil || !allowedRelative(rel, false) {
		return protocol.MCPContextReadResponse{}, errors.New("file is not exposed")
	}
	full, err := joined(request.RootPath, rel)
	if err != nil {
		return protocol.MCPContextReadResponse{}, err
	}
	reader, info, err := s.files.OpenReaderSource(ctx, request.SourceID, full, 0)
	if err != nil {
		return protocol.MCPContextReadResponse{}, err
	}
	defer reader.Close()
	if info.Size() > maxReadBytes {
		return protocol.MCPContextReadResponse{}, fmt.Errorf("file exceeds %d byte limit", maxReadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxReadBytes+1))
	if err != nil {
		return protocol.MCPContextReadResponse{}, err
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return protocol.MCPContextReadResponse{}, errors.New("binary file is not exposed")
	}
	return protocol.MCPContextReadResponse{Path: rel, Content: string(data)}, nil
}

func (s *Service) Search(ctx context.Context, request protocol.MCPContextSearchRequest) (protocol.MCPContextSearchResponse, error) {
	query := strings.ToLower(strings.TrimSpace(request.Query))
	if query == "" {
		return protocol.MCPContextSearchResponse{}, errors.New("query is required")
	}
	limit := request.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	queue := []string{""}
	visited := 0
	var inspected int64
	var results []protocol.MCPContextSearchResult
	truncated := false
	for len(queue) > 0 {
		if visited >= maxSearchFiles || inspected >= maxSearchBytes {
			truncated = true
			break
		}
		dir := queue[0]
		queue = queue[1:]
		listing, err := s.List(ctx, protocol.MCPContextListRequest{SourceID: request.SourceID, RootPath: request.RootPath, Path: dir})
		if err != nil {
			return protocol.MCPContextSearchResponse{}, err
		}
		for _, entry := range listing.Entries {
			visited++
			if entry.IsDirectory {
				queue = append(queue, entry.Path)
				continue
			}
			if len(results) >= limit {
				truncated = true
				break
			}
			hit := strings.Contains(strings.ToLower(entry.Path), query)
			preview := ""
			if !hit && inspected+entry.Size <= maxSearchBytes && entry.Size <= maxReadBytes {
				read, err := s.Read(ctx, protocol.MCPContextReadRequest{SourceID: request.SourceID, RootPath: request.RootPath, Path: entry.Path})
				if err == nil {
					inspected += int64(len(read.Content))
					low := strings.ToLower(read.Content)
					if idx := strings.Index(low, query); idx >= 0 {
						hit = true
						start := max(0, idx-80)
						end := min(len(read.Content), idx+len(query)+160)
						preview = strings.TrimSpace(read.Content[start:end])
					}
				}
			}
			if hit {
				results = append(results, protocol.MCPContextSearchResult{Path: entry.Path, Preview: preview})
			}
		}
		if len(results) >= limit {
			break
		}
	}
	return protocol.MCPContextSearchResponse{Results: results, Truncated: truncated}, nil
}

func (s *Service) Test(ctx context.Context, request protocol.MCPContextTestRequest) (protocol.MCPContextTestResponse, error) {
	_, err := s.List(ctx, protocol.MCPContextListRequest{SourceID: request.SourceID, RootPath: request.RootPath})
	return protocol.MCPContextTestResponse{OK: err == nil}, err
}
