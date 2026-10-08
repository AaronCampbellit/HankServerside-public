package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/dropfile/HankServerside/internal/agent/operations"
	"github.com/dropfile/HankServerside/internal/protocol"
	"io"
	"os"
	"time"
)

// SetOperationJournal must be called before Run; the caller owns its lifetime.
func (c *Client) SetOperationJournal(journal *operations.Journal) { c.operationJournal = journal }

func strictOperationJSON(raw json.RawMessage, target any) error {
	if len(raw) > 512<<10 {
		return operations.ErrConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return operations.ErrConflict
	}
	return nil
}

func (c *Client) handleAssistantOperation(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope, command protocol.RoutedCommand) error {
	receipt, err := c.executeAssistantOperation(ctx, envelope, command)
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "operation_unavailable", "Operation identity or durable receipt could not be verified.", nil)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return c.writeJSON(ctx, conn, protocol.Envelope{Version: protocol.Version, Type: protocol.TypeCloudResponse, RequestID: envelope.RequestID, HomeID: envelope.HomeID, AgentID: c.agentID, Timestamp: time.Now().UTC(), Payload: raw})
}

func (c *Client) executeAssistantOperation(ctx context.Context, envelope protocol.Envelope, command protocol.RoutedCommand) (protocol.AssistantOperationStatusResponse, error) {
	var request protocol.AssistantOperationRequest
	var empty protocol.AssistantOperationStatusResponse
	if err := strictOperationJSON(command.Body, &request); err != nil {
		return empty, err
	}
	identity := request.Identity
	if c.operationJournal == nil || c.registeredHomeID == "" || identity.HomeID != c.registeredHomeID || envelope.HomeID != identity.HomeID || identity.AgentID != c.agentID || envelope.AgentID != c.agentID || identity.DeviceID != "" || identity.JournalEpoch != c.operationJournal.Epoch() {
		return empty, operations.ErrConflict
	}
	if command.Command == protocol.CommandAssistantOperationStatus {
		receipt, err := c.operationJournal.Lookup(identity)
		if errors.Is(err, os.ErrNotExist) {
			return protocol.AssistantOperationStatusResponse{Identity: identity, Outcome: "not_found"}, nil
		}
		return receipt, err
	}
	if command.Command == protocol.CommandAssistantOperationExecute && identity.Tool == "machines.service_action" && identity.ToolVersion == 1 {
		return c.executeAssistantServiceOperation(ctx, request)
	}
	if command.Command == protocol.CommandAssistantOperationExecute && identity.Tool == "homeassistant.call_service" && identity.ToolVersion == 1 {
		return c.executeAssistantHAOperation(ctx, request)
	}
	if command.Command != protocol.CommandAssistantOperationExecute || (identity.Tool != "files.create_folder" && identity.Tool != "files.upload") || identity.ToolVersion != 1 {
		return empty, operations.ErrConflict
	}
	var args protocol.AssistantUploadOperationArguments
	if err := strictOperationJSON(request.Arguments, &args); err != nil {
		return empty, err
	}
	if args.Path == "" || args.SourceID == "" {
		return empty, operations.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	var staged *os.File
	if identity.Tool == "files.upload" {
		// A known receipt takes precedence over staging availability after restart.
		if saved, err := c.operationJournal.Lookup(identity); err == nil {
			return saved, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return empty, err
		}
		var err error
		staged, err = c.operationJournal.OpenStage(identity, args.ChecksumSHA256, args.SizeBytes)
		if err != nil {
			return empty, err
		}
		defer staged.Close()
	}
	receipt, fresh, err := c.operationJournal.Begin(identity)
	if err != nil || !fresh {
		return receipt, err
	}
	result := protocol.AssistantOperationResult{Verification: "unknown"}
	if identity.Tool == "files.upload" {
		err = c.dispatcher.files.UploadExclusiveSource(ctx, args.SourceID, args.Path, args.SizeBytes, staged)
	} else {
		err = c.dispatcher.files.CreateDirectoryExclusiveSource(ctx, args.SourceID, args.Path)
	}
	if err == nil {
		item, readErr := c.dispatcher.files.StatSource(ctx, args.SourceID, args.Path)
		verified := readErr == nil && item.IsDirectory && identity.Tool == "files.create_folder"
		if readErr == nil && !item.IsDirectory && identity.Tool == "files.upload" {
			reader, _, err := c.dispatcher.files.OpenReaderSource(ctx, args.SourceID, args.Path, 0)
			if err == nil {
				hash := sha256.New()
				n, copyErr := io.Copy(hash, io.LimitReader(reader, args.SizeBytes+1))
				closeErr := reader.Close()
				verified = copyErr == nil && closeErr == nil && n == args.SizeBytes && hex.EncodeToString(hash.Sum(nil)) == args.ChecksumSHA256
			}
		}
		if verified {
			receipt.Outcome = "confirmed"
			result.Verification = "observed"
			result.Item = &item
		} else {
			result.Code = "verification_unavailable"
		}
	} else if errors.Is(err, os.ErrExist) {
		receipt.Outcome = "failed"
		result.Code = "revision_conflict"
		result.Verification = "not_attempted"
	} else {
		// A transport or filesystem failure may follow the effect. Never replay.
		result.Code = "outcome_unknown"
	}
	receipt.Result, _ = json.Marshal(result)
	if err := c.operationJournal.Complete(receipt); err != nil {
		return empty, err
	}
	return receipt, nil
}

func (c *Client) handleAssistantStage(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope, command protocol.RoutedCommand) error {
	var request protocol.AssistantOperationStageRequest
	if err := strictOperationJSON(command.Body, &request); err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "invalid_arguments", "Invalid staged transfer.", nil)
	}
	identity := request.Identity
	if c.operationJournal == nil || c.registeredHomeID == "" || identity.HomeID != c.registeredHomeID || envelope.HomeID != identity.HomeID || identity.AgentID != c.agentID || envelope.AgentID != c.agentID || identity.DeviceID != "" || identity.JournalEpoch != c.operationJournal.Epoch() || identity.Tool != "files.upload" || identity.ToolVersion != 1 {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "permission_denied", "Invalid operation identity.", nil)
	}
	next, err := c.operationJournal.StageChunk(identity, request.ChecksumSHA256, request.SizeBytes, request.Offset, request.Data)
	if err != nil {
		return c.writeError(ctx, conn, envelope.RequestID, envelope.HomeID, "stage_unavailable", "Staged bytes could not be verified.", nil)
	}
	raw, _ := json.Marshal(protocol.AssistantOperationStageResponse{Identity: identity, NextOffset: next})
	return c.writeJSON(ctx, conn, protocol.Envelope{Version: protocol.Version, Type: protocol.TypeCloudResponse, RequestID: envelope.RequestID, HomeID: envelope.HomeID, AgentID: c.agentID, Timestamp: time.Now().UTC(), Payload: raw})
}
