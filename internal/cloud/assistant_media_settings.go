package cloud

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const assistantMediaSettingsStatusTimeout = 8 * time.Second
const assistantMediaImageFetchTimeout = 12 * time.Second

type assistantMediaSettingsResponse struct {
	Online             bool                              `json:"online"`
	CanEdit            bool                              `json:"can_edit"`
	Settings           protocol.MediaSettings            `json:"settings"`
	DestinationOptions []protocol.MediaDestinationOption `json:"destination_options,omitempty"`
	Jobs               []protocol.MediaDownloadJobStatus `json:"jobs"`
	Error              string                            `json:"error,omitempty"`
}

type assistantMediaJobCancelResponse struct {
	Online bool                            `json:"online"`
	Job    protocol.MediaDownloadJobStatus `json:"job"`
}

type assistantMediaJobStatusResponse struct {
	Online bool                            `json:"online"`
	Job    protocol.MediaDownloadJobStatus `json:"job"`
}

func (s *Server) handleAssistantMediaSettings(w http.ResponseWriter, r *http.Request, home domain.Home, membership domain.HomeMembership) {
	switch r.Method {
	case http.MethodGet:
		response, err := s.fetchAssistantMediaSettings(r, home, membership)
		if err != nil {
			response.Online = false
			response.CanEdit = membership.Role == domain.HomeRoleAdmin
			response.Settings = defaultAssistantMediaSettings()
			response.DestinationOptions = defaultAssistantMediaDestinationOptions(response.Settings)
			response.Error = err.Error()
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPut:
		if membership.Role != domain.HomeRoleAdmin {
			http.Error(w, "admin role required", http.StatusForbidden)
			return
		}
		var request protocol.MediaSettingsApplyRequest
		if err := parseJSON(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		request.Persist = true
		envelope, err := s.sendMediaCommand(r.Context(), home.ID, protocol.CommandMediaSettingsApply, request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if envelope.Error != nil {
			http.Error(w, envelope.Error.Message, http.StatusBadGateway)
			return
		}
		payload, err := protocol.DecodePayload[protocol.MediaSettingsApplyResponse](envelope)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, assistantMediaSettingsResponse{
			Online:             true,
			CanEdit:            true,
			Settings:           payload.Settings,
			DestinationOptions: defaultAssistantMediaDestinationOptions(payload.Settings),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) fetchAssistantMediaSettings(r *http.Request, home domain.Home, membership domain.HomeMembership) (assistantMediaSettingsResponse, error) {
	ctx, cancel := context.WithTimeout(r.Context(), assistantMediaSettingsStatusTimeout)
	defer cancel()
	envelope, err := s.sendMediaCommand(ctx, home.ID, protocol.CommandMediaSettingsStatus, protocol.MediaSettingsStatusRequest{})
	if err != nil {
		return assistantMediaSettingsResponse{}, err
	}
	if envelope.Error != nil {
		return assistantMediaSettingsResponse{}, errors.New(envelope.Error.Message)
	}
	payload, err := protocol.DecodePayload[protocol.MediaSettingsStatusResponse](envelope)
	if err != nil {
		return assistantMediaSettingsResponse{}, err
	}
	return assistantMediaSettingsResponse{
		Online:             true,
		CanEdit:            membership.Role == domain.HomeRoleAdmin,
		Settings:           payload.Settings,
		DestinationOptions: payload.DestinationOptions,
		Jobs:               payload.Jobs,
	}, nil
}

func (s *Server) handleAssistantMediaJobStatus(w http.ResponseWriter, r *http.Request, home domain.Home, _ domain.HomeMembership, jobID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	envelope, err := s.sendMediaCommand(r.Context(), home.ID, protocol.CommandMediaDownloadStatus, protocol.MediaDownloadStatusRequest{JobID: jobID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if envelope.Error != nil {
		http.Error(w, envelope.Error.Message, http.StatusBadGateway)
		return
	}
	payload, err := protocol.DecodePayload[protocol.MediaDownloadStatusResponse](envelope)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, assistantMediaJobStatusResponse{Online: true, Job: payload.Job})
}

func (s *Server) handleAssistantMediaImage(w http.ResponseWriter, r *http.Request, home domain.Home, _ domain.HomeMembership) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		http.Error(w, "media image URL is required", http.StatusBadRequest)
		return
	}
	if len(rawURL) > 4096 {
		http.Error(w, "media image URL is too long", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), assistantMediaImageFetchTimeout)
	defer cancel()
	envelope, err := s.sendMediaCommand(ctx, home.ID, protocol.CommandMediaImageFetch, protocol.MediaImageFetchRequest{URL: rawURL})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if envelope.Error != nil {
		http.Error(w, envelope.Error.Message, http.StatusBadGateway)
		return
	}
	payload, err := protocol.DecodePayload[protocol.MediaImageFetchResponse](envelope)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	image, err := base64.StdEncoding.DecodeString(payload.ContentBase64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	contentType := strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "").Replace(payload.ContentType))
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		http.Error(w, "media image response was not an image", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image)
}

func (s *Server) handleAssistantMediaJobCancel(w http.ResponseWriter, r *http.Request, home domain.Home, membership domain.HomeMembership, jobID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	envelope, err := s.sendMediaCommand(r.Context(), home.ID, protocol.CommandMediaDownloadCancel, protocol.MediaDownloadCancelRequest{JobID: jobID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if envelope.Error != nil {
		http.Error(w, envelope.Error.Message, http.StatusBadGateway)
		return
	}
	payload, err := protocol.DecodePayload[protocol.MediaDownloadCancelResponse](envelope)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, assistantMediaJobCancelResponse{Online: true, Job: payload.Job})
}

func defaultAssistantMediaSettings() protocol.MediaSettings {
	return protocol.MediaSettings{
		BaseURL:             "https://gramaton.io",
		PreferredQuality:    "1080p",
		RequireConfirmation: true,
	}
}

func defaultAssistantMediaDestinationOptions(settings protocol.MediaSettings) []protocol.MediaDestinationOption {
	sourceID := strings.TrimSpace(settings.SourceID)
	options := []protocol.MediaDestinationOption{{Value: "", Label: defaultAssistantMediaDestinationLabel(sourceID, ""), SourceID: sourceID}}
	seen := map[string]struct{}{"": {}}
	for _, current := range []string{settings.DestinationPath, settings.MovieDestinationPath, settings.TVDestinationPath} {
		if current == "" {
			continue
		}
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		options = append(options, protocol.MediaDestinationOption{
			Value:    current,
			Label:    defaultAssistantMediaDestinationLabel(sourceID, current),
			SourceID: sourceID,
		})
	}
	return options
}

func defaultAssistantMediaDestinationLabel(sourceID string, value string) string {
	prefix := "SMB share"
	if sourceID != "" {
		prefix += " " + sourceID
	}
	if value == "" {
		return prefix + " root"
	}
	return prefix + "/" + value
}
