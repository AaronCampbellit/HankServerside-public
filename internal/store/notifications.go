package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/dropfile/HankServerside/internal/domain"
)

const (
	NotificationChannelNotes    = "hank_notes_events"
	NotificationChannelProfiles = "hank_profile_events"
)

type Notification struct {
	Channel string
	Payload json.RawMessage
}

func (s *Store) Listen(ctx context.Context, channels ...string) (<-chan Notification, error) {
	conn, err := pgx.Connect(ctx, s.databaseURL)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		if _, err := conn.Exec(ctx, `LISTEN `+pgx.Identifier{channel}.Sanitize()); err != nil {
			_ = conn.Close(context.Background())
			return nil, err
		}
	}

	out := make(chan Notification, 16)
	go func() {
		defer close(out)
		defer conn.Close(context.Background())
		for {
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				return
			}
			select {
			case out <- Notification{Channel: notification.Channel, Payload: json.RawMessage(notification.Payload)}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func txNotify(ctx context.Context, tx *dbTx, channel string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `SELECT pg_notify(?, ?)`, channel, string(encoded))
	return err
}

func txNotifyNoteChanged(ctx context.Context, tx *dbTx, note domain.UserNote, event string) error {
	payload := map[string]any{
		"event":            event,
		"note_id":          note.NoteID,
		"note_internal_id": note.ID,
		"user_id":          note.OwnerUserID,
		"owner_user_id":    note.OwnerUserID,
		"home_id":          note.HomeID,
		"revision":         note.Revision,
		"collab_version":   note.CollabVersion,
		"updated_by":       note.UpdatedBy,
	}
	return txNotify(ctx, tx, NotificationChannelNotes, payload)
}

func txNotifyProfileChanged(ctx context.Context, tx *dbTx, userID string, event string, revision int) error {
	return txNotify(ctx, tx, NotificationChannelProfiles, map[string]any{
		"event":    event,
		"user_id":  userID,
		"revision": revision,
	})
}
