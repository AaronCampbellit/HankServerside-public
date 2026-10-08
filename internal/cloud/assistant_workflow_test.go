package cloud

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestDuplicateArticleNoteAppendPromptResolvesToAppendTool(t *testing.T) {
	prompt := "add fix hankai conversation output the the hank features note"
	itemText, noteHint := extractAppendIntent(prompt)
	if itemText != "fix hankai conversation output" || noteHint != "hank features note" {
		t.Fatalf("append intent = item:%q note:%q", itemText, noteHint)
	}
	tool, intent := resolveAssistantTool(prompt)
	if tool.Kind != assistantIntentNotesAppend || intent.Kind != assistantIntentNotesAppend {
		t.Fatalf("resolved tool = %q intent = %q, want notes append", tool.Kind, intent.Kind)
	}
	if intent.Query != prompt {
		t.Fatalf("append intent query = %q, want original prompt %q", intent.Query, prompt)
	}
}

func TestAssistantIntentClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prompt string
		want   assistantIntentKind
	}{
		{name: "note list", prompt: "what notes do I have", want: assistantIntentNotesList},
		{name: "note search", prompt: "find grocery list note", want: assistantIntentNotesSearch},
		{name: "note append", prompt: "add eggs to the grocery list", want: assistantIntentNotesAppend},
		{name: "note append work note", prompt: "add call patrick to the work note", want: assistantIntentNotesAppend},
		{name: "note append typo delimiter", prompt: "add fix hankai conversation output the the hank features note", want: assistantIntentNotesAppend},
		{name: "note append referenced link", prompt: "https://docs.gitlab.com/install/docker/installation/\nAdd this link to work note", want: assistantIntentNotesAppend},
		{name: "home assistant on", prompt: "what entities are on", want: assistantIntentHomeAssistantQuery},
		{name: "home assistant garage", prompt: "garage entities", want: assistantIntentHomeAssistantQuery},
		{name: "home assistant conversational query", prompt: "can you find all the garage light entities", want: assistantIntentHomeAssistantQuery},
		{name: "home assistant control", prompt: "turn on the garage overhead light", want: assistantIntentHomeAssistantControl},
		{name: "media quoted movie search", prompt: `find "normal" movie`, want: assistantIntentMediaSearch},
		{name: "media quoted download search", prompt: `search for "normal" movie for download`, want: assistantIntentMediaSearch},
		{name: "file search", prompt: "find 2025 taxes", want: assistantIntentFilesSearch},
		{name: "note content search", prompt: "find information in my notes about SMB", want: assistantIntentNotesSearch},
		{name: "tax folder search", prompt: "find the 2025 tax folder", want: assistantIntentFilesSearch},
		{name: "attachment SMB phrase", prompt: "add this PDF to the taxes SMB share", want: assistantIntentFilesSearch},
		{name: "store list append", prompt: "add buy coffee to the store list", want: assistantIntentNotesAppend},
		{name: "calendar tomorrow", prompt: "what do I have tomorrow", want: assistantIntentCalendarSearch},
		{name: "calendar create timed", prompt: "create calendar event for dentist Friday at 2", want: assistantIntentCalendarCreate},
		{name: "calendar update", prompt: "move my dentist appointment to 3", want: assistantIntentCalendarUpdate},
		{name: "calendar delete", prompt: "delete the dentist appointment tomorrow", want: assistantIntentCalendarDelete},
		{name: "project docs", prompt: "what does AGENTS.md say", want: assistantIntentProjectDocs},
		{name: "project docs from source path request", prompt: "Using Hank context, what is Hank supposed to do? Keep it concise and cite any project-doc source path if available.", want: assistantIntentProjectDocs},
		{name: "project docs product intent", prompt: "what is the product intent? cite the source path if you can", want: assistantIntentProjectDocs},
		{name: "read only multi source", prompt: "what do I have tomorrow and do my notes mention dentist", want: assistantIntentReadOnlySynthesis},
		{name: "assistant memory decision", prompt: "what did we decide about calendar defaults", want: assistantIntentMemorySearch},
		{name: "Hermes slash command", prompt: "/Hermes summarize the current plan", want: assistantIntentHermesChat},
		{name: "Gramaton slash command", prompt: "/gramaton dutton ranch", want: assistantIntentGramatonCommand},
		{name: "YDownload slash command", prompt: "/ydownload https://www.youtube.com/watch?v=UYbZo6UuMMY", want: assistantIntentYDownloadCommand},
		{name: "Home Assistant slash command", prompt: "/ha garage lights", want: assistantIntentHACommand},
		{name: "files slash command", prompt: "/files 2025 taxes", want: assistantIntentFilesCommand},
		{name: "notes slash command", prompt: "/notes grocery list", want: assistantIntentNotesCommand},
		{name: "append slash command", prompt: "/append buy eggs to grocery list", want: assistantIntentAppendCommand},
		{name: "calendar slash command", prompt: "/calendar tomorrow", want: assistantIntentCalendarCommand},
		{name: "docs slash command", prompt: "/docs deployment", want: assistantIntentDocsCommand},
		{name: "status slash command", prompt: "/status", want: assistantIntentStatusCommand},
		{name: "assistant source status", prompt: "show assistant source and index status", want: assistantIntentAssistantStatus},
		{name: "local model status", prompt: "what local model is HankAI using", want: assistantIntentAssistantStatus},
		{name: "agent online status", prompt: "is the primary Hank Agent online", want: assistantIntentAgentStatus},
		{name: "notes sync status", prompt: "show notes sync status", want: assistantIntentSyncStatus},
		{name: "backup status", prompt: "show backup status", want: assistantIntentBackupStatus},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := classifyAssistantIntent(test.prompt)
			if got.Kind != test.want {
				t.Fatalf("classifyAssistantIntent(%q) = %s, want %s", test.prompt, got.Kind, test.want)
			}
		})
	}
}

func TestAssistantSlotExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		prompt      string
		wantAction  string
		wantObject  string
		wantPayload string
		wantDest    string
	}{
		{prompt: "add buy batteries to the store list", wantAction: "append", wantObject: "note", wantPayload: "buy batteries", wantDest: "store list"},
		{prompt: "what do I have tomorrow", wantAction: "list", wantObject: "calendar"},
		{prompt: "what did we decide about SMB shares", wantAction: "find", wantObject: "chat_history"},
		{prompt: "create a folder called Warranty in Documents", wantAction: "create", wantObject: "file"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.prompt, func(t *testing.T) {
			t.Parallel()

			got := extractAssistantSlots(test.prompt)
			if got.Action != test.wantAction || got.Object != test.wantObject {
				t.Fatalf("slots = %#v, want action=%q object=%q", got, test.wantAction, test.wantObject)
			}
			if test.wantPayload != "" && got.Payload != test.wantPayload {
				t.Fatalf("payload = %q, want %q; slots=%#v", got.Payload, test.wantPayload, got)
			}
			if test.wantDest != "" && got.Destination != test.wantDest {
				t.Fatalf("destination = %q, want %q; slots=%#v", got.Destination, test.wantDest, got)
			}
		})
	}
}

func TestAssistantParsingHelpers(t *testing.T) {
	t.Parallel()

	title, body := parseNoteCreatePrompt("create a note titled HankAI validation saying the 5.5 live workflow test passed")
	if title != "HankAI validation" || body != "the 5.5 live workflow test passed" {
		t.Fatalf("parseNoteCreatePrompt title=%q body=%q", title, body)
	}

	path, sourceID := parseCreateFolderTarget("create folder _hank_validation/live-run/hank-chat-flow on primary")
	if path != "_hank_validation/live-run/hank-chat-flow" || sourceID != "primary" {
		t.Fatalf("parseCreateFolderTarget path=%q source_id=%q", path, sourceID)
	}

	query, sourceID := stripFileSourceSuffix("_hank_validation on the secondary file server")
	if query != "_hank_validation" || sourceID != "secondary" {
		t.Fatalf("stripFileSourceSuffix query=%q source_id=%q", query, sourceID)
	}

	noteQuery := noteSearchQuery("find information in my notes about SMB")
	if noteQuery != "SMB" {
		t.Fatalf("noteSearchQuery = %q, want SMB", noteQuery)
	}

	index, ok := assistantSelectionIndex("use the second one", 3)
	if !ok || index != 1 {
		t.Fatalf("assistantSelectionIndex = %d %t, want 1 true", index, ok)
	}

	control, ok := parseHomeAssistantControlPrompt("Please turn the porch light off")
	if !ok || control.Service != "turn_off" || control.Target != "porch light" {
		t.Fatalf("parseHomeAssistantControlPrompt = %#v %t, want porch light turn_off", control, ok)
	}
	control, ok = parseHomeAssistantControlPrompt("/ha unlock the front door")
	if !ok || control.Service != "unlock" || control.Target != "front door" || !control.HighImpact {
		t.Fatalf("parse slash Home Assistant control = %#v %t, want high-impact front door unlock", control, ok)
	}
	control, ok = parseHomeAssistantControlPrompt("Could you turn the kitchen light on please?")
	if !ok || control.Service != "turn_on" || control.Target != "kitchen light" {
		t.Fatalf("parse conversational Home Assistant control = %#v %t, want kitchen light turn_on", control, ok)
	}
	if !isHomeAssistantMutationPrompt("/ha please set the thermostat to 70") {
		t.Fatal("unsupported conversational Home Assistant mutation was classified as a read")
	}
}

func TestFileMatchScoreRequiresTextMatch(t *testing.T) {
	t.Parallel()

	item := protocol.FileItem{
		Path:        "docs/recipes",
		Name:        "recipes",
		IsDirectory: true,
	}

	if score := fileMatchScore(item, "needle-not-present"); score != 0 {
		t.Fatalf("fileMatchScore unrelated directory score = %d, want 0", score)
	}
	if score := fileMatchScore(item, "recipes"); score <= 0 {
		t.Fatalf("fileMatchScore matching directory score = %d, want positive", score)
	}
}

func TestAssistantToolRegistryShape(t *testing.T) {
	t.Parallel()

	if len(assistantToolRegistry) == 0 {
		t.Fatal("assistant tool registry is empty")
	}
	seen := map[assistantIntentKind]bool{}
	for index, tool := range assistantToolRegistry {
		if tool.Kind == "" {
			t.Fatalf("tool %d has empty kind", index)
		}
		if seen[tool.Kind] {
			t.Fatalf("tool kind %s registered more than once", tool.Kind)
		}
		seen[tool.Kind] = true
		if tool.Match == nil {
			t.Fatalf("tool %s is missing matcher", tool.Kind)
		}
		if tool.Execute == nil {
			t.Fatalf("tool %s is missing executor", tool.Kind)
		}
	}
	last := assistantToolRegistry[len(assistantToolRegistry)-1]
	if last.Kind != assistantIntentGeneral {
		t.Fatalf("last registry tool = %s, want %s fallback", last.Kind, assistantIntentGeneral)
	}
}

func TestAssistantNoteRankingPrefersExactTitleThenTitleThenBody(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	results := rankScoredNotes([]domain.UserNote{
		{
			ID:        "note_body",
			NoteID:    "body",
			Title:     "Personal Reminders",
			Content:   "Call Patrick about work tomorrow.",
			PageType:  protocol.NotePageTypeText,
			UpdatedAt: now.Add(2 * time.Hour),
		},
		{
			ID:        "note_title",
			NoteID:    "title",
			Title:     "Work Projects",
			Content:   "Deployment notes.",
			PageType:  protocol.NotePageTypeText,
			UpdatedAt: now.Add(time.Hour),
		},
		{
			ID:        "note_exact",
			NoteID:    "exact",
			Title:     "Work",
			Content:   "Deployment notes.",
			PageType:  protocol.NotePageTypeText,
			UpdatedAt: now,
		},
	}, "work")

	got := []string{results[0].Note.Title, results[1].Note.Title, results[2].Note.Title}
	want := []string{"Work", "Work Projects", "Personal Reminders"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranked notes = %v, want %v", got, want)
		}
	}
}

func TestAssistantAppendTextInheritsNoteStyle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		item    string
		want    string
	}{
		{name: "empty defaults to bullet", content: "", item: "buy batteries", want: "- buy batteries"},
		{name: "bullet", content: "- milk", item: "eggs", want: "- milk\n- eggs"},
		{name: "checklist", content: "- [x] call Patrick", item: "send notes", want: "- [x] call Patrick\n- [ ] send notes"},
		{name: "numbered", content: "1. first\n2. second", item: "third", want: "1. first\n2. second\n3. third"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := appendAssistantNoteText(test.content, test.item); got != test.want {
				t.Fatalf("appendAssistantNoteText() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAssistantNotesSearchAppendAndReindex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_notes", Email: "assistant-notes@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	otherUser := domain.User{ID: "usr_assistant_notes_other", Email: "assistant-notes-other@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_notes", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	note := domain.UserNote{
		ID:           "note_assistant_grocery",
		NoteID:       "11111111-1111-1111-1111-111111111111",
		OwnerUserID:  user.ID,
		Title:        "Grocery List",
		Content:      "- milk",
		BodyMarkdown: "- milk",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now,
		UpdatedBy:    user.ID,
	}
	featuresNote := domain.UserNote{
		ID:           "note_assistant_features",
		NoteID:       "22222222-2222-2222-2222-222222222222",
		OwnerUserID:  user.ID,
		Title:        "Hank Features",
		Content:      "- existing feature",
		BodyMarkdown: "- existing feature",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now.Add(-time.Minute),
		UpdatedBy:    user.ID,
	}
	workNote := domain.UserNote{
		ID:           "note_assistant_work",
		NoteID:       "44444444-4444-4444-8444-444444444444",
		OwnerUserID:  user.ID,
		Title:        "Work",
		Content:      "- deployment notes",
		BodyMarkdown: "- deployment notes",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now.Add(-3 * time.Minute),
		UpdatedBy:    user.ID,
	}
	workProjectsNote := domain.UserNote{
		ID:           "note_assistant_work_projects",
		NoteID:       "55555555-5555-5555-8555-555555555555",
		OwnerUserID:  user.ID,
		Title:        "Work Projects",
		Content:      "- planning",
		BodyMarkdown: "- planning",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now.Add(5 * time.Minute),
		UpdatedBy:    user.ID,
	}
	workBodyNote := domain.UserNote{
		ID:           "note_assistant_work_body",
		NoteID:       "66666666-6666-6666-8666-666666666666",
		OwnerUserID:  user.ID,
		Title:        "Personal Reminders",
		Content:      "- work follow up",
		BodyMarkdown: "- work follow up",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now.Add(6 * time.Minute),
		UpdatedBy:    user.ID,
	}
	sharedNote := domain.UserNote{
		ID:           "note_assistant_shared",
		NoteID:       "33333333-3333-3333-3333-333333333333",
		OwnerUserID:  otherUser.ID,
		HomeID:       home.ID,
		Title:        "Shared Checklist",
		Content:      "- shared item",
		BodyMarkdown: "- shared item",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now.Add(-2 * time.Minute),
		UpdatedBy:    otherUser.ID,
	}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateUser(ctx, otherUser))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertUserNote(ctx, note))
	must(t, db.UpsertUserNote(ctx, featuresNote))
	must(t, db.UpsertUserNote(ctx, workNote))
	must(t, db.UpsertUserNote(ctx, workProjectsNote))
	must(t, db.UpsertUserNote(ctx, workBodyNote))
	must(t, db.UpsertUserNote(ctx, sharedNote))
	must(t, db.AddNoteShare(ctx, domain.NoteShare{
		NoteID:       sharedNote.ID,
		HomeID:       home.ID,
		TargetUserID: user.ID,
		SharedBy:     otherUser.ID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.ProjectDocsEnabled = false
	settings.FilesEnabled = false
	settings.HomeAssistantEnabled = false
	settings.CalendarEnabled = false
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}

	listed, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "what notes do I have")
	if err != nil {
		t.Fatalf("generateAssistantResponse list: %v", err)
	}
	for _, want := range []string{"Grocery List", "Hank Features", "Work", "Work Projects", "Personal Reminders", "Shared Checklist"} {
		if !strings.Contains(listed.Text, want) {
			t.Fatalf("note list missing %q: %s", want, listed.Text)
		}
	}
	if len(listed.Cards) != 6 {
		t.Fatalf("note list cards = %#v, want one card per listed note", listed.Cards)
	}
	for _, want := range []string{"Grocery List", "Hank Features", "Work", "Work Projects", "Personal Reminders", "Shared Checklist"} {
		if !assistantCardsContainTitle(listed.Cards, want) {
			t.Fatalf("note list cards missing %q: %#v", want, listed.Cards)
		}
	}

	found, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "find grocery list note")
	if err != nil {
		t.Fatalf("generateAssistantResponse search: %v", err)
	}
	if len(found.Cards) != 1 || found.Cards[0].Kind != "note" || found.Cards[0].Title != "Grocery List" {
		t.Fatalf("note search cards = %#v", found.Cards)
	}

	appended, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "add eggs to the grocery list")
	if err != nil {
		t.Fatalf("generateAssistantResponse append: %v", err)
	}
	if !strings.Contains(appended.Text, "Added `eggs`") {
		t.Fatalf("append text = %q", appended.Text)
	}
	updated, err := db.GetUserNoteByID(ctx, note.ID)
	if err != nil {
		t.Fatalf("GetUserNoteByID: %v", err)
	}
	if !strings.Contains(updated.Content, "- eggs") || !strings.Contains(updated.BodyMarkdown, "- eggs") {
		t.Fatalf("updated note did not include appended item: %#v", updated)
	}
	if !server.processNextAssistantIndexJob(ctx) {
		t.Fatal("expected queued assistant index job after append")
	}
	results, err := db.SearchAssistantContext(ctx, home.ID, user.ID, "eggs grocery", nil, "", 5)
	if err != nil {
		t.Fatalf("SearchAssistantContext: %v", err)
	}
	if len(results) == 0 || results[0].SourceType != "profile_note" || results[0].Title != "Grocery List" {
		t.Fatalf("reindexed note search results = %#v", results)
	}

	appendedTypo, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "add fix hankai conversation output the the hank features note")
	if err != nil {
		t.Fatalf("generateAssistantResponse append typo: %v", err)
	}
	if !strings.Contains(appendedTypo.Text, "Added `fix hankai conversation output`") {
		t.Fatalf("append typo text = %q", appendedTypo.Text)
	}
	updatedFeatures, err := db.GetUserNoteByID(ctx, featuresNote.ID)
	if err != nil {
		t.Fatalf("GetUserNoteByID features: %v", err)
	}
	if !strings.Contains(updatedFeatures.Content, "- fix hankai conversation output") {
		t.Fatalf("features note did not include appended item: %#v", updatedFeatures)
	}

	linkPrompt := "https://docs.gitlab.com/install/docker/installation/\nAdd this link to work note"
	appendedLink, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, linkPrompt)
	if err != nil {
		t.Fatalf("generateAssistantResponse append link: %v", err)
	}
	if !strings.Contains(appendedLink.Text, "Added `https://docs.gitlab.com/install/docker/installation/`") {
		t.Fatalf("append link text = %q", appendedLink.Text)
	}
	updatedWork, err := db.GetUserNoteByID(ctx, workNote.ID)
	if err != nil {
		t.Fatalf("GetUserNoteByID work: %v", err)
	}
	if !strings.Contains(updatedWork.Content, "- https://docs.gitlab.com/install/docker/installation/") ||
		strings.Contains(updatedWork.Content, "- this link") {
		t.Fatalf("work note did not include appended URL: %#v", updatedWork)
	}

	appendedCall, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "add call patrick to the work note")
	if err != nil {
		t.Fatalf("generateAssistantResponse append call: %v", err)
	}
	if !strings.Contains(appendedCall.Text, "Added `call patrick` to `Work`") {
		t.Fatalf("append call text = %q", appendedCall.Text)
	}
	updatedWork, err = db.GetUserNoteByID(ctx, workNote.ID)
	if err != nil {
		t.Fatalf("GetUserNoteByID work after call: %v", err)
	}
	if !strings.Contains(updatedWork.Content, "- call patrick") {
		t.Fatalf("work note did not include appended call item: %#v", updatedWork)
	}
	updatedWorkProjects, err := db.GetUserNoteByID(ctx, workProjectsNote.ID)
	if err != nil {
		t.Fatalf("GetUserNoteByID work projects: %v", err)
	}
	if strings.Contains(updatedWorkProjects.Content, "- call patrick") {
		t.Fatalf("work projects note received exact-title append: %#v", updatedWorkProjects)
	}
	updatedWorkBody, err := db.GetUserNoteByID(ctx, workBodyNote.ID)
	if err != nil {
		t.Fatalf("GetUserNoteByID work body: %v", err)
	}
	if strings.Contains(updatedWorkBody.Content, "- call patrick") {
		t.Fatalf("body-only work note received exact-title append: %#v", updatedWorkBody)
	}
}

func TestConfirmedAssistantNoteAppendReindexesNote(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_confirm_append", Email: "assistant-confirm-append@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_confirm_append", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AssistantSession{
		ID:            "asess_confirm_append",
		HomeID:        home.ID,
		UserID:        user.ID,
		Title:         "Note append confirmation",
		LastMessageAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	note := domain.UserNote{
		ID:           "note_confirm_append",
		NoteID:       "confirm-append-note",
		OwnerUserID:  user.ID,
		Title:        "Projects",
		Content:      "- initial",
		BodyMarkdown: "- initial",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now,
		UpdatedBy:    user.ID,
	}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateAssistantSession(ctx, session))
	must(t, db.UpsertUserNote(ctx, note))
	message := domain.AssistantMessage{
		ID:          "amsg_confirm_append",
		SessionID:   session.ID,
		Role:        assistantRoleAssistant,
		Status:      assistantStateWaitingConfirm,
		ContentJSON: `{"text":"Confirm note append."}`,
		ModelName:   assistantModelName,
		CreatedAt:   now,
	}
	must(t, db.CreateAssistantMessage(ctx, message))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	pending := assistantPendingAction{
		Kind: "note_append",
		NoteAppend: &assistantPendingNoteAppend{
			TargetNoteID: note.ID,
			TargetTitle:  note.Title,
			AppendedText: "ship vector audit",
		},
	}
	run := domain.AssistantRun{
		ID:                   "arun_confirm_append",
		SessionID:            session.ID,
		MessageID:            message.ID,
		State:                assistantStateWaitingConfirm,
		RequiresConfirmation: true,
		CreatedAt:            now,
	}
	must(t, db.CreateAssistantRun(ctx, run))

	if _, err := server.executeConfirmedAssistantAction(ctx, session, run, pending, user.ID); err != nil {
		t.Fatalf("executeConfirmedAssistantAction: %v", err)
	}
	if !server.processNextAssistantIndexJob(ctx) {
		t.Fatal("expected queued assistant index job after confirmed append")
	}
	results, err := db.SearchAssistantContext(ctx, home.ID, user.ID, "ship vector audit", nil, "", 5)
	if err != nil {
		t.Fatalf("SearchAssistantContext: %v", err)
	}
	foundReindexedNote := false
	for _, result := range results {
		if result.SourceType == "profile_note" && result.Title == "Projects" {
			foundReindexedNote = true
			break
		}
	}
	if !foundReindexedNote {
		t.Fatalf("confirmed append was not reindexed: %#v", results)
	}
}

func TestAssistantConfirmationResponseIncludesStructuredSummary(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_confirm", Email: "assistant-confirm@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_confirm", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	sessionToken := "assistant-confirm-token"
	appSession := domain.AppSession{ID: "sess_assistant_confirm", UserID: user.ID, TokenHash: hashToken(sessionToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	session := domain.AssistantSession{
		ID:            "asess_confirm",
		HomeID:        home.ID,
		UserID:        user.ID,
		Title:         "Calendar confirmation",
		LastMessageAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, appSession))
	must(t, db.CreateAssistantSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}

	response, err := server.processAssistantMessage(ctx, home, membership, auth, session, "create dentist appointment on May 3", "device-confirm", "UTC")
	if err != nil {
		t.Fatalf("processAssistantMessage: %v", err)
	}
	if !response.RequiresConfirmation {
		t.Fatalf("RequiresConfirmation = false, response=%#v", response)
	}
	if response.PendingActionSummary == nil {
		t.Fatalf("missing pending action summary: %#v", response)
	}
	if response.PendingActionSummary.Kind != "calendar_create" || response.PendingActionSummary.Title != "Create calendar event" {
		t.Fatalf("unexpected pending action summary: %#v", response.PendingActionSummary)
	}
	requestedDateIncludesMay3 := false
	for _, detail := range response.PendingActionSummary.Details {
		if detail.Label == "Requested date" && strings.Contains(detail.Value, "May 3") {
			requestedDateIncludesMay3 = true
		}
	}
	if !assistantSummaryDetailsContain(response.PendingActionSummary.Details, "Event", "dentist appointment") ||
		!requestedDateIncludesMay3 ||
		!assistantSummaryDetailsContain(response.PendingActionSummary.Details, "All day", "Yes") {
		t.Fatalf("summary details missing expected values: %#v", response.PendingActionSummary.Details)
	}

	run, err := db.GetAssistantRun(ctx, response.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	fetched := server.assistantRunResponseForSession(ctx, session, run)
	if fetched.PendingActionSummary == nil || fetched.PendingActionSummary.Kind != "calendar_create" {
		t.Fatalf("fetched run missing pending action summary: %#v", fetched)
	}

	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()
	var messages assistantMessageListResponse
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/assistant/sessions/"+session.ID+"/messages", nil, &messages)
	if messages.ActiveRun == nil || messages.ActiveRun.ID != response.ID {
		t.Fatalf("message history missing active run: %#v", messages.ActiveRun)
	}
	if !messages.ActiveRun.RequiresConfirmation || messages.ActiveRun.PendingActionSummary == nil || messages.ActiveRun.PendingActionSummary.Kind != "calendar_create" {
		t.Fatalf("active run missing confirmation details: %#v", messages.ActiveRun)
	}
}

func TestAssistantCalendarSearchAndMutationPlanning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now()
	tomorrow := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	user := domain.User{ID: "usr_assistant_calendar", Email: "assistant-calendar@example.com", PasswordHash: "hash", CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	home := domain.Home{ID: "home_assistant_calendar", UserID: user.ID, Name: "Home", CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	session := domain.AssistantSession{ID: "asess_calendar", HomeID: home.ID, UserID: user.ID, Title: "Calendar", LastMessageAt: now.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	dentistNote := domain.UserNote{
		ID:           "note_assistant_calendar_dentist",
		NoteID:       "dentist-note",
		OwnerUserID:  user.ID,
		Title:        "Dentist Notes",
		Content:      "Remember to ask the dentist about the night guard.",
		BodyMarkdown: "Remember to ask the dentist about the night guard.",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
		UpdatedBy:    user.ID,
	}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateAssistantSession(ctx, session))
	must(t, db.UpsertUserNote(ctx, dentistNote))
	must(t, db.UpsertAssistantCalendarEntries(ctx, []domain.AssistantCalendarEntry{
		{
			ID:              "acal_dentist",
			HomeID:          home.ID,
			UserID:          user.ID,
			DeviceID:        "device-calendar",
			ExternalEventID: "event_dentist",
			CalendarID:      "Personal",
			Title:           "Dentist Appointment",
			StartsAt:        tomorrow,
			EndsAt:          tomorrow.Add(time.Hour),
			SearchText:      "Dentist Appointment Personal",
			UpdatedAt:       now.UTC(),
		},
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.ProjectDocsEnabled = false
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}

	agenda, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "what do I have tomorrow")
	if err != nil {
		t.Fatalf("generateAssistantResponse calendar search: %v", err)
	}
	if len(agenda.Cards) != 1 || agenda.Cards[0].Kind != "calendar" || agenda.Cards[0].EventID != "event_dentist" {
		t.Fatalf("calendar agenda cards = %#v", agenda.Cards)
	}
	diagnostics := assistantDiagnosticsFromContent(agenda)
	if diagnostics == nil || diagnostics.ToolKind != string(assistantIntentCalendarSearch) {
		t.Fatalf("calendar diagnostics = %#v", diagnostics)
	}

	multiSource, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "what do I have tomorrow and do my notes mention dentist")
	if err != nil {
		t.Fatalf("generateAssistantResponse multi source: %v", err)
	}
	if !assistantCardsContainKind(multiSource.Cards, "calendar") || !assistantCardsContainKind(multiSource.Cards, "note") {
		t.Fatalf("multi-source cards = %#v, want calendar and note cards", multiSource.Cards)
	}

	updateRun, err := server.processAssistantMessage(ctx, home, membership, auth, session, "move my dentist appointment to 4", "device-calendar", "UTC")
	if err != nil {
		t.Fatalf("process calendar update: %v", err)
	}
	if !updateRun.RequiresConfirmation || updateRun.PendingActionSummary == nil || updateRun.PendingActionSummary.Kind != "calendar_update" {
		t.Fatalf("calendar update did not require confirmation: %#v", updateRun)
	}
	run, err := db.GetAssistantRun(ctx, updateRun.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun update: %v", err)
	}
	var pending assistantPendingAction
	if err := json.Unmarshal([]byte(run.PendingActionJSON), &pending); err != nil {
		t.Fatalf("pending update JSON: %v", err)
	}
	if pending.CalendarClient == nil || pending.CalendarClient.ToolRequest.ToolName != "calendar.update_event" {
		t.Fatalf("pending update action = %#v", pending)
	}
	if startsAt := assistantToolArgumentString(pending.CalendarClient.ToolRequest.Arguments, "starts_at"); !strings.Contains(startsAt, "16:00:00") {
		t.Fatalf("planned starts_at = %q, want 4pm", startsAt)
	}
	toolRun, err := server.executeConfirmedAssistantAction(ctx, session, run, pending, user.ID)
	if err != nil {
		t.Fatalf("execute calendar update confirmation: %v", err)
	}
	if !toolRun.RequiresClientTools || toolRun.ClientToolRequest == nil || toolRun.ClientToolRequest.ToolName != "calendar.update_event" {
		t.Fatalf("calendar update did not request client tool: %#v", toolRun)
	}

	deleteRun, err := server.processAssistantMessage(ctx, home, membership, auth, session, "delete the dentist appointment tomorrow", "device-calendar", "UTC")
	if err != nil {
		t.Fatalf("process calendar delete: %v", err)
	}
	if !deleteRun.RequiresConfirmation || deleteRun.PendingActionSummary == nil || deleteRun.PendingActionSummary.Kind != "calendar_delete" || !deleteRun.PendingActionSummary.Destructive {
		t.Fatalf("calendar delete did not require destructive confirmation: %#v", deleteRun)
	}
}

func TestAssistantCalendarFollowupSelectsPreviousCard(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	user := domain.User{ID: "usr_assistant_calendar_followup", Email: "assistant-calendar-followup@example.com", PasswordHash: "hash", CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	home := domain.Home{ID: "home_assistant_calendar_followup", UserID: user.ID, Name: "Home", CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	session := domain.AssistantSession{ID: "asess_calendar_followup", HomeID: home.ID, UserID: user.ID, Title: "Calendar", LastMessageAt: now.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateAssistantSession(ctx, session))
	must(t, db.UpsertAssistantCalendarEntries(ctx, []domain.AssistantCalendarEntry{
		{
			ID:              "acal_dentist_one",
			HomeID:          home.ID,
			UserID:          user.ID,
			DeviceID:        "device-calendar",
			ExternalEventID: "event_dentist_one",
			CalendarID:      "Personal",
			Title:           "Dentist Appointment One",
			StartsAt:        start,
			EndsAt:          start.Add(time.Hour),
			SearchText:      "Dentist Appointment One Personal",
			UpdatedAt:       now.UTC(),
		},
		{
			ID:              "acal_dentist_two",
			HomeID:          home.ID,
			UserID:          user.ID,
			DeviceID:        "device-calendar",
			ExternalEventID: "event_dentist_two",
			CalendarID:      "Personal",
			Title:           "Dentist Appointment Two",
			StartsAt:        start.Add(time.Hour),
			EndsAt:          start.Add(2 * time.Hour),
			SearchText:      "Dentist Appointment Two Personal",
			UpdatedAt:       now.UTC(),
		},
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}

	ambiguous, err := server.processAssistantMessage(ctx, home, membership, auth, session, "move my dentist appointment to 4", "device-calendar", "UTC")
	if err != nil {
		t.Fatalf("ambiguous update: %v", err)
	}
	if ambiguous.RequiresConfirmation || ambiguous.AssistantMessage == nil || len(ambiguous.AssistantMessage.Cards) != 2 {
		t.Fatalf("ambiguous update response = %#v", ambiguous)
	}

	selected, err := server.processAssistantMessage(ctx, home, membership, auth, session, "second one to 5", "device-calendar", "UTC")
	if err != nil {
		t.Fatalf("followup selection: %v", err)
	}
	if !selected.RequiresConfirmation || selected.PendingActionSummary == nil || selected.PendingActionSummary.Kind != "calendar_update" {
		t.Fatalf("followup did not create update confirmation: %#v", selected)
	}
	if !strings.Contains(selected.PendingActionSummary.Confirmation, "5:00 PM") {
		t.Fatalf("followup confirmation = %q, want new 5pm time", selected.PendingActionSummary.Confirmation)
	}
	run, err := db.GetAssistantRun(ctx, selected.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun selected: %v", err)
	}
	var pending assistantPendingAction
	if err := json.Unmarshal([]byte(run.PendingActionJSON), &pending); err != nil {
		t.Fatalf("pending selected JSON: %v", err)
	}
	if got := assistantToolArgumentString(pending.CalendarClient.ToolRequest.Arguments, "event_id"); got != "event_dentist_two" {
		t.Fatalf("selected event_id = %q, want event_dentist_two", got)
	}
	if startsAt := assistantToolArgumentString(pending.CalendarClient.ToolRequest.Arguments, "starts_at"); !strings.Contains(startsAt, "17:00:00") {
		t.Fatalf("selected starts_at = %q, want 5pm", startsAt)
	}
}

func TestAssistantAttachmentClarificationReusesStagedMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_attachment_followup", Email: "attachment-followup@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_attachment_followup", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AssistantSession{ID: "asess_attachment_followup", HomeID: home.ID, UserID: user.ID, Title: "Uploads", LastMessageAt: now, CreatedAt: now, UpdatedAt: now}
	note := domain.UserNote{
		ID:           "note_attachment_project_ideas",
		NoteID:       "11111111-3333-4333-8333-111111111111",
		OwnerUserID:  user.ID,
		Title:        "Project Ideas",
		Content:      "Ideas",
		BodyMarkdown: "Ideas",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now,
		UpdatedBy:    user.ID,
	}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateAssistantSession(ctx, session))
	must(t, db.UpsertUserNote(ctx, note))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}
	attachments := []assistantMessageAttachment{{
		ClientAttachmentID: "client_attachment_project",
		Filename:           "idea.png",
		ContentType:        "image/png",
		SizeBytes:          12,
		ChecksumSHA256:     "abc123",
		Kind:               "image",
	}}

	first, err := server.processAssistantMessageWithAttachments(ctx, home, membership, auth, session, "uploaded this", attachments, "device", "UTC")
	if err != nil {
		t.Fatalf("process first attachment message: %v", err)
	}
	if first.RequiresConfirmation {
		t.Fatalf("first response unexpectedly requires confirmation: %#v", first)
	}
	if first.AssistantMessage == nil || !strings.Contains(first.AssistantMessage.Text, "Where should I store") {
		t.Fatalf("first response did not ask for destination: %#v", first.AssistantMessage)
	}

	followup, err := server.processAssistantMessageWithAttachments(ctx, home, membership, auth, session, "Project Ideas note", nil, "device", "UTC")
	if err != nil {
		t.Fatalf("process follow-up message: %v", err)
	}
	if !followup.RequiresConfirmation || followup.PendingActionSummary == nil {
		t.Fatalf("follow-up did not reuse staged attachment for confirmation: %#v", followup)
	}
	if followup.PendingActionSummary.Kind != "attachment_commit" {
		t.Fatalf("pending kind = %q, want attachment_commit", followup.PendingActionSummary.Kind)
	}
}

func TestAssistantAttachmentToolErrorCompletesAndExpiresMissingStagedBytes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_attachment_error", Email: "attachment-error@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_attachment_error", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	appSession := domain.AppSession{ID: "sess_attachment_error", UserID: user.ID, TokenHash: hashToken("attachment-error-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	note := domain.UserNote{
		ID:           "note_attachment_error_project",
		NoteID:       "22222222-3333-4333-8333-111111111111",
		OwnerUserID:  user.ID,
		Title:        "Project Ideas",
		Content:      "Ideas",
		BodyMarkdown: "Ideas",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now,
		UpdatedBy:    user.ID,
	}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, appSession))
	must(t, db.UpsertUserNote(ctx, note))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var apiSession assistantAPISession
	requestJSON(t, testServer, "attachment-error-token", http.MethodPost, "/v1/home/assistant/sessions", nil, &apiSession)

	var initial assistantRunResponse
	requestJSON(t, testServer, "attachment-error-token", http.MethodPost, "/v1/home/assistant/sessions/"+apiSession.ID+"/messages", map[string]any{
		"content": "put this in Project Ideas note",
		"attachments": []map[string]any{
			{
				"client_attachment_id": "client_attachment_missing",
				"filename":             "missing.pdf",
				"content_type":         "application/pdf",
				"size_bytes":           24,
				"checksum_sha256":      "abc123",
				"kind":                 "document",
			},
		},
	}, &initial)
	if !initial.RequiresConfirmation {
		t.Fatalf("initial run did not request confirmation: %#v", initial)
	}

	var toolRun assistantRunResponse
	requestJSON(t, testServer, "attachment-error-token", http.MethodPost, "/v1/home/assistant/runs/"+initial.ID+"/confirm", map[string]any{"approved": true}, &toolRun)
	if !toolRun.RequiresClientTools || toolRun.ClientToolRequest == nil || toolRun.ClientToolRequest.ToolName != "attachments.commit" {
		t.Fatalf("confirmed run did not request attachment client tool: %#v", toolRun)
	}

	var completed assistantRunResponse
	requestJSON(t, testServer, "attachment-error-token", http.MethodPost, "/v1/home/assistant/runs/"+toolRun.ID+"/client-tool-results", map[string]any{
		"results": []map[string]any{
			{
				"tool_name": "attachments.commit",
				"error":     "The staged upload is no longer available on this device.",
				"result": map[string]any{
					"destination_kind":       "note_attachment",
					"attachment_ids":         []string{"client_attachment_missing"},
					"expired_attachment_ids": []string{"client_attachment_missing"},
					"error_code":             "missing_staged_attachment",
				},
			},
		},
	}, &completed)
	if completed.State != assistantStateCompleted || completed.AssistantMessage == nil {
		t.Fatalf("tool error did not complete run: %#v", completed)
	}
	if !strings.Contains(completed.AssistantMessage.Text, "could not store") {
		t.Fatalf("unexpected completion text: %#v", completed.AssistantMessage)
	}
	records, err := db.ListAssistantAttachments(ctx, apiSession.ID)
	if err != nil {
		t.Fatalf("ListAssistantAttachments: %v", err)
	}
	if len(records) != 1 || records[0].Status != "expired" {
		t.Fatalf("attachment status = %#v, want one expired record", records)
	}
}

func TestAssistantAttachmentNoteCommitCompletesAndMarksCommitted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_attachment_note_success", Email: "attachment-note-success@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_attachment_note_success", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	appSession := domain.AppSession{ID: "sess_attachment_note_success", UserID: user.ID, TokenHash: hashToken("attachment-note-success-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	note := domain.UserNote{
		ID:           "note_attachment_note_success",
		NoteID:       "33333333-3333-4333-8333-111111111111",
		OwnerUserID:  user.ID,
		Title:        "Project Ideas",
		Content:      "Ideas",
		BodyMarkdown: "Ideas",
		BodyFormat:   "markdown",
		PageType:     protocol.NotePageTypeText,
		Revision:     "rev_initial",
		Checksum:     "checksum_initial",
		CreatedAt:    now,
		UpdatedAt:    now,
		UpdatedBy:    user.ID,
	}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, appSession))
	must(t, db.UpsertUserNote(ctx, note))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var apiSession assistantAPISession
	requestJSON(t, testServer, "attachment-note-success-token", http.MethodPost, "/v1/home/assistant/sessions", nil, &apiSession)

	var initial assistantRunResponse
	requestJSON(t, testServer, "attachment-note-success-token", http.MethodPost, "/v1/home/assistant/sessions/"+apiSession.ID+"/messages", map[string]any{
		"content": "Add this photo to the Project Ideas note",
		"attachments": []map[string]any{
			{
				"client_attachment_id": "client_attachment_note_success",
				"filename":             "idea.png",
				"content_type":         "image/png",
				"size_bytes":           12,
				"checksum_sha256":      "abc123",
				"kind":                 "image",
			},
		},
	}, &initial)
	if !initial.RequiresConfirmation || initial.PendingActionSummary == nil {
		t.Fatalf("initial run did not request attachment confirmation: %#v", initial)
	}

	var toolRun assistantRunResponse
	requestJSON(t, testServer, "attachment-note-success-token", http.MethodPost, "/v1/home/assistant/runs/"+initial.ID+"/confirm", map[string]any{"approved": true}, &toolRun)
	if !toolRun.RequiresClientTools || toolRun.ClientToolRequest == nil || toolRun.ClientToolRequest.ToolName != "attachments.commit" {
		t.Fatalf("confirmed run did not request attachment client tool: %#v", toolRun)
	}

	var completed assistantRunResponse
	requestJSON(t, testServer, "attachment-note-success-token", http.MethodPost, "/v1/home/assistant/runs/"+toolRun.ID+"/client-tool-results", map[string]any{
		"tool_name": "attachments.commit",
		"result": map[string]any{
			"destination_kind": "note_attachment",
			"note_id":          note.NoteID,
			"note_scope":       "profile",
			"note_title":       note.Title,
			"attachment_ids":   []string{"client_attachment_note_success"},
			"files": []map[string]any{
				{
					"client_attachment_id": "client_attachment_note_success",
					"attachment_id":        "natt_success",
					"filename":             "idea.png",
					"content_type":         "image/png",
					"size_bytes":           12,
				},
			},
		},
	}, &completed)
	if completed.State != assistantStateCompleted || completed.AssistantMessage == nil {
		t.Fatalf("note commit did not complete: %#v", completed)
	}
	if !strings.Contains(completed.AssistantMessage.Text, "Stored 1 attachment(s) in `Project Ideas`.") {
		t.Fatalf("unexpected completion text: %#v", completed.AssistantMessage.Text)
	}
	if len(completed.AssistantMessage.Cards) != 1 || completed.AssistantMessage.Cards[0].Kind != "note" || completed.AssistantMessage.Cards[0].NoteID != note.NoteID {
		t.Fatalf("completion cards = %#v, want note card", completed.AssistantMessage.Cards)
	}
	records, err := db.ListAssistantAttachments(ctx, apiSession.ID)
	if err != nil {
		t.Fatalf("ListAssistantAttachments: %v", err)
	}
	if len(records) != 1 || records[0].Status != "committed" || records[0].CommittedAt == nil {
		t.Fatalf("attachment status = %#v, want one committed record", records)
	}
}

func TestAssistantAttachmentSMBCommitCompletesAndMarksCommitted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_attachment_smb_success", Email: "attachment-smb-success@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_attachment_smb_success", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	appSession := domain.AppSession{ID: "sess_attachment_smb_success", UserID: user.ID, TokenHash: hashToken("attachment-smb-success-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, appSession))
	must(t, db.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
		ID:               "afile_tax_folder_success",
		HomeID:           home.ID,
		ServiceProfileID: "smb-primary",
		Path:             "Documents/Taxes",
		Name:             "Taxes",
		IsDirectory:      true,
		SearchText:       "Documents Taxes",
		MetadataJSON:     "{}",
		EmbeddingJSON:    "[]",
		UpdatedAt:        now,
		EmbeddingModel:   "test",
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var apiSession assistantAPISession
	requestJSON(t, testServer, "attachment-smb-success-token", http.MethodPost, "/v1/home/assistant/sessions", nil, &apiSession)

	var initial assistantRunResponse
	requestJSON(t, testServer, "attachment-smb-success-token", http.MethodPost, "/v1/home/assistant/sessions/"+apiSession.ID+"/messages", map[string]any{
		"content": "store this in the Taxes folder on SMB share",
		"attachments": []map[string]any{
			{
				"client_attachment_id": "client_attachment_smb_success",
				"filename":             "tax.pdf",
				"content_type":         "application/pdf",
				"size_bytes":           24,
				"checksum_sha256":      "def456",
				"kind":                 "document",
			},
		},
	}, &initial)
	if !initial.RequiresConfirmation || initial.PendingActionSummary == nil {
		t.Fatalf("initial run did not request attachment confirmation: %#v", initial)
	}

	var toolRun assistantRunResponse
	requestJSON(t, testServer, "attachment-smb-success-token", http.MethodPost, "/v1/home/assistant/runs/"+initial.ID+"/confirm", map[string]any{"approved": true}, &toolRun)
	if !toolRun.RequiresClientTools || toolRun.ClientToolRequest == nil || toolRun.ClientToolRequest.ToolName != "attachments.commit" {
		t.Fatalf("confirmed run did not request attachment client tool: %#v", toolRun)
	}
	if got := toolRun.ClientToolRequest.Arguments["source_id"]; got != "smb-primary" {
		t.Fatalf("attachment client tool source_id = %#v, want smb-primary", got)
	}

	var completed assistantRunResponse
	requestJSON(t, testServer, "attachment-smb-success-token", http.MethodPost, "/v1/home/assistant/runs/"+toolRun.ID+"/client-tool-results", map[string]any{
		"tool_name": "attachments.commit",
		"result": map[string]any{
			"destination_kind": "smb",
			"target_path":      "Documents/Taxes",
			"attachment_ids":   []string{"client_attachment_smb_success"},
			"files": []map[string]any{
				{
					"client_attachment_id": "client_attachment_smb_success",
					"filename":             "tax.pdf",
					"path":                 "Documents/Taxes/tax.pdf",
					"content_type":         "application/pdf",
					"size_bytes":           24,
				},
			},
		},
	}, &completed)
	if completed.State != assistantStateCompleted || completed.AssistantMessage == nil {
		t.Fatalf("SMB commit did not complete: %#v", completed)
	}
	if !strings.Contains(completed.AssistantMessage.Text, "Stored 1 attachment(s) in `Documents/Taxes`.") {
		t.Fatalf("unexpected completion text: %#v", completed.AssistantMessage.Text)
	}
	if len(completed.AssistantMessage.Cards) != 1 || completed.AssistantMessage.Cards[0].Kind != "file" || completed.AssistantMessage.Cards[0].Path != "Documents/Taxes/tax.pdf" {
		t.Fatalf("completion cards = %#v, want file card", completed.AssistantMessage.Cards)
	}
	records, err := db.ListAssistantAttachments(ctx, apiSession.ID)
	if err != nil {
		t.Fatalf("ListAssistantAttachments: %v", err)
	}
	if len(records) != 1 || records[0].Status != "committed" || records[0].CommittedAt == nil {
		t.Fatalf("attachment status = %#v, want one committed record", records)
	}
}

func TestAssistantAttachmentSMBFolderResolutionUsesFileIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_attachment_smb", Email: "attachment-smb@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_attachment_smb", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AssistantSession{ID: "asess_attachment_smb", HomeID: home.ID, UserID: user.ID, Title: "SMB uploads", LastMessageAt: now, CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateAssistantSession(ctx, session))
	must(t, db.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
		ID:             "afile_tax_folder",
		HomeID:         home.ID,
		Path:           "Documents/Taxes",
		Name:           "Taxes",
		IsDirectory:    true,
		SearchText:     "Documents Taxes",
		MetadataJSON:   "{}",
		EmbeddingJSON:  "[]",
		UpdatedAt:      now,
		EmbeddingModel: "test",
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}
	attachments := []assistantMessageAttachment{{
		ClientAttachmentID: "client_attachment_tax",
		Filename:           "tax.pdf",
		ContentType:        "application/pdf",
		SizeBytes:          12,
		ChecksumSHA256:     "abc123",
		Kind:               "document",
	}}

	response, err := server.processAssistantMessageWithAttachments(ctx, home, membership, auth, session, "store this in the Taxes folder on SMB share", attachments, "device", "UTC")
	if err != nil {
		t.Fatalf("process smb attachment message: %v", err)
	}
	if !response.RequiresConfirmation || response.PendingActionSummary == nil {
		t.Fatalf("SMB folder did not resolve to confirmation: %#v", response)
	}
	if !assistantSummaryDetailsContain(response.PendingActionSummary.Details, "Target folder", "Documents/Taxes") {
		t.Fatalf("confirmation details did not include resolved folder: %#v", response.PendingActionSummary.Details)
	}
}

func TestAssistantFileSearchUsesFileIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_files", Email: "assistant-files@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_files", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
		ID:               "afile_2025_taxes",
		HomeID:           home.ID,
		ServiceProfileID: "archive",
		Path:             "Documents/Taxes/2025 Taxes",
		Name:             "2025 Taxes",
		IsDirectory:      true,
		SearchText:       "Documents Taxes 2025 Taxes",
		MetadataJSON:     "{}",
		EmbeddingJSON:    "[]",
		EmbeddingModel:   "test",
		EmbeddingVersion: "test",
		UpdatedAt:        now,
	}))
	must(t, db.UpsertAssistantFileIndex(ctx, domain.AssistantFileIndex{
		ID:               "afile_2025_w2",
		HomeID:           home.ID,
		ServiceProfileID: "archive",
		Path:             "Documents/Taxes/2025 Taxes/W2.pdf",
		Name:             "W2.pdf",
		IsDirectory:      false,
		SearchText:       "Documents Taxes 2025 W2",
		MetadataJSON:     "{}",
		EmbeddingJSON:    "[]",
		EmbeddingModel:   "test",
		EmbeddingVersion: "test",
		UpdatedAt:        now.Add(-time.Minute),
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.ProjectDocsEnabled = false
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}

	traceCtx := withAssistantTraceContext(ctx, assistantTraceContext{
		HomeID:    home.ID,
		UserID:    user.ID,
		SessionID: "asess_trace",
		RunID:     "arun_trace",
	})
	answer, err := server.generateAssistantResponse(traceCtx, home, membership, auth, settings, "find 2025 taxes")
	if err != nil {
		t.Fatalf("generateAssistantResponse: %v", err)
	}
	if len(answer.Cards) != 1 || answer.Cards[0].Kind != "file" || answer.Cards[0].Path != "Documents/Taxes/2025 Taxes" || answer.Cards[0].SourceID != "archive" {
		t.Fatalf("file search cards = %#v", answer.Cards)
	}
	diagnostics := assistantDiagnosticsFromContent(answer)
	if diagnostics == nil || diagnostics.ToolKind != string(assistantIntentFilesSearch) || diagnostics.IntentKind != string(assistantIntentFilesSearch) || diagnostics.Query != "2025 taxes" {
		t.Fatalf("file search diagnostics = %#v", diagnostics)
	}
	events, total := server.assistantTraceSnapshot(home.ID, "asess_trace", "arun_trace", 20)
	if total == 0 {
		t.Fatal("assistant trace did not record the file-search workflow")
	}
	if !assistantTraceHasEvent(events, "assistant.tool.resolved") || !assistantTraceHasEvent(events, "assistant.tool.execute_done") {
		t.Fatalf("assistant trace events = %#v, want tool resolution and completion", events)
	}

	allAnswer, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "show me all 2025 taxes")
	if err != nil {
		t.Fatalf("generateAssistantResponse all files: %v", err)
	}
	if len(allAnswer.Cards) < 2 {
		t.Fatalf("broad file search cards = %#v, want multiple", allAnswer.Cards)
	}
}

func TestAssistantFileCreateFolderRequiresConfirmation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_file_create", Email: "assistant-file-create@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_file_create", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	settings := defaultAssistantSettings(home.ID, user.ID)
	settings.ProjectDocsEnabled = false
	auth := authContext{User: user}
	membership := domain.HomeMembership{HomeID: home.ID, UserID: user.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}

	answer, err := server.generateAssistantResponse(ctx, home, membership, auth, settings, "create a folder called Warranty in Documents")
	if err != nil {
		t.Fatalf("generateAssistantResponse create folder: %v", err)
	}
	pending := assistantPendingActionFromContent(answer)
	if pending == nil || pending.Kind != "file_create_folder" || pending.FileCreateFolder == nil {
		t.Fatalf("missing file create pending action: %#v", answer)
	}
	if pending.FileCreateFolder.Path != "Documents/Warranty" {
		t.Fatalf("create folder path = %q, want Documents/Warranty", pending.FileCreateFolder.Path)
	}
}

func TestAssistantMemorySearchUsesConversationIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_assistant_memory", Email: "assistant-memory@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_assistant_memory", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AssistantSession{ID: "asess_memory", HomeID: home.ID, UserID: user.ID, Title: "Calendar defaults", LastMessageAt: now, CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateAssistantSession(ctx, session))
	must(t, db.CreateAssistantMessage(ctx, domain.AssistantMessage{
		ID:          "amsg_memory_user",
		SessionID:   session.ID,
		Role:        assistantRoleUser,
		Status:      assistantStateCompleted,
		ContentJSON: `{"text":"Default calendar should be configurable in calendar settings."}`,
		ModelName:   assistantModelName,
		CreatedAt:   now,
	}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := server.indexAssistantConversation(ctx, session, user.ID); err != nil {
		t.Fatalf("indexAssistantConversation: %v", err)
	}
	settings := defaultAssistantSettings(home.ID, user.ID)
	answer, err := server.answerAssistantMemoryPrompt(ctx, home.ID, user.ID, &session, settings, "what did we decide about calendar defaults")
	if err != nil {
		t.Fatalf("answerAssistantMemoryPrompt: %v", err)
	}
	if len(answer.Cards) != 1 || answer.Cards[0].Kind != "assistant_conversation" || answer.Cards[0].Path != session.ID {
		t.Fatalf("memory cards = %#v", answer.Cards)
	}
}

func TestAssistantHomeAssistantQueryUsesLiveStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	states := []protocol.HomeAssistantState{
		{EntityID: "light.garage_overhead", State: "on", Attributes: map[string]any{"friendly_name": "Garage Overhead"}},
		{EntityID: "switch.kitchen_outlet", State: "on", Attributes: map[string]any{"friendly_name": "Kitchen Outlet"}},
		{EntityID: "sensor.garage_temperature", State: "72", Attributes: map[string]any{"friendly_name": "Garage Temperature"}},
		{EntityID: "light.kitchenlightshelly", State: "off", Attributes: map[string]any{"friendly_name": "KitchenLightShelly Switch 0"}},
		{EntityID: "switch.kitchen_light_scene", State: "on", Attributes: map[string]any{"friendly_name": "Kitchen Light Scene"}},
		{EntityID: "binary_sensor.kitchen_light_motion", State: "off", Attributes: map[string]any{"friendly_name": "Kitchen Light Motion"}},
		{EntityID: "light.kitchen_island", State: "off", Attributes: map[string]any{"friendly_name": "Kitchen Island"}},
		{EntityID: "light.porch", State: "off", Attributes: map[string]any{"friendly_name": "Porch Light"}},
	}
	go serveAssistantHomeAssistantStates(ctx, t, agentConn, agentID, homeID, states)

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var onRun assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "what entities are on",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &onRun)
	if onRun.AssistantMessage == nil {
		t.Fatalf("missing assistant message for on query: %#v", onRun)
	}
	if !strings.Contains(onRun.AssistantMessage.Text, "Garage Overhead") || !strings.Contains(onRun.AssistantMessage.Text, "Kitchen Outlet") {
		t.Fatalf("on query text missing live on entities: %q", onRun.AssistantMessage.Text)
	}
	if strings.Contains(onRun.AssistantMessage.Text, "Garage Temperature") {
		t.Fatalf("on query included non-on entity: %q", onRun.AssistantMessage.Text)
	}
	if !assistantCardsContainKindAndSearchText(onRun.AssistantMessage.Cards, "homeassistant", "light.garage_overhead") ||
		!assistantCardsContainKindAndSearchText(onRun.AssistantMessage.Cards, "homeassistant", "switch.kitchen_outlet") {
		t.Fatalf("on query cards missing live entities: %#v", onRun.AssistantMessage.Cards)
	}

	var garageRun assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "garage entities",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &garageRun)
	if garageRun.AssistantMessage == nil {
		t.Fatalf("missing assistant message for garage query: %#v", garageRun)
	}
	if !strings.Contains(garageRun.AssistantMessage.Text, "Garage Overhead") || !strings.Contains(garageRun.AssistantMessage.Text, "Garage Temperature") {
		t.Fatalf("garage query text missing garage entities: %q", garageRun.AssistantMessage.Text)
	}
	if strings.Contains(garageRun.AssistantMessage.Text, "Kitchen Outlet") {
		t.Fatalf("garage query included non-garage entity: %q", garageRun.AssistantMessage.Text)
	}
	if !assistantCardsContainKindAndSearchText(garageRun.AssistantMessage.Cards, "homeassistant", "light.garage_overhead") ||
		!assistantCardsContainKindAndSearchText(garageRun.AssistantMessage.Cards, "homeassistant", "sensor.garage_temperature") {
		t.Fatalf("garage query cards missing garage entities: %#v", garageRun.AssistantMessage.Cards)
	}

	var garageLightRun assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "can you find all the garage light entities",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &garageLightRun)
	if garageLightRun.AssistantMessage == nil {
		t.Fatalf("missing assistant message for garage light query: %#v", garageLightRun)
	}
	if !strings.Contains(garageLightRun.AssistantMessage.Text, "Garage Overhead") {
		t.Fatalf("garage light query missing garage light: %q", garageLightRun.AssistantMessage.Text)
	}
	if strings.Contains(garageLightRun.AssistantMessage.Text, "Garage Temperature") || strings.Contains(garageLightRun.AssistantMessage.Text, "Kitchen Outlet") {
		t.Fatalf("garage light query included wrong entity: %q", garageLightRun.AssistantMessage.Text)
	}
	if !assistantCardsContainKindAndSearchText(garageLightRun.AssistantMessage.Cards, "homeassistant", "light.garage_overhead") {
		t.Fatalf("garage light query cards missing garage light: %#v", garageLightRun.AssistantMessage.Cards)
	}

	var kitchenLightRun assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "show me all the kitchen light entities",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &kitchenLightRun)
	if kitchenLightRun.AssistantMessage == nil {
		t.Fatalf("missing assistant message for kitchen light query: %#v", kitchenLightRun)
	}
	for _, want := range []string{"KitchenLightShelly", "Kitchen Light Scene", "Kitchen Light Motion", "Kitchen Island"} {
		if !strings.Contains(kitchenLightRun.AssistantMessage.Text, want) {
			t.Fatalf("kitchen light query missing %q: %q", want, kitchenLightRun.AssistantMessage.Text)
		}
	}
	if strings.Contains(kitchenLightRun.AssistantMessage.Text, "Kitchen Outlet") || strings.Contains(kitchenLightRun.AssistantMessage.Text, "Porch Light") {
		t.Fatalf("kitchen light query included wrong entity: %q", kitchenLightRun.AssistantMessage.Text)
	}
	for _, want := range []string{"light.kitchenlightshelly", "switch.kitchen_light_scene", "binary_sensor.kitchen_light_motion", "light.kitchen_island"} {
		if !assistantCardsContainKindAndSearchText(kitchenLightRun.AssistantMessage.Cards, "homeassistant", want) {
			t.Fatalf("kitchen light query cards missing %q: %#v", want, kitchenLightRun.AssistantMessage.Cards)
		}
	}
}

func TestAssistantHomeAssistantControlRequiresExactConfirmation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	states := []protocol.HomeAssistantState{
		{EntityID: "light.porch", State: "off", Attributes: map[string]any{"friendly_name": "Porch Light"}},
		{EntityID: "light.kitchen_island", State: "off", Attributes: map[string]any{"friendly_name": "Kitchen Island"}},
		{EntityID: "light.kitchen_sink", State: "on", Attributes: map[string]any{"friendly_name": "Kitchen Sink Light"}},
		{EntityID: "lock.front_door", State: "locked", Attributes: map[string]any{"friendly_name": "Front Door"}},
	}
	serviceCalls := make(chan protocol.HomeAssistantCallServiceRequest, 2)
	go serveAssistantHomeAssistantControl(ctx, t, agentConn, agentID, homeID, states, serviceCalls)

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var planned assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "turn on the porch light",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &planned)
	if !planned.RequiresConfirmation || planned.PendingActionSummary == nil {
		t.Fatalf("Home Assistant control did not require confirmation: %#v", planned)
	}
	if planned.PendingActionSummary.Kind != "homeassistant_control" || planned.PendingActionSummary.Destructive {
		t.Fatalf("unexpected Home Assistant confirmation summary: %#v", planned.PendingActionSummary)
	}
	if !assistantSummaryDetailsContain(planned.PendingActionSummary.Details, "Entity ID", "light.porch") ||
		!assistantSummaryDetailsContain(planned.PendingActionSummary.Details, "Command", "turn on") {
		t.Fatalf("Home Assistant confirmation omitted exact action details: %#v", planned.PendingActionSummary.Details)
	}
	select {
	case call := <-serviceCalls:
		t.Fatalf("Home Assistant service ran before confirmation: %#v", call)
	default:
	}

	var completed assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/runs/"+planned.ID+"/confirm", map[string]any{"approved": true}, &completed)
	if completed.State != assistantStateCompleted || completed.AssistantMessage == nil || !strings.Contains(completed.AssistantMessage.Text, "Porch Light") {
		t.Fatalf("confirmed Home Assistant action did not complete: %#v", completed)
	}
	select {
	case call := <-serviceCalls:
		if call.Domain != "light" || call.Service != "turn_on" {
			t.Fatalf("Home Assistant service = %s.%s, want light.turn_on", call.Domain, call.Service)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(call.Body, &body); err != nil {
			t.Fatalf("decode Home Assistant service body: %v", err)
		}
		if body["entity_id"] != "light.porch" {
			t.Fatalf("Home Assistant entity_id = %#v, want light.porch", body["entity_id"])
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for confirmed Home Assistant service call")
	}

	var ambiguous assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "turn off the kitchen light",
	}, &ambiguous)
	if ambiguous.RequiresConfirmation || ambiguous.AssistantMessage == nil || !strings.Contains(ambiguous.AssistantMessage.Text, "more than one") {
		t.Fatalf("ambiguous Home Assistant target was not rejected safely: %#v", ambiguous)
	}
	if len(ambiguous.AssistantMessage.Cards) != 2 {
		t.Fatalf("ambiguous Home Assistant cards = %#v, want two candidates", ambiguous.AssistantMessage.Cards)
	}

	var unlock assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "/ha unlock the front door",
	}, &unlock)
	if !unlock.RequiresConfirmation || unlock.PendingActionSummary == nil || !unlock.PendingActionSummary.Destructive {
		t.Fatalf("unlock was not marked high impact: %#v", unlock)
	}
	var cancelled assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/runs/"+unlock.ID+"/confirm", map[string]any{"approved": false}, &cancelled)
	if cancelled.State != assistantStateCompleted || cancelled.RequiresConfirmation || cancelled.AssistantMessage == nil || !strings.Contains(cancelled.AssistantMessage.Text, "No changes were made") {
		t.Fatalf("cancelled unlock run = %#v", cancelled)
	}
	select {
	case call := <-serviceCalls:
		t.Fatalf("cancelled unlock sent a Home Assistant service call: %#v", call)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAssistantHermesCommandRequiresInstalledApp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	_ = homeID
	_ = agentID

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var run assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "/Hermes what should I check next?",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &run)
	if run.AssistantMessage == nil {
		t.Fatalf("missing assistant message: %#v", run)
	}
	if run.AssistantMessage.Text != "Hermes chat is not configured on the primary Hank Agent yet." {
		t.Fatalf("assistant text = %q", run.AssistantMessage.Text)
	}
	if run.Diagnostics == nil || run.Diagnostics.ToolKind != string(assistantIntentHermesChat) || run.Diagnostics.Query != "what should I check next?" {
		t.Fatalf("diagnostics = %#v", run.Diagnostics)
	}
}

func TestAssistantHermesCommandPrefersInstalledApp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	heartbeat, err := protocol.NewEnvelope(protocol.TypeAgentHeartbeat, "", agentID, homeID, protocol.AgentHeartbeat{
		AgentID:      agentID,
		SentAt:       time.Now().UTC(),
		Capabilities: []string{protocol.CapabilityAppsSandboxV1, "apps.hermes.chat"},
	})
	if err != nil {
		t.Fatalf("NewEnvelope heartbeat: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, heartbeat); err != nil {
		t.Fatalf("heartbeat write: %v", err)
	}
	waitForAgentCapability(t, testServer, sessionToken, "apps.hermes.chat")

	requests := make(chan assistantHermesCommandCapture, 1)
	go serveAssistantHermesAppInvoke(ctx, t, agentConn, agentID, homeID, requests)

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var run assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "/Hermes what should I check next?",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &run)
	if run.AssistantMessage == nil {
		t.Fatalf("missing assistant message: %#v", run)
	}
	if run.AssistantMessage.Text != "Hermes app says check the package runtime." {
		t.Fatalf("assistant text = %q", run.AssistantMessage.Text)
	}

	select {
	case request := <-requests:
		if request.Command != protocol.CommandAppsInvoke {
			t.Fatalf("command = %q, want %q", request.Command, protocol.CommandAppsInvoke)
		}
		if request.AppInvoke.AppID != "hermes" || request.AppInvoke.CommandID != "chat" {
			t.Fatalf("apps.invoke request = %#v", request.AppInvoke)
		}
		var input hermesChatAppRequest
		if err := json.Unmarshal(request.AppInvoke.Input, &input); err != nil {
			t.Fatalf("Decode app input: %v", err)
		}
		if input.Prompt != "what should I check next?" {
			t.Fatalf("app prompt = %q", input.Prompt)
		}
		if !strings.Contains(input.ConversationID, session.ID) || !strings.Contains(input.SessionKey, session.ID) {
			t.Fatalf("app scope = conversation %q session key %q", input.ConversationID, input.SessionKey)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for app invoke command")
	}
}

func TestAssistantYDownloadCommandRequiresInstalledApp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	_ = homeID
	_ = agentID

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var run assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "/ydownload https://www.youtube.com/watch?v=UYbZo6UuMMY",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &run)
	if run.AssistantMessage == nil {
		t.Fatalf("missing assistant message: %#v", run)
	}
	if run.AssistantMessage.Text != "YDownload is not configured on the primary Hank Agent yet." {
		t.Fatalf("assistant text = %q", run.AssistantMessage.Text)
	}
	if run.Diagnostics == nil || run.Diagnostics.ToolKind != string(assistantIntentYDownloadCommand) || run.Diagnostics.Query != "https://www.youtube.com/watch?v=UYbZo6UuMMY" {
		t.Fatalf("diagnostics = %#v", run.Diagnostics)
	}
}

func TestAssistantYDownloadCommandInvokesInstalledApp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	heartbeat, err := protocol.NewEnvelope(protocol.TypeAgentHeartbeat, "", agentID, homeID, protocol.AgentHeartbeat{
		AgentID:      agentID,
		SentAt:       time.Now().UTC(),
		Capabilities: []string{protocol.CapabilityAppsSandboxV1, "apps.ydownload.download"},
	})
	if err != nil {
		t.Fatalf("NewEnvelope heartbeat: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, heartbeat); err != nil {
		t.Fatalf("heartbeat write: %v", err)
	}
	waitForAgentCapability(t, testServer, sessionToken, "apps.ydownload.download")

	requests := make(chan assistantAppCommandCapture, 1)
	go serveAssistantAppInvoke(ctx, t, agentConn, agentID, homeID, requests, func(request protocol.AppsInvokeRequest) json.RawMessage {
		if request.AppID != "ydownload" || request.CommandID != "download" {
			t.Errorf("apps.invoke request = %#v", request)
		}
		return json.RawMessage(`{"text":"Downloaded 1 file(s) to YouTube.","destination_path":"YouTube","files":[{"path":"YouTube/video.mp4","size":123}]}`)
	})

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var run assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "/ydownload https://www.youtube.com/watch?v=UYbZo6UuMMY",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &run)
	if run.AssistantMessage == nil {
		t.Fatalf("missing assistant message: %#v", run)
	}
	if run.AssistantMessage.Text != "Downloaded 1 file(s) to YouTube." {
		t.Fatalf("assistant text = %q", run.AssistantMessage.Text)
	}

	select {
	case request := <-requests:
		if request.Command != protocol.CommandAppsInvoke {
			t.Fatalf("command = %q, want %q", request.Command, protocol.CommandAppsInvoke)
		}
		if request.AppInvoke.AppID != "ydownload" || request.AppInvoke.CommandID != "download" {
			t.Fatalf("apps.invoke request = %#v", request.AppInvoke)
		}
		var input struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(request.AppInvoke.Input, &input); err != nil {
			t.Fatalf("Decode app input: %v", err)
		}
		if input.URL != "https://www.youtube.com/watch?v=UYbZo6UuMMY" {
			t.Fatalf("app url = %q", input.URL)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for app invoke command")
	}
}

func TestInferUniqueFileMatchParentDirectory(t *testing.T) {
	matches := []protocol.FileItem{
		{
			SourceID:    "local",
			Path:        "Documents/Taxes/2025 Taxes/hankai-eval.txt",
			Name:        "hankai-eval.txt",
			IsDirectory: false,
		},
	}

	path, sourceID, ok := inferUniqueFileMatchParentDirectory(matches)
	if !ok {
		t.Fatal("inferUniqueFileMatchParentDirectory returned ok=false")
	}
	if path != "Documents/Taxes/2025 Taxes" || sourceID != "local" {
		t.Fatalf("inferred path/source = %q/%q", path, sourceID)
	}
}

func TestInferUniqueFileMatchParentDirectoryRejectsAmbiguousParents(t *testing.T) {
	matches := []protocol.FileItem{
		{SourceID: "local", Path: "Documents/Taxes/2025 Taxes/hankai-eval.txt", Name: "hankai-eval.txt"},
		{SourceID: "local", Path: "Documents/Archive/2025 Taxes/old.txt", Name: "old.txt"},
	}

	if path, sourceID, ok := inferUniqueFileMatchParentDirectory(matches); ok {
		t.Fatalf("inferred ambiguous path/source = %q/%q", path, sourceID)
	}
}

func TestAssistantGenericInstalledAppSlashInvokesApp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	now := time.Now().UTC()
	must(t, db.UpsertHomeApp(ctx, domain.HomeAgentApp{
		HomeID:              homeID,
		AppID:               "members_app",
		Name:                "Members App",
		Version:             "1.0.0",
		Enabled:             true,
		PublicConfigJSON:    `{}`,
		SecretFieldsSetJSON: `{}`,
		CapabilitiesJSON:    `["apps.members.run"]`,
		SlashCommandsJSON:   `[{"command":"/members","command_id":"run","description":"Run member app."}]`,
		CommandsJSON:        `[{"id":"run","mode":"request_response","timeout_seconds":30}]`,
		UserAccess:          domain.HomeAgentAppUserAccessHomeMembers,
		Status:              "installed",
		UpdatedAt:           now,
		UpdatedBy:           "usr_1",
	}))

	requests := make(chan assistantAppCommandCapture, 1)
	go serveAssistantAppInvoke(ctx, t, agentConn, agentID, homeID, requests, func(request protocol.AppsInvokeRequest) json.RawMessage {
		if request.AppID != "members_app" || request.CommandID != "run" {
			t.Errorf("apps.invoke request = %#v", request)
		}
		return json.RawMessage(`{"text":"Members app answered.","cards":[{"kind":"app","title":"Result","summary":"Generic app card","action_title":"Open"}]}`)
	})

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var run assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "/members check the new platform",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &run)
	if run.AssistantMessage == nil {
		t.Fatalf("missing assistant message: %#v", run)
	}
	if run.AssistantMessage.Text != "Members app answered." {
		t.Fatalf("assistant text = %q", run.AssistantMessage.Text)
	}
	if run.Diagnostics == nil || run.Diagnostics.ToolKind != string(assistantIntentInstalledAppCommand) || run.Diagnostics.AppID != "members_app" || run.Diagnostics.CommandID != "run" || run.Diagnostics.SlashCommand != "/members" {
		t.Fatalf("diagnostics = %#v", run.Diagnostics)
	}

	select {
	case request := <-requests:
		if request.Command != protocol.CommandAppsInvoke {
			t.Fatalf("command = %q, want %q", request.Command, protocol.CommandAppsInvoke)
		}
		var input genericInstalledAppInput
		if err := json.Unmarshal(request.AppInvoke.Input, &input); err != nil {
			t.Fatalf("Decode app input: %v", err)
		}
		if input.RawText != "check the new platform" || input.SlashCommand != "/members" {
			t.Fatalf("app input = %#v", input)
		}
		var appContext map[string]interface{}
		if err := json.Unmarshal(request.AppInvoke.Context, &appContext); err != nil {
			t.Fatalf("Decode app context: %v", err)
		}
		if appContext["home_id"] != homeID || appContext["user_id"] != "usr_1" || appContext["role"] != domain.HomeRoleAdmin {
			t.Fatalf("app context = %#v", appContext)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for app invoke command")
	}
}

func TestAssistantInstalledGramatonSlashUsesSearchInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgentWithDB(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	now := time.Now().UTC()
	must(t, db.UpsertHomeApp(ctx, domain.HomeAgentApp{
		HomeID:              homeID,
		AppID:               "gramaton",
		Name:                "Gramaton",
		Version:             "1.0.0",
		Enabled:             true,
		PublicConfigJSON:    `{}`,
		SecretFieldsSetJSON: `{"password":true}`,
		CapabilitiesJSON:    `["apps.gramaton.search"]`,
		SlashCommandsJSON:   `[{"command":"/gramaton","command_id":"search","description":"Search Gramaton."}]`,
		CommandsJSON:        `[{"id":"search","mode":"request_response","timeout_seconds":120}]`,
		UserAccess:          domain.HomeAgentAppUserAccessAdminsOnly,
		Status:              "installed",
		UpdatedAt:           now,
		UpdatedBy:           "usr_1",
	}))

	requests := make(chan assistantAppCommandCapture, 1)
	go serveAssistantAppInvoke(ctx, t, agentConn, agentID, homeID, requests, func(request protocol.AppsInvokeRequest) json.RawMessage {
		if request.AppID != "gramaton" || request.CommandID != "search" {
			t.Errorf("apps.invoke request = %#v", request)
		}
		return json.RawMessage(`{"query":"the arrow","results":[{"id":"movies/the-arrow","title":"The Arrow","year":2026,"type":"movie","summary":"Movie | Demo","page_path":"/movies/the-arrow"},{"id":"series/the-arrow","title":"The Arrow Series","year":2025,"type":"series","summary":"Series | Demo","page_path":"/series/the-arrow"}]}`)
	})

	var session assistantAPISession
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions", nil, &session)

	var run assistantRunResponse
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/assistant/sessions/"+session.ID+"/messages", map[string]any{
		"content": "/gramaton the arrow",
		"device_context": map[string]any{
			"device_id": "test-device",
			"timezone":  "UTC",
		},
	}, &run)
	if run.AssistantMessage == nil {
		t.Fatalf("missing assistant message: %#v", run)
	}
	if !strings.Contains(run.AssistantMessage.Text, "The Arrow") {
		t.Fatalf("assistant text = %q", run.AssistantMessage.Text)
	}

	select {
	case request := <-requests:
		var input protocol.MediaSearchRequest
		if err := json.Unmarshal(request.AppInvoke.Input, &input); err != nil {
			t.Fatalf("Decode app input: %v", err)
		}
		if input.Query != "the arrow" || input.Limit != 10 {
			t.Fatalf("app input = %#v", input)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for app invoke command")
	}
}

func TestAssistantGenericAppOutputContent(t *testing.T) {
	content, err := assistantContentFromGenericAppOutput("Members App", json.RawMessage(`{
		"text":"Members app answered.",
		"cards":[{"kind":"app","title":"Result","summary":"Generic app card","action_title":"Open"}],
		"diagnostics":{"job":"job_123"}
	}`))
	if err != nil {
		t.Fatalf("assistantContentFromGenericAppOutput: %v", err)
	}
	if content.Text != "Members app answered." {
		t.Fatalf("text = %q", content.Text)
	}
	if len(content.Cards) != 1 || content.Cards[0].Title != "Result" {
		t.Fatalf("cards = %#v", content.Cards)
	}
	if content.Meta["app_diagnostics"] == nil {
		t.Fatalf("meta = %#v", content.Meta)
	}

	empty, err := assistantContentFromGenericAppOutput("Members App", nil)
	if err != nil {
		t.Fatalf("empty output: %v", err)
	}
	if empty.Text != "Members App returned an empty response." {
		t.Fatalf("empty text = %q", empty.Text)
	}
}

func TestInstalledAppSlashInputUsesFirstPartySchemas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		intent assistantIntent
		want   string
	}{
		{
			name:   "gramaton search",
			intent: assistantIntent{AppID: "gramaton", CommandID: "search", Query: "the arrow", SlashCommand: "/gramaton"},
			want:   `{"query":"the arrow","limit":10}`,
		},
		{
			name:   "ydownload download",
			intent: assistantIntent{AppID: "ydownload", CommandID: "download", Query: "https://youtu.be/example", SlashCommand: "/ydownload"},
			want:   `{"url":"https://youtu.be/example"}`,
		},
		{
			name:   "hermes chat",
			intent: assistantIntent{AppID: "hermes", CommandID: "chat", Query: "summarize this", SlashCommand: "/Hermes"},
			want:   `{"prompt":"summarize this"}`,
		},
		{
			name:   "generic app",
			intent: assistantIntent{AppID: "members_app", CommandID: "run", Query: "check status", SlashCommand: "/members"},
			want:   `{"raw_text":"check status","slash_command":"/members"}`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := installedAppSlashInput(tt.intent)
			if err != nil {
				t.Fatalf("installedAppSlashInput: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("input = %s, want %s", got, tt.want)
			}
		})
	}
}

func assistantTraceHasEvent(events []assistantTraceEvent, eventName string) bool {
	for _, event := range events {
		if event.Event == eventName {
			return true
		}
	}
	return false
}

func assistantCardsContainTitle(cards []assistantResultCard, title string) bool {
	for _, card := range cards {
		if card.Title == title {
			return true
		}
	}
	return false
}

func assistantCardsContainKind(cards []assistantResultCard, kind string) bool {
	for _, card := range cards {
		if card.Kind == kind {
			return true
		}
	}
	return false
}

func assistantCardsContainKindAndSearchText(cards []assistantResultCard, kind string, searchText string) bool {
	for _, card := range cards {
		if card.Kind == kind && card.SearchText == searchText {
			return true
		}
	}
	return false
}

func assistantSummaryDetailsContain(details []assistantPendingActionDetail, label string, value string) bool {
	for _, detail := range details {
		if detail.Label == label && detail.Value == value {
			return true
		}
	}
	return false
}

func waitForAgentCapability(t *testing.T, testServer *httptest.Server, sessionToken string, capability string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var payload struct {
			Agent struct {
				Capabilities []string `json:"capabilities"`
			} `json:"agent"`
		}
		requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/agent", nil, &payload)
		if hasCapabilities(payload.Agent.Capabilities, capability) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent capability %q not advertised, got %#v", capability, payload.Agent.Capabilities)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type assistantHermesCommandCapture struct {
	Command   string
	AppInvoke protocol.AppsInvokeRequest
}

type assistantAppCommandCapture struct {
	Command   string
	AppInvoke protocol.AppsInvokeRequest
}

func serveAssistantAppInvoke(ctx context.Context, t *testing.T, agentConn *websocket.Conn, agentID string, homeID string, requests chan<- assistantAppCommandCapture, output func(protocol.AppsInvokeRequest) json.RawMessage) {
	t.Helper()

	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
			return
		}
		if envelope.Type != protocol.TypeCloudCommand {
			continue
		}
		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil {
			return
		}
		capture := assistantAppCommandCapture{Command: command.Command}
		if command.Command == protocol.CommandAppsInvoke {
			request, err := decodeProtocolBody[protocol.AppsInvokeRequest](command.Body)
			if err != nil {
				return
			}
			capture.AppInvoke = request
			select {
			case requests <- capture:
			default:
			}
			response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.AppsInvokeResponse{
				Output: output(request),
			})
			if err != nil {
				return
			}
			_ = wsjson.Write(ctx, agentConn, response)
			continue
		}
		select {
		case requests <- capture:
		default:
		}
	}
}

func serveAssistantHermesAppInvoke(ctx context.Context, t *testing.T, agentConn *websocket.Conn, agentID string, homeID string, requests chan<- assistantHermesCommandCapture) {
	t.Helper()

	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
			return
		}
		if envelope.Type != protocol.TypeCloudCommand {
			continue
		}
		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil {
			return
		}
		capture := assistantHermesCommandCapture{Command: command.Command}
		if command.Command == protocol.CommandAppsInvoke {
			request, err := decodeProtocolBody[protocol.AppsInvokeRequest](command.Body)
			if err != nil {
				return
			}
			capture.AppInvoke = request
			select {
			case requests <- capture:
			default:
			}
			output, err := json.Marshal(hermesChatAppResponse{
				Text:           "Hermes app says check the package runtime.",
				Model:          "hermes-agent",
				ResponseID:     "resp_app",
				ConversationID: "conv_app",
			})
			if err != nil {
				return
			}
			response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.AppsInvokeResponse{
				Output: output,
			})
			if err != nil {
				return
			}
			_ = wsjson.Write(ctx, agentConn, response)
			continue
		}
		select {
		case requests <- capture:
		default:
		}
	}
}

func serveAssistantHomeAssistantStates(ctx context.Context, t *testing.T, agentConn *websocket.Conn, agentID string, homeID string, states []protocol.HomeAssistantState) {
	t.Helper()

	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
			return
		}
		if envelope.Type != protocol.TypeCloudCommand {
			continue
		}
		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil {
			return
		}
		if command.Command != "homeassistant.fetch_states" {
			continue
		}
		response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.HomeAssistantFetchStatesResponse{States: states})
		if err != nil {
			return
		}
		_ = wsjson.Write(ctx, agentConn, response)
	}
}

func serveAssistantHomeAssistantControl(
	ctx context.Context,
	t *testing.T,
	agentConn *websocket.Conn,
	agentID string,
	homeID string,
	states []protocol.HomeAssistantState,
	serviceCalls chan<- protocol.HomeAssistantCallServiceRequest,
) {
	t.Helper()

	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
			return
		}
		if envelope.Type != protocol.TypeCloudCommand {
			continue
		}
		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil {
			return
		}
		var response protocol.Envelope
		switch command.Command {
		case "homeassistant.fetch_states":
			response, err = protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.HomeAssistantFetchStatesResponse{States: states})
		case "homeassistant.call_service":
			request, decodeErr := decodeProtocolBody[protocol.HomeAssistantCallServiceRequest](command.Body)
			if decodeErr != nil {
				return
			}
			select {
			case serviceCalls <- request:
			case <-ctx.Done():
				return
			}
			response, err = protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.HomeAssistantCallServiceResponse{Result: json.RawMessage(`[]`)})
		default:
			continue
		}
		if err != nil {
			return
		}
		if err := wsjson.Write(ctx, agentConn, response); err != nil {
			return
		}
	}
}

func decodeProtocolBody[T any](body json.RawMessage) (T, error) {
	var out T
	if len(body) == 0 {
		return out, nil
	}
	return out, json.Unmarshal(body, &out)
}
