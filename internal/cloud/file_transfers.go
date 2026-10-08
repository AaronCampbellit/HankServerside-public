package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func writeFileUploadLimitError(w http.ResponseWriter, maxUploadBytes int64, attemptedUploadBytes int64) {
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
		"error": map[string]any{
			"code":    "upload_too_large",
			"message": "This file is larger than the maximum upload size.",
		},
		"max_upload_bytes":       maxUploadBytes,
		"attempted_upload_bytes": attemptedUploadBytes,
	})
}

func (s *Server) handleFileTransferSetup(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, operation string) {
	type request struct {
		SourceID string `json:"source_id"`
		Path     string `json:"path"`
		Size     int64  `json:"size,omitempty"`
		AgentID  string `json:"agent_id,omitempty"`
	}

	var body request
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Path = strings.TrimSpace(body.Path)
	body.SourceID = strings.TrimSpace(body.SourceID)
	if body.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	policyCommand := protocol.RoutedCommand{Command: "files.download"}
	if operation == protocol.FileTransferOperationUpload {
		policyCommand.Command = "files.upload"
	}
	policyBody, _ := json.Marshal(map[string]string{"path": body.Path, "source_id": body.SourceID})
	policyCommand.Body = policyBody
	if err := s.authorizeFileCommandPolicy(r.Context(), home.ID, policyCommand); err != nil {
		s.audit(r.Context(), "file_operation.denied", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "file_policy", operation, map[string]any{"reason": err.Error()})
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if operation == protocol.FileTransferOperationUpload && body.Size > 0 {
		maxUploadBytes, err := s.maxFileUploadBytes(r.Context(), home.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if maxUploadBytes > 0 && body.Size > maxUploadBytes {
			metadata := auditPathMetadata(body.SourceID, body.Path)
			metadata["operation"] = operation
			metadata["reason"] = "upload_size_limit"
			metadata["requested_size"] = body.Size
			s.audit(r.Context(), "file_transfer.setup_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "file_policy", operation, metadata)
			writeFileUploadLimitError(w, maxUploadBytes, body.Size)
			return
		}
	}

	agentConn, ok := s.router.ResolveAgent(home.ID, strings.TrimSpace(body.AgentID))
	if !ok {
		metadata := auditPathMetadata(body.SourceID, body.Path)
		metadata["operation"] = operation
		metadata["reason"] = "agent_offline"
		s.audit(r.Context(), "file_transfer.setup_failed", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "file_transfer", operation, metadata)
		http.Error(w, "target Hank Agent is offline", http.StatusBadGateway)
		return
	}

	now := time.Now().UTC()
	jobID := newID("filejob")
	if err := s.store.CreateFileOperationJob(r.Context(), store.FileOperationJob{
		ID:        jobID,
		AgentID:   agentConn.agent.ID,
		HomeID:    home.ID,
		UserID:    auth.User.ID,
		Operation: operation,
		SourceID:  body.SourceID,
		FromPath:  body.Path,
		Status:    "queued",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		s.logger.Warn("failed to persist file operation job", "job_id", jobID, "error", err)
	}

	transfer, rawToken := s.transfers.Create(home.ID, agentConn.agent.ID, jobID, operation, body.SourceID, body.Path, 10*time.Minute)
	if err := s.store.CreateFileTransfer(r.Context(), store.FileTransferRecord{
		ID:        transfer.ID,
		TokenHash: transfer.TokenHash,
		JobID:     jobID,
		HomeID:    home.ID,
		UserID:    auth.User.ID,
		AgentID:   agentConn.agent.ID,
		Operation: operation,
		SourceID:  body.SourceID,
		Path:      body.Path,
		Status:    "pending",
		CreatedAt: transfer.CreatedAt,
		ExpiresAt: transfer.ExpiresAt,
	}); err != nil {
		s.logger.Warn("failed to persist file transfer lease", "transfer_id", transfer.ID, "error", err)
	}
	requestMetadata := auditPathMetadata(body.SourceID, body.Path)
	requestMetadata["operation"] = operation
	requestMetadata["job_id"] = jobID
	s.audit(r.Context(), "file_transfer.requested", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "file_transfer", transfer.ID, requestMetadata)

	method := http.MethodGet
	if operation == protocol.FileTransferOperationUpload {
		method = http.MethodPut
	}

	payload := transfer.Snapshot()
	if body.Size > 0 {
		payload["requested_size"] = body.Size
	}
	payload["transfer_id"] = transfer.ID
	payload["job_id"] = jobID
	payload["transfer_token"] = rawToken
	payload["method"] = method
	payload["url"] = "/v1/file-transfers/" + transfer.ID
	if operation == protocol.FileTransferOperationDownload {
		setFileTransferCookie(w, r, transfer.ID, rawToken, transfer.ExpiresAt)
	}
	writeJSON(w, http.StatusCreated, payload)
}

type filePreviewRange struct {
	HasRange bool
	Start    int64
	End      int64
}

func (r filePreviewRange) Length() int64 {
	if !r.HasRange || r.End < r.Start {
		return 0
	}
	return r.End - r.Start + 1
}

func (s *Server) handleFilePreviewStream(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext) {
	sourceID := strings.TrimSpace(r.URL.Query().Get("source_id"))
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	targetAgentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	policyBody, _ := json.Marshal(map[string]string{"path": path, "source_id": sourceID})
	if err := s.authorizeFileCommandPolicy(r.Context(), home.ID, protocol.RoutedCommand{Command: "files.download", Body: policyBody}); err != nil {
		s.audit(r.Context(), "file_operation.denied", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "file_policy", "preview", map[string]any{"reason": err.Error()})
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	byteRange, err := parseFilePreviewRange(r.Header.Get("Range"))
	if err != nil {
		w.Header().Set("Content-Range", "bytes */*")
		http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
		return
	}

	agentConn, ok := s.router.ResolveAgent(home.ID, targetAgentID)
	if !ok {
		http.Error(w, "target Hank Agent is offline", http.StatusBadGateway)
		return
	}

	transfer, _ := s.transfers.Create(home.ID, agentConn.agent.ID, "", protocol.FileTransferOperationDownload, sourceID, path, 5*time.Minute)
	attempt, err := s.transfers.BeginAttempt(transfer, byteRange.Start, agentConn.replyBinding())
	if err != nil {
		s.writeTransferAttemptError(w, transfer, err)
		return
	}
	defer s.transfers.EndAttempt(attempt.ID)
	defer s.finishTransferAttempt(attempt)
	defer s.sendFileTransferCancel(agentConn, attempt.Session.HomeID, attempt.ID, "http_stream_closed")

	attempt.Length = byteRange.Length()
	open, err := protocol.NewEnvelope(protocol.TypeFileTransferOpen, attempt.ID, agentConn.agent.ID, home.ID, protocol.FileTransferOpen{
		Operation:   protocol.FileTransferOperationDownload,
		FlowControl: protocol.FileTransferFlowControlV1, WindowBytes: protocol.FileTransferWindowBytes,
		SourceID: sourceID,
		Path:     path,
		Offset:   byteRange.Start,
		Length:   byteRange.Length(),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := agentConn.peer.Write(r.Context(), open); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	ready, protocolErr, err := s.waitTransferReady(r.Context(), attempt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if protocolErr != nil {
		http.Error(w, protocolErr.Message, http.StatusBadGateway)
		return
	}
	transfer.MarkReady(ready)
	r, finishIO := bindTransferRequest(w, r, attempt)
	defer finishIO()
	if byteRange.HasRange && byteRange.End < 0 {
		byteRange.End = ready.Size - 1
	}
	if byteRange.HasRange && (byteRange.Start >= ready.Size || byteRange.End >= ready.Size) {
		w.Header().Set("Content-Range", "bytes */"+int64ToString(ready.Size))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	contentLength := ready.Size - byteRange.Start
	status := http.StatusOK
	if byteRange.HasRange {
		status = http.StatusPartialContent
		contentLength = byteRange.End - byteRange.Start + 1
		w.Header().Set("Content-Range", "bytes "+int64ToString(byteRange.Start)+"-"+int64ToString(byteRange.End)+"/"+int64ToString(ready.Size))
	}
	contentType := previewContentType(path)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)
	if policy := previewContentSecurityPolicy(contentType); policy != "" {
		w.Header().Set("Content-Security-Policy", policy)
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(path)}))
	if contentLength >= 0 {
		w.Header().Set("Content-Length", int64ToString(contentLength))
	}
	w.WriteHeader(status)

	flusher, _ := w.(http.Flusher)
	currentOffset := byteRange.Start
	remaining := contentLength
	for remaining > 0 {
		select {
		case <-r.Context().Done():
			s.sendFileTransferCancel(agentConn, home.ID, attempt.ID, "http_request_cancelled")
			return
		case <-attempt.done:
			return
		case frame := <-attempt.DataCh:
			if frame.Error != nil {
				return
			}
			if frame.Offset != currentOffset {
				return
			}
			if len(frame.Data) == 0 {
				continue
			}
			data := frame.Data
			if int64(len(data)) > remaining {
				data = data[:int(remaining)]
			}
			n, err := w.Write(data)
			currentOffset += int64(n)
			remaining -= int64(n)
			if err != nil || n != len(data) {
				return
			}
			if err := s.acknowledgeTransfer(r.Context(), attempt, currentOffset); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case result := <-attempt.CompleteCh:
			if result.Error != nil {
				return
			}
			if remaining > 0 && result.Complete.Offset > currentOffset {
				continue
			}
		}
	}
}

func parseFilePreviewRange(header string) (filePreviewRange, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return filePreviewRange{}, nil
	}
	if !strings.HasPrefix(header, "bytes=") {
		return filePreviewRange{}, errors.New("unsupported range unit")
	}
	spec := strings.TrimPrefix(header, "bytes=")
	if strings.Contains(spec, ",") {
		return filePreviewRange{}, errors.New("multiple ranges are not supported")
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return filePreviewRange{}, errors.New("range start is required")
	}
	start, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || start < 0 {
		return filePreviewRange{}, errors.New("range start is invalid")
	}
	end := int64(-1)
	if strings.TrimSpace(parts[1]) != "" {
		end, err = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || end < start {
			return filePreviewRange{}, errors.New("range end is invalid")
		}
	}
	return filePreviewRange{HasRange: true, Start: start, End: end}, nil
}

func previewContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".apng":
		return "image/apng"
	case ".avif":
		return "image/avif"
	case ".bmp":
		return "image/bmp"
	case ".gif":
		return "image/gif"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".webp":
		return "image/webp"
	case ".m4v", ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".mov":
		return "video/quicktime"
	case ".mpeg", ".mpg":
		return "video/mpeg"
	case ".ogv":
		return "video/ogg"
	case ".webm":
		return "video/webm"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".oga", ".ogg", ".opus":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".weba":
		return "audio/webm"
	case ".pdf":
		return "application/pdf"
	case ".htm", ".html":
		return "text/html; charset=utf-8"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	default:
		return "application/octet-stream"
	}
}

func previewContentSecurityPolicy(contentType string) string {
	if strings.HasPrefix(contentType, "text/html") {
		return "sandbox"
	}
	return ""
}

func (s *Server) sendFileTransferCancel(agentConn *agentConnection, homeID string, transferID string, reason string) {
	if agentConn == nil || agentConn.peer == nil || transferID == "" {
		return
	}
	cancel, err := protocol.NewEnvelope(protocol.TypeFileTransferCancel, transferID, agentConn.agent.ID, homeID, protocol.FileTransferCancel{Reason: reason})
	if err != nil {
		return
	}
	ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	if err := agentConn.peer.Write(ctx, cancel); err != nil {
		s.logger.Debug("failed to send file transfer cancel", "transfer_id", transferID, "error", err)
	}
}

func (s *Server) handleFileTransfer(w http.ResponseWriter, r *http.Request) {
	transferID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/file-transfers/"), "/")
	statusOnly := false
	if strings.HasSuffix(transferID, "/status") {
		statusOnly = true
		transferID = strings.TrimSuffix(transferID, "/status")
	}
	if transferID == "" {
		http.NotFound(w, r)
		return
	}

	rawToken := ""
	if headerToken, err := bearerToken(r.Header.Get("Authorization")); err == nil {
		rawToken = headerToken
	} else if r.Method == http.MethodGet {
		rawToken = fileTransferTokenFromCookie(r, transferID)
	}
	if rawToken == "" {
		http.Error(w, "transfer token is required", http.StatusUnauthorized)
		return
	}

	if statusOnly {
		record, err := s.store.GetFileTransfer(r.Context(), transferID)
		if err != nil || record.TokenHash != hashToken(rawToken) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"transfer_id":  record.ID,
			"operation":    record.Operation,
			"source_id":    record.SourceID,
			"path":         record.Path,
			"status":       record.Status,
			"bytes_total":  record.BytesTotal,
			"bytes_done":   record.BytesDone,
			"created_at":   record.CreatedAt,
			"expires_at":   record.ExpiresAt,
			"completed_at": record.CompletedAt,
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		transfer, err := s.authorizeTransfer(r.Context(), transferID, rawToken, protocol.FileTransferOperationDownload)
		if err != nil {
			http.Error(w, "transfer not found", http.StatusNotFound)
			return
		}
		offset, err := offsetParam(r, 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		agentConn, ok := s.router.ResolveAgent(transfer.HomeID, transfer.AgentID)
		if !ok {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "agent_offline", auditSeverityWarning)
			http.Error(w, "target Hank Agent is offline", http.StatusBadGateway)
			return
		}

		attempt, err := s.transfers.BeginAttempt(transfer, offset, agentConn.replyBinding())
		if err != nil {
			s.writeTransferAttemptError(w, transfer, err)
			return
		}
		s.persistTransferStatus(r.Context(), transfer, "active")
		defer s.transfers.EndAttempt(attempt.ID)
		defer s.finishTransferAttempt(attempt)
		defer s.sendFileTransferCancel(agentConn, attempt.Session.HomeID, attempt.ID, "http_stream_closed")

		open, err := protocol.NewEnvelope(protocol.TypeFileTransferOpen, attempt.ID, agentConn.agent.ID, transfer.HomeID, protocol.FileTransferOpen{
			Operation:   protocol.FileTransferOperationDownload,
			FlowControl: protocol.FileTransferFlowControlV1, WindowBytes: protocol.FileTransferWindowBytes,
			SourceID: transfer.SourceID,
			Path:     transfer.Path,
			Offset:   offset,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := agentConn.peer.Write(r.Context(), open); err != nil {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "agent_write_failed", auditSeverityWarning)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		ready, protocolErr, err := s.waitTransferReady(r.Context(), attempt)
		if err != nil {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "ready_timeout", auditSeverityWarning)
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		if protocolErr != nil {
			transfer.Fail(protocolErr)
			s.persistTransferStatus(r.Context(), transfer, "failed")
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, protocolErr.Code, auditSeverityWarning)
			http.Error(w, protocolErr.Message, http.StatusBadGateway)
			return
		}
		transfer.MarkReady(ready)
		r, finishIO := bindTransferRequest(w, r, attempt)
		defer finishIO()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(transfer.Path)}))
		if remaining := ready.Size - offset; remaining >= 0 {
			w.Header().Set("Content-Length", int64ToString(remaining))
		}

		flusher, _ := w.(http.Flusher)
		currentOffset := offset
		var pendingComplete *protocol.FileTransferComplete
		for {
			select {
			case <-r.Context().Done():
				s.sendFileTransferCancel(agentConn, transfer.HomeID, attempt.ID, "http_request_cancelled")
				transfer.Advance(currentOffset, ready.Size)
				return
			case <-attempt.done:
				return
			case frame := <-attempt.DataCh:
				if frame.Error != nil {
					transfer.Fail(frame.Error)
					s.persistTransferStatus(r.Context(), transfer, "failed")
					s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, frame.Error.Code, auditSeverityWarning)
					return
				}
				if frame.Offset != currentOffset {
					transfer.Fail(&protocol.ErrorPayload{Code: "transfer_offset_mismatch", Message: "download stream offset mismatch"})
					s.persistTransferStatus(r.Context(), transfer, "failed")
					s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "transfer_offset_mismatch", auditSeverityWarning)
					return
				}
				if len(frame.Data) == 0 {
					continue
				}
				n, err := w.Write(frame.Data)
				currentOffset += int64(n)
				if err != nil || n != len(frame.Data) {
					transfer.Advance(currentOffset, ready.Size)
					return
				}
				if err := s.acknowledgeTransfer(r.Context(), attempt, currentOffset); err != nil {
					return
				}
				transfer.Advance(currentOffset, ready.Size)
				if pendingComplete != nil && currentOffset >= pendingComplete.Offset {
					transfer.Complete(*pendingComplete)
					s.persistTransferStatus(r.Context(), transfer, "completed")
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			case result := <-attempt.CompleteCh:
				protocolErr := result.Error
				if protocolErr != nil {
					transfer.Fail(protocolErr)
					s.persistTransferStatus(r.Context(), transfer, "failed")
					s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, protocolErr.Code, auditSeverityWarning)
					return
				}
				complete := result.Complete
				if currentOffset >= complete.Offset {
					transfer.Complete(complete)
					s.persistTransferStatus(r.Context(), transfer, "completed")
					return
				}
				pendingComplete = &complete
			}
		}

	case http.MethodPut:
		transfer, err := s.authorizeTransfer(r.Context(), transferID, rawToken, protocol.FileTransferOperationUpload)
		if err != nil {
			http.Error(w, "transfer not found", http.StatusNotFound)
			return
		}
		maxUploadBytes, err := s.maxFileUploadBytes(r.Context(), transfer.HomeID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		offset, err := offsetParam(r, transfer.NextOffset())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if maxUploadBytes > 0 && r.ContentLength > 0 && offset+r.ContentLength > maxUploadBytes {
			transfer.Fail(&protocol.ErrorPayload{Code: "upload_too_large", Message: "file source policy upload size limit exceeded"})
			s.persistTransferStatus(r.Context(), transfer, "failed")
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "upload_too_large", auditSeverityWarning)
			writeFileUploadLimitError(w, maxUploadBytes, offset+r.ContentLength)
			return
		}
		agentConn, ok := s.router.ResolveAgent(transfer.HomeID, transfer.AgentID)
		if !ok {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "agent_offline", auditSeverityWarning)
			http.Error(w, "target Hank Agent is offline", http.StatusBadGateway)
			return
		}

		attempt, err := s.transfers.BeginAttempt(transfer, offset, agentConn.replyBinding())
		if err != nil {
			s.writeTransferAttemptError(w, transfer, err)
			return
		}
		s.persistTransferStatus(r.Context(), transfer, "active")
		defer s.transfers.EndAttempt(attempt.ID)
		defer s.finishTransferAttempt(attempt)
		defer s.sendFileTransferCancel(agentConn, attempt.Session.HomeID, attempt.ID, "http_stream_closed")

		open, err := protocol.NewEnvelope(protocol.TypeFileTransferOpen, attempt.ID, agentConn.agent.ID, transfer.HomeID, protocol.FileTransferOpen{
			Operation: protocol.FileTransferOperationUpload,
			SourceID:  transfer.SourceID,
			Path:      transfer.Path,
			Offset:    offset,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := agentConn.peer.Write(r.Context(), open); err != nil {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "agent_write_failed", auditSeverityWarning)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		ready, protocolErr, err := s.waitTransferReady(r.Context(), attempt)
		if err != nil {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "ready_timeout", auditSeverityWarning)
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		if protocolErr != nil {
			transfer.Fail(protocolErr)
			s.persistTransferStatus(r.Context(), transfer, "failed")
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, protocolErr.Code, auditSeverityWarning)
			http.Error(w, protocolErr.Message, http.StatusBadGateway)
			return
		}
		transfer.MarkReady(ready)
		r, finishIO := bindTransferRequest(w, r, attempt)
		defer finishIO()
		if ready.Offset != offset {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":       "transfer_offset_mismatch",
				"next_offset": ready.Offset,
				"size":        ready.Size,
			})
			return
		}

		buffer := make([]byte, 32*1024)
		currentOffset := offset
		for {
			n, err := r.Body.Read(buffer)
			if n > 0 {
				if maxUploadBytes > 0 && currentOffset+int64(n) > maxUploadBytes {
					transfer.Fail(&protocol.ErrorPayload{Code: "upload_too_large", Message: "file source policy upload size limit exceeded"})
					s.persistTransferStatus(r.Context(), transfer, "failed")
					s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "upload_too_large", auditSeverityWarning)
					writeFileUploadLimitError(w, maxUploadBytes, currentOffset+int64(n))
					return
				}
				envelope, envelopeErr := protocol.NewEnvelope(protocol.TypeFileTransferData, attempt.ID, agentConn.agent.ID, transfer.HomeID, protocol.FileTransferChunk{
					Offset:        currentOffset,
					ContentBase64: base64.StdEncoding.EncodeToString(buffer[:n]),
				})
				if envelopeErr != nil {
					http.Error(w, envelopeErr.Error(), http.StatusInternalServerError)
					return
				}
				if err := agentConn.peer.Write(r.Context(), envelope); err != nil {
					s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "agent_write_failed", auditSeverityWarning)
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				currentOffset += int64(n)
				transfer.Advance(currentOffset, currentOffset)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "request_body_read_failed", auditSeverityWarning)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		complete, err := protocol.NewEnvelope(protocol.TypeFileTransferComplete, attempt.ID, agentConn.agent.ID, transfer.HomeID, protocol.FileTransferComplete{
			Operation: protocol.FileTransferOperationUpload,
			SourceID:  transfer.SourceID,
			Path:      transfer.Path,
			Offset:    currentOffset,
			Size:      currentOffset,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := agentConn.peer.Write(r.Context(), complete); err != nil {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "agent_write_failed", auditSeverityWarning)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		done, protocolErr, err := s.waitTransferComplete(r.Context(), attempt)
		if err != nil {
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, "complete_timeout", auditSeverityWarning)
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		if protocolErr != nil {
			transfer.Fail(protocolErr)
			s.persistTransferStatus(r.Context(), transfer, "failed")
			s.auditFileTransfer(r.Context(), "file_transfer.failed", transfer, protocolErr.Code, auditSeverityWarning)
			http.Error(w, protocolErr.Message, http.StatusBadGateway)
			return
		}
		transfer.Complete(done)
		s.persistTransferStatus(r.Context(), transfer, "completed")

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"path":        done.Path,
			"size":        done.Size,
			"next_offset": done.Offset,
			"resumable":   true,
		})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) authorizeTransfer(ctx context.Context, transferID string, rawToken string, operation string) (*transferSession, error) {
	s.transferControlMu.Lock()
	defer s.transferControlMu.Unlock()
	transfer, err := s.transfers.Authorize(transferID, rawToken, operation)
	record, recordErr := s.store.GetFileTransfer(ctx, transferID)
	if recordErr != nil {
		return nil, ErrTransferNotFound
	}
	if record.Operation != operation || record.TokenHash != hashToken(rawToken) || !record.ExpiresAt.After(time.Now().UTC()) {
		return nil, ErrTransferNotFound
	}
	if record.Status == "expired" || record.Status == "failed" {
		return nil, ErrTransferNotFound
	}
	if err == nil {
		return transfer, nil
	}
	transfer = &transferSession{
		ID:          record.ID,
		HomeID:      record.HomeID,
		AgentID:     record.AgentID,
		JobID:       record.JobID,
		Operation:   record.Operation,
		SourceID:    record.SourceID,
		Path:        record.Path,
		TokenHash:   record.TokenHash,
		CreatedAt:   record.CreatedAt,
		ExpiresAt:   record.ExpiresAt,
		nextOffset:  record.BytesDone,
		completedAt: record.CompletedAt,
	}
	if record.BytesTotal != nil {
		transfer.size = *record.BytesTotal
	}
	s.transfers.Restore(transfer)
	return transfer, nil
}

func (s *Server) persistTransferStatus(ctx context.Context, transfer *transferSession, status string) {
	if transfer == nil {
		return
	}
	size, done, completedAt := transfer.Progress()
	var total *int64
	if size > 0 {
		total = &size
	}
	if err := s.store.UpdateFileTransferProgress(ctx, transfer.ID, status, total, done, completedAt); err != nil {
		s.logger.Warn("failed to persist file transfer status", "transfer_id", transfer.ID, "status", status, "error", err)
	}
	s.persistTransferJobStatus(ctx, transfer, status, done, completedAt)
}

func (s *Server) auditFileTransfer(ctx context.Context, eventType string, transfer *transferSession, reason string, severity string) {
	if transfer == nil {
		return
	}
	metadata := auditPathMetadata(transfer.SourceID, transfer.Path)
	metadata["operation"] = transfer.Operation
	if strings.TrimSpace(reason) != "" {
		metadata["reason"] = strings.TrimSpace(reason)
	}
	if transfer.JobID != "" {
		metadata["job_id"] = transfer.JobID
	}
	record, err := s.store.GetFileTransfer(ctx, transfer.ID)
	actorUserID := ""
	homeID := transfer.HomeID
	agentID := transfer.AgentID
	if err == nil {
		actorUserID = record.UserID
		homeID = record.HomeID
		agentID = record.AgentID
	}
	s.audit(ctx, eventType, severity, actorUserID, agentID, homeID, "", "file_transfer", transfer.ID, metadata)
}

func (s *Server) persistTransferJobStatus(ctx context.Context, transfer *transferSession, transferStatus string, bytesDone int64, completedAt *time.Time) {
	if transfer == nil || transfer.JobID == "" {
		return
	}
	jobStatus := "running"
	filesDone := int64(0)
	errorMessage := ""
	var jobCompletedAt *time.Time
	switch transferStatus {
	case "active":
		jobStatus = "running"
	case "completed":
		jobStatus = "completed"
		filesDone = 1
		if completedAt != nil {
			jobCompletedAt = completedAt
		} else {
			now := time.Now().UTC()
			jobCompletedAt = &now
		}
	case "failed", "expired":
		jobStatus = "failed"
		if lastError := transfer.LastError(); lastError != nil {
			errorMessage = lastError.Message
		}
		now := time.Now().UTC()
		jobCompletedAt = &now
	default:
		return
	}
	if _, err := s.store.UpdateFileOperationJobMonotonic(ctx, transfer.JobID, jobStatus, bytesDone, filesDone, errorMessage, jobCompletedAt); err != nil {
		s.logger.Warn("failed to persist file operation job status", "job_id", transfer.JobID, "transfer_id", transfer.ID, "status", jobStatus, "error", err)
	}
}

func (s *Server) handleTransferReady(sender agentReplyBinding, envelope protocol.Envelope) {
	attempt, ok := s.transfers.GetAttempt(sender, envelope)
	if !ok {
		return
	}
	if envelope.Error != nil {
		attempt.deliverReady(transferReadyResult{Error: envelope.Error})
		return
	}
	ready, err := protocol.DecodePayload[protocol.FileTransferReady](envelope)
	if err != nil {
		attempt.deliverReady(transferReadyResult{Error: &protocol.ErrorPayload{Code: "invalid_transfer_ready", Message: err.Error()}})
		return
	}
	attempt.wireMu.Lock()
	if attempt.negotiated {
		attempt.wireMu.Unlock()
		return
	}
	if attempt.Session.Operation == protocol.FileTransferOperationDownload {
		if ready.FlowControl != protocol.FileTransferFlowControlV1 || ready.WindowBytes != protocol.FileTransferWindowBytes {
			attempt.wireMu.Unlock()
			attempt.abort(&protocol.ErrorPayload{Code: "agent_update_required", Message: "Update this Hank Agent to enable flow-controlled downloads"})
			return
		}
		if ready.Operation != attempt.Session.Operation || ready.Offset != attempt.Offset || ready.Size < attempt.Offset {
			attempt.wireMu.Unlock()
			attempt.abort(&protocol.ErrorPayload{Code: "invalid_transfer_ready", Message: "invalid download stream bounds"})
			return
		}
		attempt.expectedEnd = ready.Size
		if attempt.Length > 0 && attempt.Length < ready.Size-attempt.Offset {
			attempt.expectedEnd = attempt.Offset + attempt.Length
		}
	}
	attempt.negotiated = true
	attempt.wireMu.Unlock()
	attempt.deliverReady(transferReadyResult{Ready: ready})
}

func (s *Server) handleTransferData(sender agentReplyBinding, envelope protocol.Envelope) {
	attempt, ok := s.transfers.GetAttempt(sender, envelope)
	if !ok {
		return
	}
	chunk, err := protocol.DecodePayload[protocol.FileTransferChunk](envelope)
	if err != nil {
		attempt.deliverData(transferDataFrame{Error: &protocol.ErrorPayload{Code: "invalid_transfer_chunk", Message: err.Error()}})
		return
	}
	if len(chunk.ContentBase64) > base64.StdEncoding.EncodedLen(protocol.FileTransferMaxChunkBytes) {
		attempt.abort(&protocol.ErrorPayload{Code: "invalid_transfer_chunk", Message: "download chunk is too large"})
		return
	}
	data, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil {
		attempt.deliverData(transferDataFrame{Error: &protocol.ErrorPayload{Code: "invalid_transfer_chunk", Message: err.Error()}})
		return
	}
	attempt.deliverData(transferDataFrame{Offset: chunk.Offset, Data: data})
}

func (s *Server) handleTransferComplete(sender agentReplyBinding, envelope protocol.Envelope) {
	attempt, ok := s.transfers.GetAttempt(sender, envelope)
	if !ok {
		return
	}
	if envelope.Error != nil {
		attempt.deliverComplete(transferCompleteResult{Error: envelope.Error})
		return
	}
	complete, err := protocol.DecodePayload[protocol.FileTransferComplete](envelope)
	if err != nil {
		attempt.deliverComplete(transferCompleteResult{Error: &protocol.ErrorPayload{Code: "invalid_transfer_complete", Message: err.Error()}})
		return
	}
	attempt.wireMu.Lock()
	invalid := attempt.Session.Operation == protocol.FileTransferOperationDownload && (!attempt.negotiated || complete.Operation != attempt.Session.Operation || complete.Offset != attempt.expectedEnd || complete.Offset != attempt.received)
	attempt.wireMu.Unlock()
	if invalid {
		attempt.abort(&protocol.ErrorPayload{Code: "invalid_transfer_complete", Message: "download ended before its advertised bytes arrived"})
		return
	}
	attempt.deliverComplete(transferCompleteResult{Complete: complete})
}

func (s *Server) handleTransferError(sender agentReplyBinding, envelope protocol.Envelope) {
	attempt, ok := s.transfers.GetAttempt(sender, envelope)
	if !ok {
		return
	}
	if envelope.Error == nil {
		attempt.deliverReady(transferReadyResult{Error: &protocol.ErrorPayload{Code: "transfer_failed", Message: "unknown transfer failure"}})
		attempt.deliverComplete(transferCompleteResult{Error: &protocol.ErrorPayload{Code: "transfer_failed", Message: "unknown transfer failure"}})
		return
	}
	attempt.deliverReady(transferReadyResult{Error: envelope.Error})
	attempt.deliverComplete(transferCompleteResult{Error: envelope.Error})
}

func offsetParam(r *http.Request, fallback int64) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("offset"))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("offset must be a non-negative integer")
	}
	return value, nil
}

func (s *Server) waitTransferReady(ctx context.Context, attempt *transferAttempt) (protocol.FileTransferReady, *protocol.ErrorPayload, error) {
	select {
	case <-ctx.Done():
		return protocol.FileTransferReady{}, nil, ctx.Err()
	case <-attempt.done:
		return protocol.FileTransferReady{}, attempt.failureResult(), nil
	case result := <-attempt.ReadyCh:
		return result.Ready, result.Error, nil
	}
}

func (s *Server) waitTransferComplete(ctx context.Context, attempt *transferAttempt) (protocol.FileTransferComplete, *protocol.ErrorPayload, error) {
	select {
	case <-ctx.Done():
		return protocol.FileTransferComplete{}, nil, ctx.Err()
	case <-attempt.done:
		return protocol.FileTransferComplete{}, attempt.failureResult(), nil
	case result := <-attempt.CompleteCh:
		return result.Complete, result.Error, nil
	}
}

func (s *Server) writeTransferAttemptError(w http.ResponseWriter, transfer *transferSession, err error) {
	switch {
	case errors.Is(err, ErrTransferBusy):
		http.Error(w, "transfer is already active", http.StatusConflict)
	case errors.Is(err, ErrTransferOffsetInvalid):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":       "transfer_offset_mismatch",
			"next_offset": transfer.NextOffset(),
		})
	case errors.Is(err, ErrTransferNotFound):
		http.Error(w, "transfer not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Return credit only for bytes successfully consumed by the HTTP writer.
func (s *Server) acknowledgeTransfer(ctx context.Context, attempt *transferAttempt, offset int64) error {
	attempt.wireMu.Lock()
	if offset < attempt.acknowledged || offset > attempt.received {
		attempt.wireMu.Unlock()
		return errors.New("invalid transfer acknowledgement")
	}
	attempt.acknowledged = offset
	attempt.wireMu.Unlock()
	ack, err := protocol.NewEnvelope(protocol.TypeFileTransferAck, attempt.ID, attempt.target.agentID, attempt.target.homeID, protocol.FileTransferAck{Offset: offset})
	if err != nil {
		return err
	}
	return attempt.target.peer.Write(ctx, ack)
}

func bindTransferRequest(w http.ResponseWriter, r *http.Request, attempt *transferAttempt) (*http.Request, func()) {
	ctx, cancel := context.WithCancel(r.Context())
	stop, exited := make(chan struct{}), make(chan struct{})
	controller := http.NewResponseController(w)
	go func() {
		defer close(exited)
		select {
		case <-stop:
			return
		case <-attempt.done:
			cancel()
			_ = controller.SetReadDeadline(time.Now())
			_ = controller.SetWriteDeadline(time.Now())
		}
	}()
	return r.WithContext(ctx), func() { close(stop); <-exited; cancel() }
}

// Preserve acknowledged progress even when the browser's context is gone.
func (s *Server) finishTransferAttempt(attempt *transferAttempt) {
	if attempt.Session.JobID == "" {
		return
	}
	attempt.wireMu.Lock()
	failure := attempt.failure
	attempt.wireMu.Unlock()
	if failure != nil {
		attempt.Session.Fail(failure)
	}
	_, _, completedAt := attempt.Session.Progress()
	status := "active"
	if completedAt != nil {
		status = "completed"
	} else if attempt.Session.LastError() != nil {
		status = "failed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.persistTransferStatus(ctx, attempt.Session, status)
	if status == "completed" && attempt.Session.Operation == protocol.FileTransferOperationUpload {
		if err := s.store.MarkFileSearchDirectoryDue(ctx, attempt.Session.HomeID, attempt.Session.AgentID, attempt.Session.SourceID, fileSearchParent(attempt.Session.Path)); err != nil {
			s.logger.Warn("failed to schedule file search refresh after upload", "agent_id", attempt.Session.AgentID, "error", err)
		}
	}
}
