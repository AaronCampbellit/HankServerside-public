package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/dropfile/HankServerside/internal/protocol"
)

var (
	ErrDisabled = errors.New("notes root is not configured")
	ErrConflict = errors.New("note conflict")

	tagLinePattern     = regexp.MustCompile(`^(?:[-*•]\s+|\d+\.\s+|[○●]\s+|- \[[ xX]\]\s+)`)
	sidecarSuffix      = ".kanban.json"
	defaultSearchLimit = 50
)

type Service struct {
	root string
}

type ConflictError struct {
	Current protocol.NotesFetchResponse
}

type noteDocument struct {
	summary protocol.NoteSummary
	fetch   protocol.NotesFetchResponse
}

type taggedLine struct {
	tag  string
	text string
}

func (e *ConflictError) Error() string {
	return ErrConflict.Error()
}

func (e *ConflictError) Is(target error) bool {
	return target == ErrConflict
}

func New(root string) *Service {
	return &Service{root: strings.TrimSpace(root)}
}

func (s *Service) Enabled() bool {
	return s.root != ""
}

func (s *Service) List(ctx context.Context) ([]protocol.NoteSummary, error) {
	documents, err := s.loadAll(ctx)
	if err != nil {
		return nil, err
	}

	notes := make([]protocol.NoteSummary, 0, len(documents))
	for _, document := range documents {
		notes = append(notes, document.summary)
	}
	return notes, nil
}

func (s *Service) Fetch(_ context.Context, noteID string) (protocol.NotesFetchResponse, error) {
	resolved, normalized, err := s.resolve(noteID, false)
	if err != nil {
		return protocol.NotesFetchResponse{}, err
	}
	document, err := s.loadDocument(normalized, resolved)
	if err != nil {
		return protocol.NotesFetchResponse{}, err
	}
	return document.fetch, nil
}

func (s *Service) Save(_ context.Context, noteID string, title string, content string, expectedRevision string, pageType string, board *protocol.KanbanBoard) (protocol.NotesSaveResponse, error) {
	resolved, normalized, err := s.resolve(noteID, true)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}

	if normalized == "" {
		normalized = slugify(title)
		if normalized == "" {
			normalized = "note"
		}
		normalized += ".md"
		resolved = filepath.Join(s.root, filepath.FromSlash(normalized))
	}

	existing, existingErr := s.loadDocument(normalized, resolved)
	if existingErr == nil {
		if expectedRevision != "" && expectedRevision != existing.fetch.Revision {
			return protocol.NotesSaveResponse{}, &ConflictError{Current: existing.fetch}
		}
	} else if !errors.Is(existingErr, os.ErrNotExist) {
		return protocol.NotesSaveResponse{}, existingErr
	}

	resolvedPageType := strings.TrimSpace(strings.ToLower(pageType))
	if resolvedPageType == "" {
		if existingErr == nil && existing.fetch.PageType != "" {
			resolvedPageType = existing.fetch.PageType
		} else {
			resolvedPageType = protocol.NotePageTypeText
		}
	}
	resolvedPageType = normalizePageType(resolvedPageType)

	resolvedBoard := board
	if resolvedPageType == protocol.NotePageTypeKanban && resolvedBoard == nil && existingErr == nil {
		resolvedBoard = existing.fetch.Board
	}
	if resolvedPageType == protocol.NotePageTypeKanban && content == "" && resolvedBoard != nil {
		content = kanbanMarkdown(titleFromPath(normalized), resolvedBoard)
	}
	if resolvedPageType == protocol.NotePageTypeNotebook {
		content = ""
		resolvedBoard = nil
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return protocol.NotesSaveResponse{}, err
	}

	if err := s.writeSidecar(resolved, resolvedPageType, resolvedBoard); err != nil {
		return protocol.NotesSaveResponse{}, err
	}

	document, err := s.loadDocument(normalized, resolved)
	if err != nil {
		return protocol.NotesSaveResponse{}, err
	}
	return protocol.NotesSaveResponse{
		NoteID:    normalized,
		Revision:  document.fetch.Revision,
		UpdatedAt: document.fetch.UpdatedAt,
		PageType:  document.fetch.PageType,
	}, nil
}

func (s *Service) Rename(_ context.Context, noteID string, title string) error {
	resolved, normalized, err := s.resolve(noteID, false)
	if err != nil {
		return err
	}

	target := slugify(title)
	if target == "" {
		return fmt.Errorf("title is required")
	}
	ext := filepath.Ext(normalized)
	if ext == "" {
		ext = ".md"
	}

	directory := filepath.Dir(normalized)
	if directory == "." {
		directory = ""
	}
	targetID := strings.TrimPrefix(filepath.ToSlash(filepath.Join(directory, target+ext)), "./")
	targetPath := filepath.Join(s.root, filepath.FromSlash(targetID))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	if err := os.Rename(resolved, targetPath); err != nil {
		return err
	}

	sourceSidecar := sidecarPath(resolved)
	targetSidecar := sidecarPath(targetPath)
	if err := os.Rename(sourceSidecar, targetSidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Service) Delete(_ context.Context, noteID string) error {
	resolved, _, err := s.resolve(noteID, false)
	if err != nil {
		return err
	}
	if err := os.Remove(resolved); err != nil {
		return err
	}
	if err := os.Remove(sidecarPath(resolved)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Service) Search(ctx context.Context, query string, limit int) ([]protocol.NoteSearchResult, error) {
	documents, err := s.loadAll(ctx)
	if err != nil {
		return nil, err
	}

	needle := strings.TrimSpace(query)
	if needle == "" {
		return []protocol.NoteSearchResult{}, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	loweredNeedle := strings.ToLower(needle)
	results := make([]protocol.NoteSearchResult, 0)
	for _, document := range documents {
		title := document.summary.Title
		content := document.fetch.Content
		titleMatch := strings.Index(strings.ToLower(title), loweredNeedle)
		bodyMatch := -1
		if document.summary.PageType != protocol.NotePageTypeNotebook {
			bodyMatch = strings.Index(strings.ToLower(content), loweredNeedle)
		}
		if titleMatch < 0 && bodyMatch < 0 {
			continue
		}

		preview := title
		matchLocation := 0
		lineIndex := 0
		if bodyMatch >= 0 {
			preview = snippetAround(content, bodyMatch, len(needle))
			matchLocation = bodyMatch
			lineIndex = strings.Count(content[:bodyMatch], "\n")
		}

		results = append(results, protocol.NoteSearchResult{
			NoteID:        document.summary.ID,
			Title:         title,
			PageType:      document.summary.PageType,
			Preview:       preview,
			MatchLocation: matchLocation,
			LineIndex:     lineIndex,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if strings.EqualFold(results[i].Title, results[j].Title) {
			return strings.ToLower(results[i].Preview) < strings.ToLower(results[j].Preview)
		}
		return strings.ToLower(results[i].Title) < strings.ToLower(results[j].Title)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Service) Tags(ctx context.Context) ([]protocol.NoteTagCount, error) {
	documents, err := s.loadAll(ctx)
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, document := range documents {
		if document.summary.PageType != protocol.NotePageTypeText {
			continue
		}
		for _, tag := range document.summary.Tags {
			counts[tag]++
		}
	}

	tags := make([]protocol.NoteTagCount, 0, len(counts))
	for tag, count := range counts {
		tags = append(tags, protocol.NoteTagCount{Tag: tag, Count: count})
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Count == tags[j].Count {
			return tags[i].Tag < tags[j].Tag
		}
		return tags[i].Count > tags[j].Count
	})
	return tags, nil
}

func (s *Service) TagRollup(ctx context.Context, tag string) ([]protocol.TaggedLineRollupItem, error) {
	documents, err := s.loadAll(ctx)
	if err != nil {
		return nil, err
	}

	normalizedTag := normalizeTag(tag)
	if normalizedTag == "" {
		return []protocol.TaggedLineRollupItem{}, nil
	}

	items := make([]protocol.TaggedLineRollupItem, 0)
	for _, document := range documents {
		if document.summary.PageType != protocol.NotePageTypeText {
			continue
		}

		for index, line := range extractTaggedLines(document.fetch.Content) {
			if !strings.EqualFold(line.tag, normalizedTag) {
				continue
			}
			items = append(items, protocol.TaggedLineRollupItem{
				NoteID:    document.summary.ID,
				NoteTitle: document.summary.Title,
				PageType:  document.summary.PageType,
				Tag:       line.tag,
				LineText:  line.text,
				LineIndex: index,
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if strings.EqualFold(items[i].NoteTitle, items[j].NoteTitle) {
			return items[i].LineIndex < items[j].LineIndex
		}
		return strings.ToLower(items[i].NoteTitle) < strings.ToLower(items[j].NoteTitle)
	})
	return items, nil
}

func (s *Service) Sync(ctx context.Context) ([]protocol.NoteSummary, error) {
	return s.List(ctx)
}

func (s *Service) resolve(noteID string, allowEmpty bool) (string, string, error) {
	if !s.Enabled() {
		return "", "", ErrDisabled
	}

	trimmed := strings.TrimSpace(noteID)
	if trimmed == "" && allowEmpty {
		return "", "", nil
	}
	if trimmed == "" {
		return "", "", fmt.Errorf("note_id is required")
	}

	cleaned := filepath.ToSlash(filepath.Clean("/" + trimmed))
	cleaned = strings.TrimPrefix(cleaned, "/")
	joined := filepath.Join(s.root, filepath.FromSlash(cleaned))
	resolved := filepath.Clean(joined)
	root := filepath.Clean(s.root)
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", "", fmt.Errorf("note path escapes root")
	}
	return resolved, cleaned, nil
}

func (s *Service) loadAll(ctx context.Context) ([]noteDocument, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	documents := make([]noteDocument, 0)
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() || isSidecar(path) {
			return nil
		}

		relative, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		noteID := filepath.ToSlash(relative)
		document, err := s.loadDocument(noteID, path)
		if err != nil {
			return err
		}
		documents = append(documents, document)
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []noteDocument{}, nil
		}
		return nil, err
	}

	sort.Slice(documents, func(i, j int) bool {
		return strings.ToLower(documents[i].summary.Title) < strings.ToLower(documents[j].summary.Title)
	})
	return documents, nil
}

func (s *Service) loadDocument(noteID string, resolved string) (noteDocument, error) {
	data, err := os.ReadFile(resolved)
	if err != nil {
		return noteDocument{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return noteDocument{}, err
	}
	updatedAt := info.ModTime().UTC()
	size := info.Size()

	pageType := protocol.NotePageTypeText
	var board *protocol.KanbanBoard
	sidecarInfo, sidecarErr := os.Stat(sidecarPath(resolved))
	if sidecarErr == nil {
		pageType, board, err = readSidecar(sidecarPath(resolved))
		if err != nil {
			return noteDocument{}, err
		}
		if sidecarInfo.ModTime().After(updatedAt) {
			updatedAt = sidecarInfo.ModTime().UTC()
		}
	} else if !errors.Is(sidecarErr, os.ErrNotExist) {
		return noteDocument{}, sidecarErr
	}

	tags := []string(nil)
	if pageType == protocol.NotePageTypeText {
		tags = extractTags(string(data))
	}
	documentRevision, err := revisionForDocument(string(data), pageType, board)
	if err != nil {
		return noteDocument{}, err
	}

	fetch := protocol.NotesFetchResponse{
		NoteID:    noteID,
		Title:     titleFromPath(noteID),
		Content:   string(data),
		Revision:  documentRevision,
		UpdatedAt: updatedAt,
		PageType:  pageType,
		Preview:   previewFromContent(string(data)),
		Tags:      tags,
		Board:     board,
	}
	return noteDocument{
		summary: protocol.NoteSummary{
			ID:         noteID,
			Title:      fetch.Title,
			UpdatedAt:  fetch.UpdatedAt,
			Revision:   fetch.Revision,
			Size:       size,
			StorageKey: noteID,
			PageType:   fetch.PageType,
			Preview:    fetch.Preview,
			Tags:       fetch.Tags,
		},
		fetch: fetch,
	}, nil
}

func (s *Service) writeSidecar(resolved string, pageType string, board *protocol.KanbanBoard) error {
	sidecar := sidecarPath(resolved)
	switch pageType {
	case protocol.NotePageTypeKanban:
		encoded, err := json.MarshalIndent(boardOrEmpty(board), "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(sidecar, encoded, 0o644)
	case protocol.NotePageTypeNotebook:
		encoded, err := json.MarshalIndent(noteSidecar{PageType: protocol.NotePageTypeNotebook}, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(sidecar, encoded, 0o644)
	default:
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
}

func sidecarPath(notePath string) string {
	base := strings.TrimSuffix(notePath, filepath.Ext(notePath))
	return base + sidecarSuffix
}

func isSidecar(path string) bool {
	return strings.HasSuffix(path, sidecarSuffix)
}

type noteSidecar struct {
	PageType string                `json:"page_type"`
	Board    *protocol.KanbanBoard `json:"board,omitempty"`
}

func readSidecar(path string) (string, *protocol.KanbanBoard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	if len(data) == 0 {
		board := boardOrEmpty(nil)
		return protocol.NotePageTypeKanban, &board, nil
	}

	var meta noteSidecar
	if err := json.Unmarshal(data, &meta); err == nil && strings.TrimSpace(meta.PageType) != "" {
		pageType := normalizePageType(meta.PageType)
		if pageType == protocol.NotePageTypeNotebook {
			return pageType, nil, nil
		}
		board := boardOrEmpty(meta.Board)
		return protocol.NotePageTypeKanban, &board, nil
	}

	var board protocol.KanbanBoard
	if err := json.Unmarshal(data, &board); err != nil {
		return "", nil, err
	}
	normalized := boardOrEmpty(&board)
	return protocol.NotePageTypeKanban, &normalized, nil
}

func boardOrEmpty(board *protocol.KanbanBoard) protocol.KanbanBoard {
	if board == nil {
		return protocol.KanbanBoard{Columns: []protocol.KanbanColumn{}}
	}

	normalized := *board
	if normalized.Columns == nil {
		normalized.Columns = []protocol.KanbanColumn{}
	}
	for i := range normalized.Columns {
		if normalized.Columns[i].Cards == nil {
			normalized.Columns[i].Cards = []protocol.KanbanCard{}
		}
	}
	return normalized
}

func revisionForDocument(content string, pageType string, board *protocol.KanbanBoard) (string, error) {
	payload := struct {
		Content  string               `json:"content"`
		PageType string               `json:"page_type"`
		Board    protocol.KanbanBoard `json:"board"`
	}{
		Content:  content,
		PageType: normalizePageType(pageType),
		Board:    boardOrEmpty(board),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return revision(encoded), nil
}

func previewFromContent(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return truncate(trimmed, 120)
		}
	}
	return ""
}

func snippetAround(content string, offset int, queryLength int) string {
	runes := []rune(content)
	runeOffset := len([]rune(content[:offset]))
	start := max(0, runeOffset-38)
	end := min(len(runes), start+max(queryLength+24, 88))
	snippet := strings.TrimSpace(string(runes[start:end]))
	if snippet == "" {
		return previewFromContent(content)
	}
	return snippet
}

func extractTags(content string) []string {
	tags := make([]string, 0)
	seen := map[string]struct{}{}
	for _, line := range extractTaggedLines(content) {
		if _, ok := seen[line.tag]; ok {
			continue
		}
		seen[line.tag] = struct{}{}
		tags = append(tags, line.tag)
	}
	sort.Strings(tags)
	return tags
}

func extractTaggedLines(content string) []taggedLine {
	lines := strings.Split(content, "\n")
	result := make([]taggedLine, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		contentLine := tagLinePattern.ReplaceAllString(trimmed, "")
		if !strings.HasPrefix(contentLine, "#") {
			continue
		}
		colonIndex := strings.Index(contentLine, ":")
		if colonIndex <= 1 {
			continue
		}

		tag := normalizeTag(contentLine[1:colonIndex])
		if tag == "" {
			continue
		}
		text := strings.TrimSpace(contentLine[colonIndex+1:])
		result = append(result, taggedLine{tag: tag, text: text})
	}
	return result
}

func normalizeTag(rawTag string) string {
	trimmed := strings.ToLower(strings.TrimSpace(rawTag))
	if trimmed == "" {
		return ""
	}

	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	var b strings.Builder
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func kanbanMarkdown(title string, board *protocol.KanbanBoard) string {
	normalized := boardOrEmpty(board)
	columns := slices.Clone(normalized.Columns)
	sort.Slice(columns, func(i, j int) bool {
		return columns[i].SortOrder < columns[j].SortOrder
	})

	sections := make([]string, 0, len(columns))
	for _, column := range columns {
		columnTitle := strings.TrimSpace(column.Title)
		if columnTitle == "" {
			columnTitle = "Column"
		}

		cards := slices.Clone(column.Cards)
		sort.Slice(cards, func(i, j int) bool {
			return cards[i].SortOrder < cards[j].SortOrder
		})

		lines := make([]string, 0, len(cards)+1)
		lines = append(lines, "## "+columnTitle)
		for _, card := range cards {
			lines = append(lines, "- "+card.Text)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled"
	}
	body := strings.Join(sections, "\n\n")
	if body == "" {
		return "# " + title + "\n"
	}
	return "# " + title + "\n\n" + body + "\n"
}

func normalizePageType(pageType string) string {
	switch strings.TrimSpace(strings.ToLower(pageType)) {
	case "", protocol.NotePageTypeText:
		return protocol.NotePageTypeText
	case protocol.NotePageTypeKanban:
		return protocol.NotePageTypeKanban
	case protocol.NotePageTypeNotebook:
		return protocol.NotePageTypeNotebook
	default:
		return protocol.NotePageTypeText
	}
}

func titleFromPath(noteID string) string {
	base := filepath.Base(noteID)
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	base = strings.ReplaceAll(base, "-", " ")
	if base == "" {
		return "Untitled"
	}
	return strings.Title(base)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	slug := strings.Trim(b.String(), "-")
	return slug
}

func revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
