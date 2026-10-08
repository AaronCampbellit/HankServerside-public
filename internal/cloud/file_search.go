package cloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

type indexedFileResult struct {
	protocol.FileItem
	AgentID    string `json:"agent_id"`
	SourceName string `json:"source_name,omitempty"`
}

type fileSearchResponse struct {
	Items   []indexedFileResult      `json:"items"`
	Status  string                   `json:"status"`
	Sources []store.FileSearchStatus `json:"sources"`
}

type liveFileSource struct {
	protocol.FileSourceInfo
	Default bool
}

func supportsFileSearchIndex(capabilities []string) bool {
	return slices.Contains(capabilities, "files.sources") && slices.Contains(capabilities, "files.list_page")
}

func (s *Server) liveFileSearchSources(ctx context.Context, homeID string, agents []string) map[string]liveFileSource {
	live := make(map[string]liveFileSource)
	var mu sync.Mutex
	var group sync.WaitGroup
	for _, agentID := range agents {
		group.Add(1)
		go func(agentID string) {
			defer group.Done()
			requestCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			defer cancel()
			envelope, err := s.sendAgentCommandTo(requestCtx, homeID, agentID, "files.sources", struct{}{})
			if err != nil || envelope.Error != nil {
				return
			}
			payload, err := protocol.DecodePayload[protocol.FilesSourcesResponse](envelope)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, source := range payload.Sources {
				if source.Enabled && source.Readable {
					live[agentID+":"+source.ID] = liveFileSource{FileSourceInfo: source, Default: source.ID == payload.DefaultSourceID}
				}
			}
		}(agentID)
	}
	group.Wait()
	return live
}

type fileSearchWorker struct{ cancel context.CancelFunc }

type fileSearchIndexContextKey struct{}

type fileSearchDirectoryRef struct {
	SourceID string
	Path     string
}

func fileSearchParent(value string) string {
	parent := path.Dir(strings.TrimPrefix(value, "/"))
	if parent == "." || parent == "/" {
		return ""
	}
	return parent
}

func fileSearchInvalidations(command protocol.RoutedCommand) []fileSearchDirectoryRef {
	switch command.Command {
	case "files.create_directory", "files.upload", "files.delete", "files.rename", "files.move":
	default:
		return nil
	}
	var request struct {
		SourceID            string `json:"source_id"`
		DestinationSourceID string `json:"destination_source_id"`
		Path                string `json:"path"`
		From                string `json:"from"`
		To                  string `json:"to"`
	}
	if json.Unmarshal(command.Body, &request) != nil {
		return nil
	}
	var dirs []fileSearchDirectoryRef
	if request.Path != "" {
		dirs = append(dirs, fileSearchDirectoryRef{request.SourceID, fileSearchParent(request.Path)})
	}
	if request.From != "" {
		dirs = append(dirs, fileSearchDirectoryRef{request.SourceID, fileSearchParent(request.From)})
	}
	if request.To != "" {
		sourceID := request.SourceID
		if request.DestinationSourceID != "" {
			sourceID = request.DestinationSourceID
		}
		dirs = append(dirs, fileSearchDirectoryRef{sourceID, fileSearchParent(request.To)})
	}
	return dirs
}

func (s *Server) startFileSearchIndexer(parent context.Context, homeID, agentID string) {
	key := homeID + ":" + agentID
	ctx, cancel := context.WithCancel(parent)
	worker := &fileSearchWorker{cancel: cancel}
	if previous, loaded := s.fileSearchWorkers.Swap(key, worker); loaded {
		previous.(*fileSearchWorker).cancel()
	}
	defer func() {
		s.fileSearchWorkers.CompareAndDelete(key, worker)
		cancel()
	}()
	s.runFileSearchIndexer(ctx, homeID, agentID)
}

func (s *Server) runFileSearchIndexer(ctx context.Context, homeID, agentID string) {
	ctx = context.WithValue(ctx, fileSearchIndexContextKey{}, true)
	nextDiscovery := time.Time{}
	sourcePolicies := map[string]fileAccessPolicy{}
	for ctx.Err() == nil {
		if time.Now().After(nextDiscovery) {
			requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			envelope, err := s.sendAgentCommandTo(requestCtx, homeID, agentID, "files.sources", struct{}{})
			cancel()
			if err != nil || envelope.Error != nil {
				if ctx.Err() == nil {
					s.logger.Warn("file search source discovery failed", "agent_id", agentID)
				}
				if !waitFileSearch(ctx, 20*time.Second) {
					return
				}
				continue
			}
			payload, err := protocol.DecodePayload[protocol.FilesSourcesResponse](envelope)
			if err != nil {
				s.logger.Warn("file search source response invalid", "agent_id", agentID)
				return
			}
			if err := s.store.SyncFileSearchSources(ctx, homeID, agentID, payload.DefaultSourceID, payload.Sources); err != nil {
				s.logger.Warn("file search source sync failed", "agent_id", agentID, "error", err)
				if !waitFileSearch(ctx, 20*time.Second) {
					return
				}
				continue
			}
			sourcePolicies = make(map[string]fileAccessPolicy, len(payload.Sources))
			for _, source := range payload.Sources {
				sourcePolicies[source.ID] = fileAccessPolicy{AllowedPrefixes: source.AllowedPrefixes, BlockedPrefixes: source.BlockedPrefixes}
			}
			nextDiscovery = time.Now().Add(time.Minute)
		}
		dir, err := s.store.NextFileSearchDirectory(ctx, homeID, agentID)
		if errors.Is(err, sql.ErrNoRows) {
			if !waitFileSearch(ctx, 10*time.Second) {
				return
			}
			continue
		}
		if err != nil {
			s.logger.Warn("file search queue failed", "agent_id", agentID, "error", err)
			if !waitFileSearch(ctx, 10*time.Second) {
				return
			}
			continue
		}
		items, err := s.listFileSearchDirectory(ctx, homeID, agentID, dir, sourcePolicies[dir.SourceID])
		if err == nil {
			err = s.store.SaveFileSearchDirectory(ctx, homeID, agentID, dir, items)
		} else if dir.Path != "" {
			// Allowed roots can name a single file. A failed listing is safe to
			// index only if a scoped stat confirms that exact file.
			statCtx, statCancel := context.WithTimeout(ctx, 20*time.Second)
			statEnvelope, statErr := s.sendAgentCommandTo(statCtx, homeID, agentID, "files.stat", protocol.FilesStatRequest{SourceID: dir.SourceID, Path: dir.Path})
			statCancel()
			if statErr == nil && statEnvelope.Error == nil {
				var stat protocol.FilesStatResponse
				stat, statErr = protocol.DecodePayload[protocol.FilesStatResponse](statEnvelope)
				if statErr == nil {
					statErr = s.store.SaveFileSearchSingleItem(ctx, homeID, agentID, dir, stat.Item)
				}
			}
			if statErr != nil {
				err = statErr
			} else if statEnvelope.Error != nil {
				err = errors.New(statEnvelope.Error.Code)
			}
		}
		if err != nil && ctx.Err() == nil {
			_ = s.store.DeferFileSearchDirectory(ctx, homeID, agentID, dir)
			s.logger.Warn("file search directory indexing failed", "agent_id", agentID, "source_id", dir.SourceID, "error_code", "operation_failed")
		}
	}
}

func (s *Server) listFileSearchDirectory(ctx context.Context, homeID, agentID string, dir store.FileSearchDirectory, policy fileAccessPolicy) ([]protocol.FileItem, error) {
	items := make([]protocol.FileItem, 0)
	cursor := ""
	for {
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		envelope, err := s.sendAgentCommandTo(requestCtx, homeID, agentID, "files.list_page", protocol.FilesListPageRequest{SourceID: dir.SourceID, Path: dir.Path, Cursor: cursor})
		cancel()
		if err != nil {
			return nil, err
		}
		if envelope.Error != nil {
			return nil, errors.New(envelope.Error.Code)
		}
		page, err := protocol.DecodePayload[protocol.FilesListPageResponse](envelope)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if policy.allow("read", item.Path) == nil {
				items = append(items, item)
			}
		}
		if page.NextCursor == "" {
			return items, nil
		}
		if len(page.Items) == 0 {
			return nil, errors.New("invalid file listing page")
		}
		cursor = page.NextCursor
	}
}

func waitFileSearch(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) catalogFileMatches(ctx context.Context, home domain.Home, membership domain.HomeMembership, auth authContext, agentID, sourceID, query string, limit int, allAgents bool) (fileSearchResponse, error) {
	response := fileSearchResponse{Items: []indexedFileResult{}, Status: "indexing", Sources: []store.FileSearchStatus{}}
	if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		return response, err
	}
	var agents []string
	if allAgents {
		for _, agent := range s.router.AgentsForHome(home.ID) {
			if supportsFileSearchIndex(agent.Capabilities) {
				agents = append(agents, agent.AgentID)
			}
		}
	} else {
		connection, ok := s.router.ResolveAgent(home.ID, agentID)
		if !ok || !supportsFileSearchIndex(connection.capabilities) {
			response.Status = "offline"
			return response, nil
		}
		agents = []string{connection.agent.ID}
	}
	if len(agents) == 0 {
		response.Status = "offline"
		return response, nil
	}
	sort.Strings(agents)
	live := s.liveFileSearchSources(ctx, home.ID, agents)
	if !allAgents && sourceID == "" {
		for _, source := range live {
			if source.Default {
				sourceID = source.ID
				break
			}
		}
	}
	if !allAgents && (sourceID == "" || live[agents[0]+":"+sourceID].ID == "") {
		response.Status = "offline"
		return response, nil
	}
	defaultOnly := false
	statuses, err := s.store.FileSearchStatuses(ctx, home.ID, agents, sourceID, defaultOnly)
	if err != nil {
		return response, err
	}
	response.Sources = statuses
	if len(statuses) > 0 {
		response.Status = "ready"
		for _, status := range statuses {
			if status.Pending > 0 {
				response.Status = "indexing"
			}
			if status.Error {
				response.Status = "partial"
			}
		}
	}
	if len([]rune(strings.TrimSpace(query))) < 2 {
		return response, nil
	}
	matches, err := s.store.SearchFileCatalog(ctx, home.ID, agents, sourceID, query, defaultOnly, limit)
	if err != nil {
		return response, err
	}
	cloudPolicy := fileAccessPolicy{}
	if profile, err := s.store.GetHomeServiceProfile(ctx, home.ID, domain.ServiceTypeSMB); err == nil {
		cloudPolicy = parseFileAccessPolicy(profile.PublicConfigJSON)
	} else if !errors.Is(err, store.ErrNotFound) {
		return response, err
	}
	for _, match := range matches {
		liveSource, ok := live[match.AgentID+":"+match.Item.SourceID]
		if !ok {
			continue
		}
		if cloudPolicy.allow("read", match.Item.Path) != nil {
			continue
		}
		sourcePolicy := fileAccessPolicy{AllowedPrefixes: liveSource.AllowedPrefixes, BlockedPrefixes: liveSource.BlockedPrefixes}
		if sourcePolicy.allow("read", match.Item.Path) != nil {
			continue
		}
		response.Items = append(response.Items, indexedFileResult{FileItem: match.Item, AgentID: match.AgentID, SourceName: match.SourceName})
	}
	return response, nil
}

func (s *Server) handleHomeFileSearch(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 1 || parts[0] != "file-search" {
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > maxSearchQuery {
		query = query[:maxSearchQuery]
	}
	response, err := s.catalogFileMatches(r.Context(), home, membership, auth,
		strings.TrimSpace(r.URL.Query().Get("agent_id")), strings.TrimSpace(r.URL.Query().Get("source_id")), query, 100, false)
	if err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			http.Error(w, "forbidden", http.StatusForbidden)
		} else {
			http.Error(w, "search unavailable", http.StatusInternalServerError)
		}
		return true
	}
	writeJSON(w, http.StatusOK, response)
	return true
}

func fileSearchURL(match indexedFileResult) string {
	params := url.Values{}
	params.Set("agent_id", match.AgentID)
	params.Set("source_id", match.SourceID)
	params.Set("path", match.Path)
	if !match.IsDirectory {
		params.Set("preview", "1")
	}
	return "/dashboard/file-server?" + params.Encode()
}
