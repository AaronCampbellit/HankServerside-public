package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) UpsertAssistantDocumentWithChunks(ctx context.Context, document domain.AssistantDocument, chunks []domain.AssistantChunk) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if document.MetadataJSON == "" {
		document.MetadataJSON = "{}"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_documents (
			id, home_id, user_id, source_type, source_id, source_key, title, path,
			canonical_uri, metadata_json, search_text, embedding_model, embedding_version, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			home_id = excluded.home_id,
			user_id = excluded.user_id,
			source_type = excluded.source_type,
			source_id = excluded.source_id,
			source_key = excluded.source_key,
			title = excluded.title,
			path = excluded.path,
			canonical_uri = excluded.canonical_uri,
			metadata_json = excluded.metadata_json,
			search_text = excluded.search_text,
			embedding_model = excluded.embedding_model,
			embedding_version = excluded.embedding_version,
			updated_at = excluded.updated_at`,
		document.ID,
		document.HomeID,
		document.UserID,
		document.SourceType,
		document.SourceID,
		document.SourceKey,
		document.Title,
		document.Path,
		document.CanonicalURI,
		document.MetadataJSON,
		document.SearchText,
		document.EmbeddingModel,
		document.EmbeddingVersion,
		document.UpdatedAt,
	); err != nil {
		return err
	}

	row := tx.QueryRowContext(ctx, `SELECT id FROM assistant_documents WHERE source_key = ?`, document.SourceKey)
	var documentID string
	if err := row.Scan(&documentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_chunks WHERE document_id = ?`, documentID); err != nil {
		return err
	}
	for _, chunk := range chunks {
		chunk.DocumentID = documentID
		if err := s.insertAssistantChunk(ctx, tx, chunk); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) DeleteAssistantDocumentForSource(ctx context.Context, homeID string, userID string, sourceType string, sourceID string) error {
	_, err := s.exec(ctx, `DELETE FROM assistant_documents
		WHERE home_id = ?
			AND source_type = ?
			AND source_id = ?
			AND (user_id IS NULL OR user_id = ?)`, homeID, sourceType, sourceID, userID)
	return err
}

func (s *Store) PruneAssistantProjectDocuments(ctx context.Context, homeID string, currentSourceIDs []string) error {
	if len(currentSourceIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(currentSourceIDs))
	args := make([]any, 0, len(currentSourceIDs)+1)
	args = append(args, homeID)
	for index, sourceID := range currentSourceIDs {
		placeholders[index] = "?"
		args = append(args, sourceID)
	}
	_, err := s.exec(ctx, `DELETE FROM assistant_documents
		WHERE home_id = ? AND user_id IS NULL AND source_type = 'project_doc'
		AND source_id NOT IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

func (s *Store) UpsertAssistantFileIndex(ctx context.Context, item domain.AssistantFileIndex) error {
	if item.MetadataJSON == "" {
		item.MetadataJSON = "{}"
	}
	if s.vectorAvailable && vectorLiteralFromJSON(item.EmbeddingJSON) != "" {
		_, err := s.exec(ctx, `INSERT INTO assistant_file_index (
				id, home_id, service_profile_id, path, name, is_directory, size_bytes, modified_at,
				search_text, metadata_json, embedding_json, embedding_model, embedding_version, updated_at, embedding
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::vector)
			ON CONFLICT(home_id, service_profile_id, path) DO UPDATE SET
				service_profile_id = excluded.service_profile_id,
				name = excluded.name,
				is_directory = excluded.is_directory,
				size_bytes = excluded.size_bytes,
				modified_at = excluded.modified_at,
				search_text = excluded.search_text,
				metadata_json = excluded.metadata_json,
				embedding_json = excluded.embedding_json,
				embedding_model = excluded.embedding_model,
				embedding_version = excluded.embedding_version,
				updated_at = excluded.updated_at,
				embedding = excluded.embedding`,
			item.ID, item.HomeID, item.ServiceProfileID, item.Path, item.Name, item.IsDirectory, item.SizeBytes, item.ModifiedAt,
			item.SearchText, item.MetadataJSON, item.EmbeddingJSON, item.EmbeddingModel, item.EmbeddingVersion, item.UpdatedAt,
			vectorLiteralFromJSON(item.EmbeddingJSON),
		)
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO assistant_file_index (
			id, home_id, service_profile_id, path, name, is_directory, size_bytes, modified_at,
			search_text, metadata_json, embedding_json, embedding_model, embedding_version, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id, service_profile_id, path) DO UPDATE SET
			service_profile_id = excluded.service_profile_id,
			name = excluded.name,
			is_directory = excluded.is_directory,
			size_bytes = excluded.size_bytes,
			modified_at = excluded.modified_at,
			search_text = excluded.search_text,
			metadata_json = excluded.metadata_json,
			embedding_json = excluded.embedding_json,
			embedding_model = excluded.embedding_model,
			embedding_version = excluded.embedding_version,
			updated_at = excluded.updated_at`,
		item.ID, item.HomeID, item.ServiceProfileID, item.Path, item.Name, item.IsDirectory, item.SizeBytes, item.ModifiedAt,
		item.SearchText, item.MetadataJSON, item.EmbeddingJSON, item.EmbeddingModel, item.EmbeddingVersion, item.UpdatedAt,
	)
	return err
}

func (s *Store) SearchAssistantFileDirectories(ctx context.Context, homeID string, query string, limit int) ([]domain.AssistantFileIndex, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.query(ctx, `SELECT id, home_id, service_profile_id, path, name, is_directory, size_bytes,
			modified_at, search_text, metadata_json, embedding_json, embedding_model, embedding_version, updated_at
		FROM assistant_file_index
		WHERE home_id = ? AND is_directory = TRUE
		ORDER BY updated_at DESC
		LIMIT 500`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	loweredQuery := strings.ToLower(query)
	terms := strings.Fields(loweredQuery)
	type scoredDirectory struct {
		item  domain.AssistantFileIndex
		score float64
	}
	var scored []scoredDirectory
	for rows.Next() {
		item, err := scanAssistantFileIndex(rows)
		if err != nil {
			return nil, err
		}
		searchText := strings.ToLower(item.Name + " " + item.Path + " " + item.SearchText)
		score := textScore(loweredQuery, terms, searchText)
		if strings.EqualFold(item.Name, query) || strings.EqualFold(strings.Trim(item.Path, "/"), strings.Trim(query, "/")) {
			score += 8
		}
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredDirectory{item: item, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].item.UpdatedAt.After(scored[j].item.UpdatedAt)
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}

	results := make([]domain.AssistantFileIndex, 0, len(scored))
	for _, item := range scored {
		results = append(results, item.item)
	}
	return results, nil
}

func scanAssistantFileIndex(scanner interface{ Scan(dest ...any) error }) (domain.AssistantFileIndex, error) {
	var item domain.AssistantFileIndex
	var modifiedAt sql.NullTime
	if err := scanner.Scan(
		&item.ID,
		&item.HomeID,
		&item.ServiceProfileID,
		&item.Path,
		&item.Name,
		&item.IsDirectory,
		&item.SizeBytes,
		&modifiedAt,
		&item.SearchText,
		&item.MetadataJSON,
		&item.EmbeddingJSON,
		&item.EmbeddingModel,
		&item.EmbeddingVersion,
		&item.UpdatedAt,
	); err != nil {
		return domain.AssistantFileIndex{}, err
	}
	if modifiedAt.Valid {
		item.ModifiedAt = &modifiedAt.Time
	}
	return item, nil
}

func (s *Store) SearchAssistantContext(ctx context.Context, homeID string, userID string, query string, queryEmbedding []float64, queryEmbeddingModel string, limit int) ([]domain.AssistantRetrievedContext, error) {
	if limit <= 0 {
		limit = 8
	}
	query = strings.TrimSpace(query)
	loweredQuery := strings.ToLower(query)
	terms := strings.Fields(loweredQuery)
	queryEmbeddingModel = strings.TrimSpace(queryEmbeddingModel)
	results := make([]domain.AssistantRetrievedContext, 0)
	resultIndex := map[string]int{}

	vectorLiteral := vectorLiteralFromValues(queryEmbedding)
	if vectorLiteral != "" && queryEmbeddingModel != "" {
		if !s.vectorAvailable {
			return nil, errors.New("required pgvector search is unavailable")
		}
		vectorResults, err := s.searchAssistantVectorContext(ctx, homeID, userID, vectorLiteral, queryEmbeddingModel, loweredQuery, terms, limit)
		if err != nil {
			return nil, err
		}
		for _, item := range vectorResults {
			mergeAssistantContextResult(&results, resultIndex, item)
		}
	}

	rows, err := s.query(ctx, `SELECT d.source_type, d.source_id, d.title, d.path, d.canonical_uri,
			d.search_text, d.updated_at, c.content, c.embedding_json, c.embedding_model
		FROM assistant_documents d
		LEFT JOIN assistant_chunks c ON c.document_id = d.id
		WHERE d.home_id = ? AND (d.user_id IS NULL OR d.user_id = ?)
		ORDER BY d.updated_at DESC, c.chunk_index ASC
		LIMIT 500`, homeID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		item, err := scanAssistantContextRow(rows, loweredQuery, terms, queryEmbedding, queryEmbeddingModel)
		if err != nil {
			return nil, err
		}
		mergeAssistantContextResult(&results, resultIndex, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	fileRows, err := s.query(ctx, `SELECT service_profile_id, path, name, search_text, updated_at, embedding_json, embedding_model
		FROM assistant_file_index
		WHERE home_id = ?
		ORDER BY updated_at DESC
		LIMIT 300`, homeID)
	if err != nil {
		return nil, err
	}
	defer fileRows.Close()
	for fileRows.Next() {
		var sourceID, pathValue, name, searchText, embeddingJSON, embeddingModel string
		var updatedAt sql.NullTime
		if err := fileRows.Scan(&sourceID, &pathValue, &name, &searchText, &updatedAt, &embeddingJSON, &embeddingModel); err != nil {
			return nil, err
		}
		score := textScore(loweredQuery, terms, strings.ToLower(name+" "+pathValue+" "+searchText))
		if queryEmbeddingModel != "" && embeddingModel == queryEmbeddingModel {
			score += embeddingScore(queryEmbedding, embeddingJSON)
		}
		if score <= 0 {
			continue
		}
		mergeAssistantContextResult(&results, resultIndex, domain.AssistantRetrievedContext{
			SourceType:   "file",
			SourceID:     strings.TrimSpace(sourceID),
			Title:        name,
			Path:         pathValue,
			CanonicalURI: "hank://files/" + strings.TrimPrefix(pathValue, "/"),
			Snippet:      pathValue,
			Score:        score,
			UpdatedAt:    updatedAt.Time,
		})
	}
	if err := fileRows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].UpdatedAt.After(results[j].UpdatedAt)
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Store) AssistantIndexStats(ctx context.Context, homeID string, userID string) (domain.AssistantIndexStats, error) {
	stats := domain.AssistantIndexStats{
		VectorAvailable: s.VectorAvailable(),
		VectorMode:      "pgvector",
	}
	if !stats.VectorAvailable {
		stats.VectorMode = "unavailable"
	}

	rows, err := s.query(ctx, `SELECT d.source_type,
			COUNT(DISTINCT d.id) AS document_count,
			COUNT(c.id) AS chunk_count,
			COUNT(c.id) FILTER (WHERE c.embedding_json <> '') AS embedded_chunk_count
		FROM assistant_documents d
		LEFT JOIN assistant_chunks c ON c.document_id = d.id
		WHERE d.home_id = ? AND (d.user_id IS NULL OR d.user_id = ?)
		GROUP BY d.source_type
		ORDER BY d.source_type`, homeID, userID)
	if err != nil {
		return domain.AssistantIndexStats{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var source domain.AssistantIndexSourceCount
		if err := rows.Scan(&source.SourceType, &source.DocumentCount, &source.ChunkCount, &source.EmbeddedChunkCount); err != nil {
			return domain.AssistantIndexStats{}, err
		}
		stats.DocumentsBySource = append(stats.DocumentsBySource, source)
		stats.ChunkCount += source.ChunkCount
		stats.EmbeddedChunkCount += source.EmbeddedChunkCount
		if source.SourceType == "assistant_conversation" {
			stats.ConversationCount = source.DocumentCount
		}
	}
	if err := rows.Err(); err != nil {
		return domain.AssistantIndexStats{}, err
	}

	row := s.queryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE embedding_json <> '')
		FROM assistant_file_index
		WHERE home_id = ?`, homeID)
	if err := row.Scan(&stats.FileCount, &stats.EmbeddedFileCount); err != nil {
		return domain.AssistantIndexStats{}, err
	}

	queued, running, failed, err := s.AssistantIndexJobCounts(ctx, homeID, userID)
	if err != nil {
		return domain.AssistantIndexStats{}, err
	}
	stats.QueuedJobCount = queued
	stats.RunningJobCount = running
	stats.FailedJobCount = failed

	return stats, nil
}

func (s *Store) searchAssistantVectorContext(ctx context.Context, homeID string, userID string, vectorLiteral string, queryEmbeddingModel string, loweredQuery string, terms []string, limit int) ([]domain.AssistantRetrievedContext, error) {
	vectorLimit := limit * 4
	if vectorLimit < 20 {
		vectorLimit = 20
	}
	if vectorLimit > 120 {
		vectorLimit = 120
	}

	rows, err := s.query(ctx, `SELECT d.source_type, d.source_id, d.title, d.path, d.canonical_uri,
			d.search_text, d.updated_at, c.content, c.embedding_json, 1 - (c.embedding <=> ?::vector) AS vector_score
		FROM assistant_documents d
		JOIN assistant_chunks c ON c.document_id = d.id
		WHERE d.home_id = ? AND (d.user_id IS NULL OR d.user_id = ?) AND c.embedding IS NOT NULL AND c.embedding_model = ?
		ORDER BY c.embedding <=> ?::vector
		LIMIT ?`, vectorLiteral, homeID, userID, queryEmbeddingModel, vectorLiteral, vectorLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]domain.AssistantRetrievedContext, 0)
	for rows.Next() {
		item, err := scanAssistantVectorContextRow(rows, loweredQuery, terms)
		if err != nil {
			return nil, err
		}
		if item.Score > 0 {
			results = append(results, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	fileRows, err := s.query(ctx, `SELECT service_profile_id, path, name, search_text, updated_at, 1 - (embedding <=> ?::vector) AS vector_score
		FROM assistant_file_index
		WHERE home_id = ? AND embedding IS NOT NULL AND embedding_model = ?
		ORDER BY embedding <=> ?::vector
		LIMIT ?`, vectorLiteral, homeID, queryEmbeddingModel, vectorLiteral, vectorLimit)
	if err != nil {
		return nil, err
	}
	defer fileRows.Close()
	for fileRows.Next() {
		var sourceID, pathValue, name, searchText string
		var updatedAt sql.NullTime
		var vectorScore sql.NullFloat64
		if err := fileRows.Scan(&sourceID, &pathValue, &name, &searchText, &updatedAt, &vectorScore); err != nil {
			return nil, err
		}
		score := textScore(loweredQuery, terms, strings.ToLower(name+" "+pathValue+" "+searchText))
		if vectorScore.Valid && vectorScore.Float64 > 0 {
			score += vectorScore.Float64 * 6
		}
		if score <= 0 {
			continue
		}
		results = append(results, domain.AssistantRetrievedContext{
			SourceType:   "file",
			SourceID:     strings.TrimSpace(sourceID),
			Title:        name,
			Path:         pathValue,
			CanonicalURI: "hank://files/" + strings.TrimPrefix(pathValue, "/"),
			Snippet:      pathValue,
			Score:        score,
			UpdatedAt:    updatedAt.Time,
		})
	}
	if err := fileRows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Store) insertAssistantChunk(ctx context.Context, tx *dbTx, chunk domain.AssistantChunk) error {
	if s.vectorAvailable && vectorLiteralFromJSON(chunk.EmbeddingJSON) != "" {
		_, err := tx.ExecContext(ctx, `INSERT INTO assistant_chunks (
				id, document_id, chunk_index, content, token_count, embedding_json,
				embedding_model, embedding_version, updated_at, embedding
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?::vector)`,
			chunk.ID, chunk.DocumentID, chunk.ChunkIndex, chunk.Content, chunk.TokenCount, chunk.EmbeddingJSON,
			chunk.EmbeddingModel, chunk.EmbeddingVersion, chunk.UpdatedAt, vectorLiteralFromJSON(chunk.EmbeddingJSON),
		)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO assistant_chunks (
			id, document_id, chunk_index, content, token_count, embedding_json,
			embedding_model, embedding_version, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chunk.ID, chunk.DocumentID, chunk.ChunkIndex, chunk.Content, chunk.TokenCount, chunk.EmbeddingJSON,
		chunk.EmbeddingModel, chunk.EmbeddingVersion, chunk.UpdatedAt,
	)
	return err
}

func scanAssistantContextRow(scanner interface{ Scan(dest ...any) error }, loweredQuery string, terms []string, queryEmbedding []float64, queryEmbeddingModel string) (domain.AssistantRetrievedContext, error) {
	var sourceType, sourceID, title, pathValue, canonicalURI, searchText string
	var updatedAt sql.NullTime
	var content, embeddingJSON, embeddingModel sql.NullString
	if err := scanner.Scan(&sourceType, &sourceID, &title, &pathValue, &canonicalURI, &searchText, &updatedAt, &content, &embeddingJSON, &embeddingModel); err != nil {
		return domain.AssistantRetrievedContext{}, err
	}
	snippet := content.String
	if strings.TrimSpace(snippet) == "" {
		snippet = searchText
	}
	score := textScore(loweredQuery, terms, strings.ToLower(title+" "+pathValue+" "+searchText+" "+snippet))
	if queryEmbeddingModel != "" && embeddingModel.String == queryEmbeddingModel {
		score += embeddingScore(queryEmbedding, embeddingJSON.String)
	}
	return domain.AssistantRetrievedContext{
		SourceType:   sourceType,
		SourceID:     sourceID,
		Title:        title,
		Path:         pathValue,
		CanonicalURI: canonicalURI,
		Snippet:      trimSnippet(snippet, 220),
		Score:        score,
		UpdatedAt:    updatedAt.Time,
	}, nil
}

func scanAssistantVectorContextRow(scanner interface{ Scan(dest ...any) error }, loweredQuery string, terms []string) (domain.AssistantRetrievedContext, error) {
	var sourceType, sourceID, title, pathValue, canonicalURI, searchText string
	var updatedAt sql.NullTime
	var content, embeddingJSON sql.NullString
	var vectorScore sql.NullFloat64
	if err := scanner.Scan(&sourceType, &sourceID, &title, &pathValue, &canonicalURI, &searchText, &updatedAt, &content, &embeddingJSON, &vectorScore); err != nil {
		return domain.AssistantRetrievedContext{}, err
	}
	snippet := content.String
	if strings.TrimSpace(snippet) == "" {
		snippet = searchText
	}
	score := textScore(loweredQuery, terms, strings.ToLower(title+" "+pathValue+" "+searchText+" "+snippet))
	if vectorScore.Valid && vectorScore.Float64 > 0 {
		score += vectorScore.Float64 * 6
	}
	return domain.AssistantRetrievedContext{
		SourceType:   sourceType,
		SourceID:     sourceID,
		Title:        title,
		Path:         pathValue,
		CanonicalURI: canonicalURI,
		Snippet:      trimSnippet(snippet, 220),
		Score:        score,
		UpdatedAt:    updatedAt.Time,
	}, nil
}

func mergeAssistantContextResult(results *[]domain.AssistantRetrievedContext, resultIndex map[string]int, item domain.AssistantRetrievedContext) {
	if item.Score <= 0 {
		return
	}
	key := strings.Join([]string{item.SourceType, item.SourceID, item.Path}, "\x00")
	if index, ok := resultIndex[key]; ok {
		merged := (*results)[index]
		merged.Score += item.Score
		if strings.TrimSpace(merged.Snippet) == "" || len(item.Snippet) > len(merged.Snippet) {
			merged.Snippet = item.Snippet
		}
		if item.UpdatedAt.After(merged.UpdatedAt) {
			merged.UpdatedAt = item.UpdatedAt
		}
		(*results)[index] = merged
		return
	}
	resultIndex[key] = len(*results)
	*results = append(*results, item)
}

func textScore(query string, terms []string, haystack string) float64 {
	if query == "" {
		return 0
	}
	score := 0.0
	if strings.Contains(haystack, query) {
		score += 10
	}
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			score += 2
		}
	}
	return score
}

func embeddingScore(queryEmbedding []float64, raw string) float64 {
	if len(queryEmbedding) == 0 || raw == "" {
		return 0
	}
	var embedding []float64
	if err := json.Unmarshal([]byte(raw), &embedding); err != nil || len(embedding) == 0 {
		return 0
	}
	score := cosineSimilarity(queryEmbedding, embedding)
	if score <= 0 {
		return 0
	}
	return score * 6
}

func cosineSimilarity(a []float64, b []float64) float64 {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	if limit == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := 0; i < limit; i++ {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func vectorLiteralFromJSON(raw string) string {
	if raw == "" {
		return ""
	}
	var values []float64
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return ""
	}
	return vectorLiteralFromValues(values)
}

func vectorLiteralFromValues(values []float64) string {
	if len(values) != 768 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatFloat(value, 'g', -1, 64))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func trimSnippet(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + "..."
}
