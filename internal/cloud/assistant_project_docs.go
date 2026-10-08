package cloud

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

const (
	assistantProjectDocSourceType = "project_doc"
	maxAssistantProjectDocBytes   = 256 * 1024
)

type assistantProjectDoc struct {
	Path      string
	Title     string
	Content   string
	UpdatedAt time.Time
}

func (s *Server) indexAssistantProjectDocs(ctx context.Context, homeID string, userID string, settingsOverride ...domain.AssistantSettings) error {
	cfg := s.assistantAI
	cfg.normalize()
	docs, err := loadAssistantProjectDocs(cfg.ProjectDocsDir)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil
	}
	currentSourceIDs := make([]string, 0, len(docs))
	for _, doc := range docs {
		currentSourceIDs = append(currentSourceIDs, doc.Path)
		metadata, _ := json.Marshal(map[string]any{"path": doc.Path})
		sourceKey := strings.Join([]string{assistantProjectDocSourceType, homeID, doc.Path}, ":")
		searchText := strings.TrimSpace(strings.Join([]string{doc.Title, doc.Path, doc.Content}, "\n"))
		document := domain.AssistantDocument{
			ID:           stableAssistantID("adoc", sourceKey),
			HomeID:       homeID,
			SourceType:   assistantProjectDocSourceType,
			SourceID:     doc.Path,
			SourceKey:    sourceKey,
			Title:        doc.Title,
			Path:         doc.Path,
			CanonicalURI: "hank://project-docs/" + doc.Path,
			MetadataJSON: string(metadata),
			SearchText:   searchText,
			UpdatedAt:    doc.UpdatedAt,
		}
		chunks, err := s.assistantChunksForText(ctx, userID, document.ID, searchText, doc.UpdatedAt, settingsOverride...)
		if err != nil {
			return err
		}
		if err := s.store.UpsertAssistantDocumentWithChunks(ctx, document, chunks); err != nil {
			return err
		}
	}
	return s.store.PruneAssistantProjectDocuments(ctx, homeID, currentSourceIDs)
}

func loadAssistantProjectDocs(root string) ([]assistantProjectDoc, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	sandbox, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer sandbox.Close()
	paths := map[string]struct{}{}
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range rootEntries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lowered := strings.ToLower(name)
		if strings.HasSuffix(lowered, ".md") || strings.HasPrefix(lowered, "readme") {
			paths[name] = struct{}{}
		}
	}

	docsRoot := filepath.Join(root, "docs")
	if docsInfo, err := os.Stat(docsRoot); err == nil && docsInfo.IsDir() {
		if err := filepath.WalkDir(docsRoot, func(pathValue string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				relative, err := filepath.Rel(root, pathValue)
				if err != nil {
					return err
				}
				paths[filepath.ToSlash(relative)] = struct{}{}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	ordered := make([]string, 0, len(paths))
	for pathValue := range paths {
		ordered = append(ordered, pathValue)
	}
	sort.Strings(ordered)

	docs := make([]assistantProjectDoc, 0, len(ordered))
	for _, pathValue := range ordered {
		// Root.Open prevents symlink escapes even if the tree changes between
		// discovery and reading. Bound the read before allocating document content.
		file, err := sandbox.Open(filepath.FromSlash(pathValue))
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, maxAssistantProjectDocBytes))
		file.Close()
		if err != nil {
			return nil, err
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		docs = append(docs, assistantProjectDoc{
			Path:      pathValue,
			Title:     markdownTitle(pathValue, content),
			Content:   content,
			UpdatedAt: info.ModTime().UTC(),
		})
	}
	return docs, nil
}

func markdownTitle(pathValue string, content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			title := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if title != "" {
				return title
			}
		}
	}
	return strings.TrimSuffix(filepath.Base(pathValue), filepath.Ext(pathValue))
}
