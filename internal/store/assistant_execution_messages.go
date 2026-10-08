package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func appendAssistantExecutionMessage(ctx context.Context, tx *dbTx, session, id, role, text string, sources ...json.RawMessage) error {
	payload := map[string]any{"text": text}
	if len(sources) > 0 && len(sources[0]) > 0 {
		payload["sources"] = sources[0]
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO assistant_messages(id,session_id,role,status,content_json,model_name,created_at) VALUES(?,?,?,'completed',?,'hank-assistant-v2',clock_timestamp()) ON CONFLICT(id) DO NOTHING`, id, session, role, string(content))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE assistant_sessions SET last_message_at=clock_timestamp(),updated_at=clock_timestamp(),title=CASE WHEN title='New Conversation' AND ?='user' THEN left(?,80) ELSE title END WHERE id=?`, role, text, session)
	return err
}
func assistantInputMessageID(task, key string) string {
	digest := sha256.Sum256([]byte(key))
	return task + "_input_" + hex.EncodeToString(digest[:])
}
func (s *Store) AssistantSessionExecutionVersion(ctx context.Context, home, user, session string) (int, error) {
	var version int
	err := s.queryRow(ctx, `SELECT execution_version FROM assistant_sessions WHERE id=? AND home_id=? AND user_id=?`, session, home, user).Scan(&version)
	return version, err
}
