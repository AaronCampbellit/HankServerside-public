package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/dropfile/HankServerside/internal/store"
)

type Jobs struct {
	Store           *store.Store
	Retention       time.Duration
	Logger          *slog.Logger
	AfterStorePrune func(store.LifecyclePruneSummary)
	AfterPrune      func(context.Context, time.Time, time.Duration) error
}

func (j Jobs) RunOnce(ctx context.Context) error {
	if j.Store == nil {
		return nil
	}
	retention := j.Retention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	now := time.Now().UTC()
	summary, err := j.Store.PruneLifecycleWithSummary(ctx, now, retention)
	if err != nil && j.Logger != nil {
		j.Logger.Warn("maintenance cleanup failed", "error", err)
	}
	if err == nil && j.Logger != nil && !summary.Empty() {
		j.Logger.Info("maintenance cleanup completed",
			"app_sessions_deleted", summary.AppSessionsDeleted,
			"agent_tokens_deleted", summary.AgentTokensDeleted,
			"home_invitations_deleted", summary.HomeInvitationsDeleted,
			"app_ws_tickets_deleted", summary.AppWebSocketTicketsDeleted,
			"file_transfers_expired", summary.FileTransfersExpired,
			"file_transfers_deleted", summary.FileTransfersDeleted,
			"rate_limit_events_deleted", summary.RateLimitEventsDeleted,
			"relay_requests_deleted", summary.RelayRequestsDeleted,
			"app_connections_deleted", summary.AppConnectionsDeleted,
			"agent_connections_deleted", summary.AgentConnectionsDeleted,
			"audit_events_deleted", summary.AuditEventsDeleted,
			"login_backoff_deleted", summary.LoginBackoffDeleted,
			"assistant_attachments_deleted", summary.AssistantAttachmentsDeleted,
			"note_attachment_rows_deleted", summary.NoteAttachmentRowsDeleted,
			"mcp_attachment_uploads_expired", summary.MCPAttachmentUploadsExpired,
			"mcp_attachment_uploads_deleted", summary.MCPAttachmentUploadsDeleted,
			"desktop_join_credentials_deleted", summary.DesktopJoinCredentialsDeleted,
			"desktop_session_events_deleted", summary.DesktopSessionEventsDeleted,
			"desktop_sessions_deleted", summary.DesktopSessionsDeleted,
			"user_notifications_deleted", summary.UserNotificationsDeleted,
			"notification_source_events_deleted", summary.NotificationSourceEventsDeleted,
			"web_push_deliveries_deleted", summary.WebPushDeliveriesDeleted,
			"web_push_claims_reclaimed", summary.WebPushClaimsReclaimed,
		)
	}
	if err == nil && j.AfterStorePrune != nil {
		j.AfterStorePrune(summary)
	}
	if err == nil && j.AfterPrune != nil {
		if cleanupErr := j.AfterPrune(ctx, now, retention); cleanupErr != nil {
			if j.Logger != nil {
				j.Logger.Warn("post-maintenance cleanup failed", "error", cleanupErr)
			}
			return cleanupErr
		}
	}
	return err
}

func (j Jobs) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := j.RunOnce(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
