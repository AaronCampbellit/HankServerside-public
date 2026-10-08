package agent

import (
	"context"
	"strings"
	"time"

	"github.com/coder/websocket"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func (c *Client) startMoveJob(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope, command protocol.RoutedCommand) error {
	request, err := decodeBody[protocol.FilesMoveRequest](command.Body)
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "invalid_file_request", err.Error(), nil)
	}
	request.JobID = strings.TrimSpace(request.JobID)
	if request.JobID == "" {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "invalid_file_request", "missing move job id", nil)
	}
	if strings.TrimSpace(request.DestinationSourceID) == "" {
		request.DestinationSourceID = request.SourceID
	}

	moveCtx, cancel := context.WithCancel(context.Background())
	c.movesMu.Lock()
	if existing, ok := c.moves[request.JobID]; ok {
		existing()
	}
	c.moves[request.JobID] = cancel
	c.movesMu.Unlock()

	responseBody, err := protocol.EncodeBody(protocol.FileOperationJobResponse{OK: true, JobID: request.JobID, Status: "running"})
	if err != nil {
		c.deleteMove(request.JobID)
		cancel()
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "encoding_failed", err.Error(), nil)
	}
	response := protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeCloudResponse,
		RequestID: envelope.RequestID,
		AgentID:   c.agentID,
		HomeID:    envelope.HomeID,
		Timestamp: time.Now().UTC(),
		Payload:   responseBody,
	}
	if err := c.writeJSON(ctx, conn, response); err != nil {
		c.deleteMove(request.JobID)
		cancel()
		return err
	}

	go c.runMoveJob(moveCtx, conn, envelope.HomeID, request)
	return nil
}

func (c *Client) cancelMoveJob(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope, command protocol.RoutedCommand) error {
	request, err := decodeBody[protocol.FilesMoveCancelRequest](command.Body)
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "invalid_file_request", err.Error(), nil)
	}
	cancelled := c.cancelMove(request.JobID)
	responseBody, err := protocol.EncodeBody(protocol.EmptyResponse{OK: cancelled})
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "encoding_failed", err.Error(), nil)
	}
	response := protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeCloudResponse,
		RequestID: envelope.RequestID,
		AgentID:   c.agentID,
		HomeID:    envelope.HomeID,
		Timestamp: time.Now().UTC(),
		Payload:   responseBody,
	}
	return c.writeJSON(ctx, conn, response)
}

func (c *Client) runMoveJob(ctx context.Context, conn *websocket.Conn, homeID string, request protocol.FilesMoveRequest) {
	defer c.deleteMove(request.JobID)

	select {
	case c.moveSlots <- struct{}{}:
		defer func() { <-c.moveSlots }()
	case <-ctx.Done():
		c.emitMoveEvent(context.Background(), conn, homeID, "files.move_failed", request, agentfiles.MoveProgress{}, "cancelled", "move cancelled")
		return
	}

	var lastReport time.Time
	report := func(progress agentfiles.MoveProgress) {
		if time.Since(lastReport) < 500*time.Millisecond && progress.BytesDone < progress.BytesTotal && progress.FilesDone < progress.FilesTotal {
			return
		}
		lastReport = time.Now()
		c.emitMoveEvent(context.Background(), conn, homeID, "files.move_progress", request, progress, "running", "")
	}

	progress, err := c.dispatcher.files.MoveBetweenSourcesWithProgress(ctx, request.SourceID, request.DestinationSourceID, request.From, request.To, request.IsDirectory, report)
	if err != nil {
		status := agentfiles.MoveStatusForError(err)
		c.emitMoveEvent(context.Background(), conn, homeID, "files.move_failed", request, progress, status, agentfiles.MoveErrorMessage(err))
		return
	}
	c.emitMoveEvent(context.Background(), conn, homeID, "files.move_completed", request, progress, "completed", "")
}

func (c *Client) emitMoveEvent(ctx context.Context, conn *websocket.Conn, homeID string, event string, request protocol.FilesMoveRequest, progress agentfiles.MoveProgress, status string, errorMessage string) {
	payload := agentfiles.MoveJobEventFromProgress(request, status, progress, errorMessage)
	if err := c.sendAgentEvent(ctx, conn, event, "files.jobs", payload); err != nil {
		c.logger.Debug("file move event failed", "agent_id", c.agentID, "job_id", request.JobID, "event", event, "error", err)
	}
}

func (c *Client) cancelMove(jobID string) bool {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false
	}
	c.movesMu.Lock()
	cancel, ok := c.moves[jobID]
	c.movesMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (c *Client) deleteMove(jobID string) {
	c.movesMu.Lock()
	delete(c.moves, jobID)
	c.movesMu.Unlock()
}
