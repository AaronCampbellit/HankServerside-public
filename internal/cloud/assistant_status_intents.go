package cloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func executeAssistantAssistantStatusTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	settings := normalizeAssistantSettings(runtime.Settings)
	status := server.assistantStatusWithSettings(ctx, runtime.Auth.User.ID, settings)
	indexStats, err := server.store.AssistantIndexStats(ctx, runtime.Home.ID, runtime.Auth.User.ID)

	rows := []string{
		"HankAI status:",
		fmt.Sprintf("- Provider: %s", status.Provider),
		fmt.Sprintf("- Chat model: %s", assistantStatusValue(status.ChatModel)),
		fmt.Sprintf("- Embeddings: %s (%s)", enabledLabel(status.EmbeddingConfigured), assistantStatusValue(status.EmbeddingModel)),
		fmt.Sprintf("- Planner: %s", enabledLabel(settings.PlannerEnabled)),
		fmt.Sprintf("- Prompt profile: %s", assistantStatusValue(settings.PromptProfile)),
	}
	if !status.EmbeddingConfigured || status.EmbeddingModel == "local-hash" {
		rows = append(rows, "- Embedding quality: local hash fallback; semantic retrieval quality is degraded")
	}
	if err != nil {
		rows = append(rows, "- Index: unavailable")
	} else {
		rows = append(rows,
			fmt.Sprintf("- Vector mode: %s", assistantStatusValue(indexStats.VectorMode)),
			fmt.Sprintf("- Indexed chunks: %d (%d embedded)", indexStats.ChunkCount, indexStats.EmbeddedChunkCount),
			fmt.Sprintf("- Indexed files: %d (%d embedded)", indexStats.FileCount, indexStats.EmbeddedFileCount),
			fmt.Sprintf("- Index queue: %d queued, %d running, %d failed", indexStats.QueuedJobCount, indexStats.RunningJobCount, indexStats.FailedJobCount),
			fmt.Sprintf("- Past conversations: %d", indexStats.ConversationCount),
		)
	}
	rows = append(rows,
		fmt.Sprintf("- Home Assistant source: %s", enabledLabel(settings.HomeAssistantEnabled)),
		fmt.Sprintf("- Files source: %s", enabledLabel(settings.FilesEnabled)),
		fmt.Sprintf("- Notes source: %s", enabledLabel(settings.ProfileNotesEnabled || settings.HomeNotesEnabled)),
		fmt.Sprintf("- Calendar source: %s", enabledLabel(settings.CalendarEnabled)),
		fmt.Sprintf("- Project docs source: %s", enabledLabel(settings.ProjectDocsEnabled)),
	)
	return assistantMessageContent{Text: strings.Join(rows, "\n")}, nil
}

func executeAssistantAgentStatusTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	stored, err := server.store.ListAgentsByHome(ctx, runtime.Home.ID)
	if err != nil {
		return assistantMessageContent{}, err
	}
	if len(stored) == 0 {
		return assistantMessageContent{Text: "No agents are registered for this Home."}, nil
	}

	// Overlay live connection state (status + capabilities) onto the stored
	// roster so offline devices still appear with their last-seen time.
	live := map[string]AgentSnapshot{}
	if server.router != nil {
		for _, snapshot := range server.router.AgentsForHome(runtime.Home.ID) {
			live[snapshot.AgentID] = snapshot
		}
	}

	rows := []string{"Devices:"}
	for _, agent := range stored {
		kind := "worker"
		if agentType, ok := normalizeAgentType(agent.AgentType); ok && agentType == AgentTypePrimary {
			kind = "primary Hank Agent"
		}
		status := agentConnectionStatus(false)
		capCount := 0
		lastSeen := agent.LastSeenAt
		if snapshot, ok := live[agent.ID]; ok {
			status = domain.AgentStatusOnline
			capCount = len(snapshot.Capabilities)
			if snapshot.LastSeenAt != nil {
				lastSeen = snapshot.LastSeenAt
			}
		}
		name := assistantStatusValue(agent.Name)
		if status == domain.AgentStatusOnline {
			extra := ""
			if capCount > 0 {
				extra = fmt.Sprintf(", %d capabilities", capCount)
			}
			rows = append(rows, fmt.Sprintf("- %s (%s): online%s", name, kind, extra))
		} else {
			rows = append(rows, fmt.Sprintf("- %s (%s): offline, last seen %s", name, kind, assistantStatusTime(lastSeen)))
		}
	}
	return assistantMessageContent{Text: strings.Join(rows, "\n")}, nil
}

func executeAssistantSyncStatusTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	state, err := server.store.GetHomeNoteSyncState(ctx, runtime.Home.ID)
	if errors.Is(err, store.ErrNotFound) {
		return assistantMessageContent{Text: "No note sync status has been recorded yet."}, nil
	}
	if err != nil {
		return assistantMessageContent{}, err
	}
	rows := []string{
		"Notes sync status:",
		fmt.Sprintf("- Status: %s", assistantStatusValue(state.Status)),
		fmt.Sprintf("- Agent: %s", assistantStatusValue(state.AgentID)),
		fmt.Sprintf("- Last successful sync: %s", assistantStatusTime(state.LastSuccessfulSyncAt)),
		fmt.Sprintf("- Last manifest: %s", assistantStatusTime(state.LastManifestAt)),
		fmt.Sprintf("- Pending pull/push: %d/%d", state.PendingPullCount, state.PendingPushCount),
	}
	if strings.TrimSpace(state.LastError) != "" {
		rows = append(rows, "- Last error: "+state.LastError)
	}
	return assistantMessageContent{Text: strings.Join(rows, "\n")}, nil
}

func executeAssistantBackupStatusTool(ctx context.Context, server *Server, runtime assistantToolRuntime, intent assistantIntent) (assistantMessageContent, error) {
	if runtime.Membership.Role != domain.HomeRoleAdmin {
		return assistantMessageContent{Text: "Backup and storage status is available to Home admins only."}, nil
	}
	if server.storage == nil {
		return assistantMessageContent{Text: "Storage operations are not configured for this Hank server."}, nil
	}
	status, err := server.storage.Status()
	if err != nil {
		return assistantMessageContent{}, err
	}
	rows := []string{
		"Backup and storage status:",
		fmt.Sprintf("- Backup target: %s", assistantStatusValue(status.Backup.Target.Type)),
		fmt.Sprintf("- Last successful backup: %s", assistantStatusTime(status.Backup.LastSuccessfulAt)),
		fmt.Sprintf("- Last backup label: %s", assistantStatusValue(status.Backup.LastBackupLabel)),
		fmt.Sprintf("- Backup failures: %d", status.Backup.FailureCount),
		fmt.Sprintf("- Reported backups: %d", len(status.Backup.Backups)),
		fmt.Sprintf("- Restore test: %s", assistantStatusTime(status.Restore.LastTestAt)),
		fmt.Sprintf("- Active storage tasks: %d", len(status.Tasks)),
		fmt.Sprintf("- Recent failures: %d", len(status.Failures)),
	}
	return assistantMessageContent{Text: strings.Join(rows, "\n")}, nil
}

func assistantStatusValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func assistantStatusTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "never"
	}
	return value.UTC().Format(time.RFC3339)
}
