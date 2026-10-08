package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	agentapps "github.com/dropfile/HankServerside/internal/agent/apps"
	agentfiles "github.com/dropfile/HankServerside/internal/agent/files"
	agentha "github.com/dropfile/HankServerside/internal/agent/homeassistant"
	agentmcpcontext "github.com/dropfile/HankServerside/internal/agent/mcpcontext"
	agentnotes "github.com/dropfile/HankServerside/internal/agent/notes"
	"github.com/dropfile/HankServerside/internal/protocol"
)

type commandDispatcher struct {
	ha                    *agentha.Client
	files                 *agentfiles.Service
	fileSourceRevisionKey string
	mcpctx                *agentmcpcontext.Service
	notes                 *agentnotes.Service
	apps                  *agentapps.Manager
	config                *configManager
	host                  *hostService
	terminals             *terminalManager
}

func (d *commandDispatcher) dispatch(ctx context.Context, command protocol.RoutedCommand) (any, *protocol.ErrorPayload) {
	switch command.Command {
	case protocol.CommandSystemPing:
		request, err := decodeBody[protocol.SystemPingRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_ping_request", err)
		}
		message := "pong"
		if request.Message != "" {
			message = "pong: " + request.Message
		}
		return protocol.SystemPingResponse{
			Message: message,
			Time:    time.Now().UTC(),
		}, nil

	case "host.status":
		return d.host.status(), nil
	case "host.lock":
		if err := d.host.lock(ctx); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil
	case "wol.send":
		request, err := decodeBody[struct {
			MAC       string `json:"mac"`
			Broadcast string `json:"broadcast,omitempty"`
		}](command.Body)
		if err != nil {
			return nil, badRequest("invalid_wol_request", err)
		}
		if err := d.host.wake(request.MAC, request.Broadcast); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil
	case "shell.exec":
		request, err := decodeBody[struct {
			Command        string  `json:"command"`
			TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
		}](command.Body)
		if err != nil {
			return nil, badRequest("invalid_shell_request", err)
		}
		result, err := d.host.exec(ctx, request.Command, time.Duration(request.TimeoutSeconds*float64(time.Second)))
		if err != nil {
			return nil, mapError(err)
		}
		return result, nil
	case protocol.CommandShellSessionOpen:
		request, err := decodeBody[protocol.ShellSessionOpenRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_shell_request", err)
		}
		result, err := d.terminals.open(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return result, nil
	case protocol.CommandShellSessionInput:
		request, err := decodeBody[protocol.ShellSessionInputRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_shell_request", err)
		}
		if err := d.terminals.input(request); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil
	case protocol.CommandShellSessionResize:
		request, err := decodeBody[protocol.ShellSessionResizeRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_shell_request", err)
		}
		if err := d.terminals.resize(request); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil
	case protocol.CommandShellSessionAttach:
		request, err := decodeBody[protocol.ShellSessionAttachRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_shell_request", err)
		}
		result, err := d.terminals.attach(request)
		if err != nil {
			return nil, mapError(err)
		}
		return result, nil
	case protocol.CommandShellSessionClose:
		request, err := decodeBody[protocol.ShellSessionCloseRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_shell_request", err)
		}
		if err := d.terminals.close(request); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil

	case "homeassistant.health":
		if err := d.ha.Health(ctx); err != nil {
			return nil, mapError(err)
		}
		return protocol.HomeAssistantHealthResponse{OK: true, CheckedAt: time.Now().UTC()}, nil

	case "homeassistant.fetch_states":
		states, err := d.ha.FetchStates(ctx)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.HomeAssistantFetchStatesResponse{States: states}, nil

	case "homeassistant.fetch_state":
		request, err := decodeBody[protocol.HomeAssistantFetchStateRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_homeassistant_request", err)
		}
		state, err := d.ha.FetchState(ctx, request.EntityID)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.HomeAssistantFetchStateResponse{State: state}, nil

	case "homeassistant.call_service":
		request, err := decodeBody[protocol.HomeAssistantCallServiceRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_homeassistant_request", err)
		}
		result, err := d.ha.CallService(ctx, request.Domain, request.Service, request.Body)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.HomeAssistantCallServiceResponse{Result: result}, nil

	case "files.sources":
		return protocol.FilesSourcesResponse{Sources: d.files.SearchSourcesWithRevision(d.fileSourceRevisionKey), DefaultSourceID: d.files.SearchDefaultSourceID()}, nil

	case "files.list":
		request, err := decodeBody[protocol.FilesListRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		items, err := d.files.ListSource(ctx, request.SourceID, request.Path)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.FilesListResponse{Items: items}, nil

	case "files.list_page":
		request, err := decodeBody[protocol.FilesListPageRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		response, err := d.files.ListPageSource(ctx, request.SourceID, request.Path, request.Cursor)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case protocol.CommandMCPContextList:
		request, err := decodeBody[protocol.MCPContextListRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_mcp_context_request", err)
		}
		response, err := d.mcpctx.List(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil
	case protocol.CommandMCPContextSearch:
		request, err := decodeBody[protocol.MCPContextSearchRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_mcp_context_request", err)
		}
		response, err := d.mcpctx.Search(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil
	case protocol.CommandMCPContextRead:
		request, err := decodeBody[protocol.MCPContextReadRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_mcp_context_request", err)
		}
		response, err := d.mcpctx.Read(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil
	case protocol.CommandMCPContextTest:
		request, err := decodeBody[protocol.MCPContextTestRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_mcp_context_request", err)
		}
		response, err := d.mcpctx.Test(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case "files.stat":
		request, err := decodeBody[protocol.FilesStatRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		item, err := d.files.StatSource(ctx, request.SourceID, request.Path)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.FilesStatResponse{Item: item}, nil

	case "files.search":
		request, err := decodeBody[protocol.FilesSearchRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		items, err := d.files.SearchSource(ctx, request.SourceID, request.Query, request.Limit)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.FilesSearchResponse{Items: items}, nil

	case "files.create_directory":
		request, err := decodeBody[protocol.FilesCreateDirectoryRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		if err := d.files.CreateDirectorySource(ctx, request.SourceID, request.Path); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil

	case "files.rename":
		request, err := decodeBody[protocol.FilesRenameRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		if err := d.files.RenameSource(ctx, request.SourceID, request.From, request.To); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil

	case "files.move":
		request, err := decodeBody[protocol.FilesMoveRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		if err := d.files.MoveBetweenSources(ctx, request.SourceID, request.DestinationSourceID, request.From, request.To, request.IsDirectory); err != nil {
			return nil, mapError(err)
		}
		filesDone := int64(1)
		return protocol.FileOperationJobResponse{OK: true, JobID: request.JobID, Status: "completed", FilesDone: filesDone}, nil

	case "files.move_rollback":
		request, err := decodeBody[protocol.FilesMoveRollbackRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		if err := d.files.RollbackMoveDestination(ctx, request.DestinationSourceID, request.To, request.IsDirectory); err != nil {
			return nil, mapError(err)
		}
		return protocol.FileOperationJobResponse{OK: true, JobID: request.JobID, Status: "rolled_back"}, nil

	case "files.delete":
		request, err := decodeBody[protocol.FilesDeleteRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		if err := d.files.DeleteSource(ctx, request.SourceID, request.Path, request.IsDirectory); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil

	case "files.download":
		request, err := decodeBody[protocol.FilesDownloadRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		contentBase64, err := d.files.DownloadSource(ctx, request.SourceID, request.Path)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.FilesDownloadResponse{Path: request.Path, ContentBase64: contentBase64}, nil

	case "files.upload":
		request, err := decodeBody[protocol.FilesUploadRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_file_request", err)
		}
		if err := d.files.UploadSource(ctx, request.SourceID, request.Path, request.ContentBase64); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil

	case protocol.CommandAppsList:
		if _, err := decodeBody[protocol.AppsListRequest](command.Body); err != nil {
			return nil, badRequest("invalid_app_request", err)
		}
		return d.apps.List(ctx), nil

	case protocol.CommandAppsPackagePreview:
		request, err := decodeBody[protocol.AppsPackagePreviewRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_app_request", err)
		}
		response, err := d.apps.PreviewPackage(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case protocol.CommandAppsPackageActivate:
		request, err := decodeBody[protocol.AppsPackageActivateRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_app_request", err)
		}
		response, err := d.apps.ActivatePackage(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case protocol.CommandAppsUninstall:
		request, err := decodeBody[protocol.AppsUninstallRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_app_request", err)
		}
		response, err := d.apps.Uninstall(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case protocol.CommandAppsConfigStatus:
		request, err := decodeBody[protocol.AppsConfigStatusRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_app_request", err)
		}
		response, err := d.apps.ConfigStatus(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case protocol.CommandAppsConfigApply:
		request, err := decodeBody[protocol.AppsConfigApplyRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_app_request", err)
		}
		response, err := d.apps.ConfigApply(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case protocol.CommandAppsInvoke:
		request, err := decodeBody[protocol.AppsInvokeRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_app_request", err)
		}
		response, err := d.apps.Invoke(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case "notes.list":
		notes, err := d.notes.List(ctx)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.NotesListResponse{Notes: notes}, nil

	case "notes.fetch":
		request, err := decodeBody[protocol.NotesFetchRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_note_request", err)
		}
		response, err := d.notes.Fetch(ctx, request.NoteID)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case "notes.save":
		request, err := decodeBody[protocol.NotesSaveRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_note_request", err)
		}
		response, err := d.notes.Save(ctx, request.NoteID, request.Title, request.Content, request.ExpectedRevision, request.PageType, request.Board)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	case "notes.rename":
		request, err := decodeBody[protocol.NotesRenameRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_note_request", err)
		}
		if err := d.notes.Rename(ctx, request.NoteID, request.Title); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil

	case "notes.delete":
		request, err := decodeBody[protocol.NotesDeleteRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_note_request", err)
		}
		if err := d.notes.Delete(ctx, request.NoteID); err != nil {
			return nil, mapError(err)
		}
		return protocol.EmptyResponse{OK: true}, nil

	case "notes.sync":
		notes, err := d.notes.Sync(ctx)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.NotesSyncResponse{Notes: notes}, nil

	case "notes.search":
		request, err := decodeBody[protocol.NotesSearchRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_note_request", err)
		}
		results, err := d.notes.Search(ctx, request.Query, request.Limit)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.NotesSearchResponse{Results: results}, nil

	case "notes.tags":
		tags, err := d.notes.Tags(ctx)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.NotesTagsResponse{Tags: tags}, nil

	case "notes.tag_rollup":
		request, err := decodeBody[protocol.NotesTagRollupRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_note_request", err)
		}
		items, err := d.notes.TagRollup(ctx, request.Tag)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.NotesTagRollupResponse{Items: items}, nil

	case "config.status":
		request, err := decodeBody[protocol.ConfigStatusRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_config_request", err)
		}
		profiles, err := d.config.Status(ctx, request.ServiceType)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.ConfigStatusResponse{Profiles: profiles}, nil

	case "config.apply":
		request, err := decodeBody[protocol.ConfigApplyRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_config_request", err)
		}
		profile, err := d.config.Apply(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return protocol.ConfigApplyResponse{Profile: profile}, nil

	case "config.smb_test":
		request, err := decodeBody[protocol.ConfigSMBTestRequest](command.Body)
		if err != nil {
			return nil, badRequest("invalid_config_request", err)
		}
		response, err := d.config.TestSMB(ctx, request)
		if err != nil {
			return nil, mapError(err)
		}
		return response, nil

	default:
		return nil, &protocol.ErrorPayload{
			Code:    "unsupported_command",
			Message: fmt.Sprintf("unsupported command %q", command.Command),
		}
	}
}

func decodeBody[T any](body json.RawMessage) (T, error) {
	var out T
	if len(body) == 0 {
		return out, nil
	}
	err := json.Unmarshal(body, &out)
	return out, err
}

func badRequest(code string, err error) *protocol.ErrorPayload {
	return &protocol.ErrorPayload{
		Code:    code,
		Message: err.Error(),
	}
}

func mapError(err error) *protocol.ErrorPayload {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentha.ErrDisabled):
		return &protocol.ErrorPayload{Code: "homeassistant_not_configured", Message: err.Error()}
	case errors.Is(err, agentfiles.ErrDisabled):
		return &protocol.ErrorPayload{Code: "files_not_configured", Message: err.Error()}
	case errors.Is(err, agentnotes.ErrDisabled):
		return &protocol.ErrorPayload{Code: "notes_not_configured", Message: err.Error()}
	case errors.Is(err, agentapps.ErrUnknownApp):
		return &protocol.ErrorPayload{Code: "app_not_found", Message: err.Error()}
	case errors.Is(err, agentapps.ErrDisabledApp):
		return &protocol.ErrorPayload{Code: "app_disabled", Message: err.Error()}
	case errors.Is(err, agentapps.ErrMissingStagingPackage):
		return &protocol.ErrorPayload{Code: "app_staging_missing", Message: err.Error()}
	case errors.Is(err, agentapps.ErrPackageValidation):
		return &protocol.ErrorPayload{Code: "app_package_invalid", Message: err.Error()}
	case errors.Is(err, agentapps.ErrSandboxUnavailable):
		return &protocol.ErrorPayload{Code: "app_sandbox_unavailable", Message: "Restricted app runtime is not available on this agent"}
	case errors.Is(err, agentapps.ErrPermissionRefused):
		return &protocol.ErrorPayload{Code: "app_permission_refused", Message: err.Error()}
	case errors.Is(err, agentapps.ErrUnknownCommand):
		return &protocol.ErrorPayload{Code: "app_command_not_found", Message: err.Error()}
	case errors.Is(err, agentapps.ErrAppInvocationFailed):
		return &protocol.ErrorPayload{Code: "app_invocation_failed", Message: err.Error()}
	case errors.Is(err, errUnsupportedServiceType):
		return &protocol.ErrorPayload{Code: "unsupported_service_type", Message: err.Error()}
	case errors.Is(err, agentnotes.ErrConflict):
		conflict := &agentnotes.ConflictError{}
		payload := &protocol.ErrorPayload{Code: "note_conflict", Message: err.Error()}
		if errors.As(err, &conflict) {
			payload.Details = map[string]any{
				"current": conflict.Current,
			}
		}
		return payload
	case errors.Is(err, context.DeadlineExceeded):
		return &protocol.ErrorPayload{Code: "request_timeout", Message: err.Error()}
	case errors.Is(err, context.Canceled):
		return &protocol.ErrorPayload{Code: "request_canceled", Message: err.Error()}
	case errors.Is(err, os.ErrNotExist):
		return &protocol.ErrorPayload{Code: "not_found", Message: err.Error()}
	default:
		return &protocol.ErrorPayload{Code: "upstream_error", Message: err.Error()}
	}
}
