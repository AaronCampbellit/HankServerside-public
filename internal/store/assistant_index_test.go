package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestAssistantIndexSearchRanksDocumentsAndFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_index", Email: "assistant-index@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_index", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}

	embeddingJSON, _ := json.Marshal([]float64{1, 0, 0, 0})
	userID := user.ID
	document := domain.AssistantDocument{
		ID:           "adoc_store_list",
		HomeID:       home.ID,
		UserID:       &userID,
		SourceType:   "profile_note",
		SourceID:     "note_internal_store",
		SourceKey:    "profile_note:" + user.ID + ":note_internal_store",
		Title:        "Store List",
		Path:         "store-list",
		CanonicalURI: "hank://notes/store-list",
		MetadataJSON: `{}`,
		SearchText:   "Store List dog food milk",
		UpdatedAt:    now,
	}
	if err := db.UpsertAssistantDocumentWithChunks(ctx, document, []domain.AssistantChunk{
		{
			ID:               "achunk_store_list_0",
			DocumentID:       document.ID,
			ChunkIndex:       0,
			Content:          "Dog food is on the store list.",
			TokenCount:       7,
			EmbeddingJSON:    string(embeddingJSON),
			EmbeddingModel:   "test",
			EmbeddingVersion: "v1",
			UpdatedAt:        now,
		},
	}); err != nil {
		t.Fatalf("UpsertAssistantDocumentWithChunks: %v", err)
	}

	if err := db.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
		ID:               "afile_tax_2025",
		HomeID:           home.ID,
		Path:             "Documents/Taxes/2025",
		Name:             "2025",
		IsDirectory:      true,
		SearchText:       "Documents Taxes 2025",
		MetadataJSON:     `{}`,
		EmbeddingJSON:    string(embeddingJSON),
		EmbeddingModel:   "test",
		EmbeddingVersion: "v1",
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("UpsertAssistantFileIndex: %v", err)
	}

	results, err := db.SearchAssistantContext(ctx, home.ID, user.ID, "dog food store", []float64{1, 0, 0, 0}, "test", 5)
	if err != nil {
		t.Fatalf("SearchAssistantContext: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected note search results")
	}
	if results[0].SourceType != "profile_note" || results[0].Title != "Store List" {
		t.Fatalf("top result = %#v, want Store List profile note", results[0])
	}

	results, err = db.SearchAssistantContext(ctx, home.ID, user.ID, "tax documents 2025", nil, "", 5)
	if err != nil {
		t.Fatalf("SearchAssistantContext files: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected file search results")
	}
	if results[0].SourceType != "file" || results[0].Path != "Documents/Taxes/2025" {
		t.Fatalf("top file result = %#v, want tax folder", results[0])
	}
}

func TestAssistantIndexStatsReportsSourcesAndEmbeddings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	user, home := createAssistantIndexUserHome(t, ctx, db, "stats")
	now := time.Now().UTC()
	userID := user.ID
	document := domain.AssistantDocument{
		ID:           "adoc_stats_note",
		HomeID:       home.ID,
		UserID:       &userID,
		SourceType:   "profile_note",
		SourceID:     "note_stats",
		SourceKey:    "profile_note:" + user.ID + ":note_stats",
		Title:        "Stats Note",
		Path:         "stats-note",
		CanonicalURI: "hank://notes/stats-note",
		MetadataJSON: `{}`,
		SearchText:   "stats note embedded chunk",
		UpdatedAt:    now,
	}
	if err := db.UpsertAssistantDocumentWithChunks(ctx, document, []domain.AssistantChunk{{
		ID:               "achunk_stats_note_0",
		DocumentID:       document.ID,
		ChunkIndex:       0,
		Content:          "stats note embedded chunk",
		TokenCount:       4,
		EmbeddingJSON:    testEmbeddingJSON(t, 4, 0),
		EmbeddingModel:   "test",
		EmbeddingVersion: "v1",
		UpdatedAt:        now,
	}}); err != nil {
		t.Fatalf("UpsertAssistantDocumentWithChunks: %v", err)
	}
	if err := db.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
		ID:               "afile_stats",
		HomeID:           home.ID,
		Path:             "Stats/file.txt",
		Name:             "file.txt",
		SearchText:       "stats file",
		MetadataJSON:     `{}`,
		EmbeddingJSON:    testEmbeddingJSON(t, 4, 1),
		EmbeddingModel:   "test",
		EmbeddingVersion: "v1",
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("UpsertAssistantFileIndex: %v", err)
	}

	stats, err := db.AssistantIndexStats(ctx, home.ID, user.ID)
	if err != nil {
		t.Fatalf("AssistantIndexStats: %v", err)
	}
	if stats.VectorAvailable != db.VectorAvailable() {
		t.Fatalf("VectorAvailable = %v, want %v", stats.VectorAvailable, db.VectorAvailable())
	}
	if stats.ChunkCount != 1 || stats.EmbeddedChunkCount != 1 || stats.FileCount != 1 || stats.EmbeddedFileCount != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if len(stats.DocumentsBySource) != 1 || stats.DocumentsBySource[0].SourceType != "profile_note" || stats.DocumentsBySource[0].DocumentCount != 1 {
		t.Fatalf("source stats = %#v", stats.DocumentsBySource)
	}
}

func TestAssistantPgvectorSearchRanksVectorMatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	if !db.VectorAvailable() {
		t.Skip("pgvector is not available")
	}

	user, home := createAssistantIndexUserHome(t, ctx, db, "pgvector")
	now := time.Now().UTC()
	userID := user.ID
	documents := []struct {
		id       string
		title    string
		sourceID string
		hotIndex int
		model    string
		content  string
	}{
		{id: "adoc_pgvector_bills", title: "Old Bills", sourceID: "old_bills", hotIndex: 11, model: "test", content: "unrelated words only"},
		{id: "adoc_pgvector_fuses", title: "Fuse Cabinet", sourceID: "fuse_cabinet", hotIndex: 4, model: "test", content: "unrelated words only"},
		{id: "adoc_pgvector_legacy", title: "Legacy Result", sourceID: "legacy_result", hotIndex: 4, model: "local-hash", content: "fallback marker"},
	}
	for _, item := range documents {
		document := domain.AssistantDocument{
			ID:           item.id,
			HomeID:       home.ID,
			UserID:       &userID,
			SourceType:   "assistant_conversation",
			SourceID:     item.sourceID,
			SourceKey:    "assistant_conversation:" + user.ID + ":" + item.sourceID,
			Title:        item.title,
			Path:         item.sourceID,
			CanonicalURI: "hank://assistant/sessions/" + item.sourceID,
			MetadataJSON: `{}`,
			SearchText:   item.content,
			UpdatedAt:    now,
		}
		if err := db.UpsertAssistantDocumentWithChunks(ctx, document, []domain.AssistantChunk{{
			ID:               "achunk_" + item.sourceID,
			DocumentID:       document.ID,
			ChunkIndex:       0,
			Content:          item.content,
			TokenCount:       3,
			EmbeddingJSON:    testEmbeddingJSON(t, 768, item.hotIndex),
			EmbeddingModel:   item.model,
			EmbeddingVersion: "v1",
			UpdatedAt:        now,
		}}); err != nil {
			t.Fatalf("UpsertAssistantDocumentWithChunks %s: %v", item.sourceID, err)
		}
	}
	if err := db.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
		ID:               "afile_pgvector_legacy",
		HomeID:           home.ID,
		Path:             "fallback-marker.txt",
		Name:             "fallback-marker.txt",
		SearchText:       "fallback marker",
		MetadataJSON:     `{}`,
		EmbeddingJSON:    testEmbeddingJSON(t, 768, 4),
		EmbeddingModel:   "local-hash",
		EmbeddingVersion: "v1",
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("UpsertAssistantFileIndex: %v", err)
	}

	queryEmbedding := make([]float64, 768)
	queryEmbedding[4] = 1
	results, err := db.SearchAssistantContext(ctx, home.ID, user.ID, "no lexical hit", queryEmbedding, "test", 5)
	if err != nil {
		t.Fatalf("SearchAssistantContext: %v", err)
	}
	if len(results) == 0 || results[0].SourceID != "fuse_cabinet" {
		t.Fatalf("top vector result = %#v, want fuse_cabinet", results)
	}
	for _, result := range results {
		if result.SourceID == "legacy_result" || result.Path == "fallback-marker.txt" {
			t.Fatalf("different-model vector matched query: %#v", result)
		}
	}
	results, err = db.SearchAssistantContext(ctx, home.ID, user.ID, "fallback marker", queryEmbedding, "test", 5)
	if err != nil {
		t.Fatalf("SearchAssistantContext lexical: %v", err)
	}
	var foundLegacyDoc, foundLegacyFile bool
	for _, result := range results {
		foundLegacyDoc = foundLegacyDoc || result.SourceID == "legacy_result"
		foundLegacyFile = foundLegacyFile || result.Path == "fallback-marker.txt"
	}
	if !foundLegacyDoc || !foundLegacyFile {
		t.Fatalf("different-model rows lost lexical matches: %#v", results)
	}
}

func TestAssistantIndexSearchRequiresPgvectorForEmbeddingQueries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()
	db.vectorAvailable = false

	user, home := createAssistantIndexUserHome(t, ctx, db, "jsonfallback")
	now := time.Now().UTC()
	userID := user.ID
	document := domain.AssistantDocument{
		ID:           "adoc_json_fallback",
		HomeID:       home.ID,
		UserID:       &userID,
		SourceType:   "assistant_conversation",
		SourceID:     "json_fallback",
		SourceKey:    "assistant_conversation:" + user.ID + ":json_fallback",
		Title:        "Fallback Memory",
		Path:         "json_fallback",
		CanonicalURI: "hank://assistant/sessions/json_fallback",
		MetadataJSON: `{}`,
		SearchText:   "words without a lexical match",
		UpdatedAt:    now,
	}
	if err := db.UpsertAssistantDocumentWithChunks(ctx, document, []domain.AssistantChunk{{
		ID:               "achunk_json_fallback",
		DocumentID:       document.ID,
		ChunkIndex:       0,
		Content:          "words without a lexical match",
		TokenCount:       5,
		EmbeddingJSON:    testEmbeddingJSON(t, 4, 2),
		EmbeddingModel:   "test",
		EmbeddingVersion: "v1",
		UpdatedAt:        now,
	}}); err != nil {
		t.Fatalf("UpsertAssistantDocumentWithChunks: %v", err)
	}

	queryEmbedding := make([]float64, 768)
	queryEmbedding[2] = 1
	_, err := db.SearchAssistantContext(ctx, home.ID, user.ID, "absent phrase", queryEmbedding, "test", 5)
	if err == nil || !strings.Contains(err.Error(), "required pgvector search is unavailable") {
		t.Fatalf("SearchAssistantContext error = %v, want required pgvector unavailable", err)
	}
}

func TestDeleteAssistantSessionRemovesConversationDocument(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	user, home := createAssistantIndexUserHome(t, ctx, db, "delete_conversation")
	now := time.Now().UTC()
	session := domain.AssistantSession{
		ID:            "asess_delete_conversation",
		HomeID:        home.ID,
		UserID:        user.ID,
		Title:         "Fuse memory",
		LastMessageAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := db.CreateAssistantSession(ctx, session); err != nil {
		t.Fatalf("CreateAssistantSession: %v", err)
	}
	userID := user.ID
	document := domain.AssistantDocument{
		ID:           "adoc_delete_conversation",
		HomeID:       home.ID,
		UserID:       &userID,
		SourceType:   "assistant_conversation",
		SourceID:     session.ID,
		SourceKey:    "assistant_conversation:" + home.ID + ":" + user.ID + ":" + session.ID,
		Title:        "Fuse memory",
		Path:         session.ID,
		CanonicalURI: "hank://assistant/sessions/" + session.ID,
		MetadataJSON: `{}`,
		SearchText:   "blue cabinet spare fuses",
		UpdatedAt:    now,
	}
	if err := db.UpsertAssistantDocumentWithChunks(ctx, document, []domain.AssistantChunk{{
		ID:               "achunk_delete_conversation",
		DocumentID:       document.ID,
		ChunkIndex:       0,
		Content:          "blue cabinet spare fuses",
		TokenCount:       4,
		EmbeddingJSON:    testEmbeddingJSON(t, 4, 0),
		EmbeddingModel:   "test",
		EmbeddingVersion: "v1",
		UpdatedAt:        now,
	}}); err != nil {
		t.Fatalf("UpsertAssistantDocumentWithChunks: %v", err)
	}
	if err := db.DeleteAssistantSession(ctx, session.ID); err != nil {
		t.Fatalf("DeleteAssistantSession: %v", err)
	}
	stats, err := db.AssistantIndexStats(ctx, home.ID, user.ID)
	if err != nil {
		t.Fatalf("AssistantIndexStats: %v", err)
	}
	if stats.ConversationCount != 0 {
		t.Fatalf("conversation count = %d, want 0", stats.ConversationCount)
	}
}

func TestAssistantIndexJobsDedupeClaimAndComplete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close()

	user, home := createAssistantIndexUserHome(t, ctx, db, "jobs")
	first, err := db.EnqueueAssistantIndexJob(ctx, domain.AssistantIndexJob{
		HomeID:     home.ID,
		UserID:     user.ID,
		SourceType: "profile_note",
		SourceID:   "note-jobs",
	})
	if err != nil {
		t.Fatalf("EnqueueAssistantIndexJob first: %v", err)
	}
	second, err := db.EnqueueAssistantIndexJob(ctx, domain.AssistantIndexJob{
		HomeID:     home.ID,
		UserID:     user.ID,
		SourceType: "profile_note",
		SourceID:   "note-jobs",
	})
	if err != nil {
		t.Fatalf("EnqueueAssistantIndexJob second: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("deduped job IDs differ: %q != %q", first.ID, second.ID)
	}

	queued, running, failed, err := db.AssistantIndexJobCounts(ctx, home.ID, user.ID)
	if err != nil {
		t.Fatalf("AssistantIndexJobCounts: %v", err)
	}
	if queued != 1 || running != 0 || failed != 0 {
		t.Fatalf("job counts = queued %d running %d failed %d, want 1/0/0", queued, running, failed)
	}

	claimed, err := db.ClaimAssistantIndexJob(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClaimAssistantIndexJob: %v", err)
	}
	if claimed.ID != first.ID || claimed.Status != AssistantIndexJobStatusRunning || claimed.Attempts != 1 || claimed.StartedAt == nil {
		t.Fatalf("claimed job = %#v", claimed)
	}
	if err := db.CompleteAssistantIndexJob(ctx, claimed.ID, time.Now().UTC()); err != nil {
		t.Fatalf("CompleteAssistantIndexJob: %v", err)
	}
	queued, running, failed, err = db.AssistantIndexJobCounts(ctx, home.ID, user.ID)
	if err != nil {
		t.Fatalf("AssistantIndexJobCounts after complete: %v", err)
	}
	if queued != 0 || running != 0 || failed != 0 {
		t.Fatalf("job counts after complete = queued %d running %d failed %d, want 0/0/0", queued, running, failed)
	}
}

func createAssistantIndexUserHome(t *testing.T, ctx context.Context, db *Store, suffix string) (domain.User, domain.Home) {
	t.Helper()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_index_" + suffix, Email: "assistant-index-" + suffix + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_index_" + suffix, UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatalf("CreateHome: %v", err)
	}
	return user, home
}

func testEmbeddingJSON(t *testing.T, dimension int, hotIndex int) string {
	t.Helper()

	values := make([]float64, dimension)
	if hotIndex >= 0 && hotIndex < len(values) {
		values[hotIndex] = 1
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("Marshal embedding: %v", err)
	}
	return string(encoded)
}
