package cloud

import (
	"context"
	"fmt"

	"github.com/dropfile/HankServerside/internal/protocol"
)

func gramatonAppCommandID(command string) (string, bool) {
	switch command {
	case protocol.CommandMediaSettingsStatus:
		return "settings_status", true
	case protocol.CommandMediaSettingsApply:
		return "settings_apply", true
	case protocol.CommandMediaSearch:
		return "search", true
	case protocol.CommandMediaPlanDownload:
		return "plan_download", true
	case protocol.CommandMediaDownloadStart:
		return "download_start", true
	case protocol.CommandMediaDownloadStatus:
		return "download_status", true
	case protocol.CommandMediaDownloadJobs:
		return "download_jobs", true
	case protocol.CommandMediaDownloadCancel:
		return "download_cancel", true
	case protocol.CommandMediaImageFetch:
		return "image_fetch", true
	default:
		return "", false
	}
}

func (s *Server) sendMediaCommand(ctx context.Context, homeID string, command string, body any) (protocol.Envelope, error) {
	commandID, ok := gramatonAppCommandID(command)
	if !ok {
		return protocol.Envelope{}, fmt.Errorf("unsupported Gramaton command %q", command)
	}
	if !s.agentHasCapability(homeID, "apps.gramaton."+commandID) {
		return protocol.Envelope{
			Error: &protocol.ErrorPayload{
				Code:    "app_not_configured",
				Message: "Gramaton app is not installed or enabled on the primary Hank Agent.",
			},
		}, nil
	}
	input, err := protocol.EncodeBody(body)
	if err != nil {
		return protocol.Envelope{}, err
	}
	envelope, err := s.sendAgentCommand(ctx, homeID, protocol.CommandAppsInvoke, protocol.AppsInvokeRequest{
		AppID:     "gramaton",
		CommandID: commandID,
		Input:     input,
	})
	if err != nil || envelope.Error != nil {
		return envelope, err
	}
	response, err := protocol.DecodePayload[protocol.AppsInvokeResponse](envelope)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode Gramaton app response: %w", err)
	}
	envelope.Payload = cloneMediaCommandPayload(response.Output)
	return envelope, nil
}

func cloneMediaCommandPayload(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	cloned := make([]byte, len(raw))
	copy(cloned, raw)
	return cloned
}
