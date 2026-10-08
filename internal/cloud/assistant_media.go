package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

var (
	mediaOptionPattern      = regexp.MustCompile(`(?i)^\s*(?:option\s*)?(\d+)\s*$`)
	mediaQuotedTitlePattern = regexp.MustCompile(`"([^"]+)"`)
	mediaSeasonPattern      = regexp.MustCompile(`(?i)\b(?:season|s)\s*(\d+)\b`)
	mediaEpisodePattern     = regexp.MustCompile(`(?i)\b(?:episode|e)\s*(\d+)\b`)
	mediaCompactSxePattern  = regexp.MustCompile(`(?i)\bs\s*(\d+)\s*e\s*(\d+)\b`)
)

func mediaAvailabilityQuery(prompt string) (string, bool) {
	trimmed := strings.TrimSpace(prompt)
	lowered := strings.ToLower(trimmed)
	hasDownloadIntent := mediaPromptHasDownloadIntent(lowered)
	hasTypedSearchIntent := hasSearchVerb(lowered) && mediaPromptHasTypeHint(trimmed)
	if !hasDownloadIntent && mediaPromptLooksLikeFileRequest(lowered) {
		return "", false
	}
	if !hasDownloadIntent && !hasTypedSearchIntent {
		return "", false
	}

	if match := mediaQuotedTitlePattern.FindStringSubmatch(trimmed); len(match) == 2 {
		query := cleanQuotedMediaQuery(match[1])
		return query, query != ""
	}

	query := stripMediaPromptPrefix(trimmed)
	query = stripMediaPromptSuffix(query)
	query = cleanMediaPromptQuery(query)
	return query, query != ""
}

func mediaPromptHasDownloadIntent(lowered string) bool {
	if !strings.Contains(lowered, "download") {
		return false
	}
	if mediaPromptLooksLikeFileRequest(lowered) && !mediaPromptHasTypeHint(lowered) {
		return false
	}
	return strings.Contains(lowered, "available") ||
		strings.Contains(lowered, "availabe") ||
		strings.Contains(lowered, "for download") ||
		strings.Contains(lowered, "to download") ||
		strings.HasPrefix(lowered, "can i download ") ||
		strings.HasPrefix(lowered, "can you download ") ||
		strings.HasPrefix(lowered, "download ") ||
		hasSearchVerb(lowered)
}

func mediaPromptLooksLikeFileRequest(lowered string) bool {
	return strings.Contains(lowered, "file") ||
		strings.Contains(lowered, "folder") ||
		strings.Contains(lowered, "directory") ||
		strings.Contains(lowered, "smb") ||
		strings.Contains(lowered, "share") ||
		strings.Contains(lowered, "document") ||
		strings.Contains(lowered, "pdf") ||
		strings.Contains(lowered, " tax") ||
		strings.Contains(lowered, "tax ") ||
		strings.Contains(lowered, "taxes")
}

func mediaPromptHasTypeHint(prompt string) bool {
	for _, token := range strings.Fields(normalizedMediaSelection(prompt)) {
		if isMediaTypeHint(token) {
			return true
		}
	}
	return false
}

func isMediaTypeHint(token string) bool {
	switch token {
	case "movie", "movies", "film", "films", "series", "show", "shows", "tv", "season", "seasons", "episode", "episodes":
		return true
	default:
		return false
	}
}

func stripMediaPromptPrefix(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	lowered := strings.ToLower(trimmed)
	prefixes := []string{
		"can you download ",
		"can i download ",
		"please search for ",
		"please search ",
		"please find ",
		"search for ",
		"look for ",
		"find all ",
		"search ",
		"find ",
		"is ",
		"are ",
		"download ",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lowered, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return stripAssistantSearchPrefix(trimmed)
}

func stripMediaPromptSuffix(query string) string {
	suffixes := []string{
		" available for download",
		" availabe for download",
		" available to download",
		" availabe to download",
		" for download",
		" to download",
		" available",
		" availabe",
		" download",
	}
	for {
		trimmed := strings.TrimSpace(query)
		lowered := strings.ToLower(trimmed)
		changed := false
		for _, suffix := range suffixes {
			if strings.HasSuffix(lowered, suffix) {
				query = strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)])
				changed = true
				break
			}
		}
		if !changed {
			return trimmed
		}
	}
}

func cleanQuotedMediaQuery(query string) string {
	return strings.TrimSpace(query)
}

func cleanMediaPromptQuery(query string) string {
	query = strings.TrimSpace(query)
	query = strings.Trim(query, " \t\r\n\"'`")
	for {
		lowered := strings.ToLower(query)
		if strings.HasPrefix(lowered, "for ") {
			query = strings.TrimSpace(query[len("for "):])
			continue
		}
		if strings.HasPrefix(lowered, "called ") {
			query = strings.TrimSpace(query[len("called "):])
			continue
		}
		if strings.HasPrefix(lowered, "named ") {
			query = strings.TrimSpace(query[len("named "):])
			continue
		}
		break
	}
	if mediaQueryEndsWithTypeHint(query) {
		query = stripLeadingMediaArticle(query)
	}
	return query
}

func mediaCancelPrompt(query string) (string, bool, bool) {
	trimmed := strings.TrimSpace(query)
	lowered := strings.ToLower(trimmed)
	if lowered != "cancel" && !strings.HasPrefix(lowered, "cancel ") {
		return "", false, false
	}
	target := strings.TrimSpace(trimmed[len("cancel"):])
	if strings.HasPrefix(strings.ToLower(target), "job ") {
		target = strings.TrimSpace(target[len("job "):])
	}
	normalizedTarget := strings.ToLower(target)
	if target == "" || normalizedTarget == "latest" || normalizedTarget == "last" || normalizedTarget == "current" {
		return "", true, true
	}
	return target, false, true
}

func mediaQueryEndsWithTypeHint(query string) bool {
	tokens := strings.Fields(normalizedMediaSelection(query))
	if len(tokens) == 0 {
		return false
	}
	return isMediaTypeHint(tokens[len(tokens)-1])
}

func stripLeadingMediaArticle(query string) string {
	trimmed := strings.TrimSpace(query)
	lowered := strings.ToLower(trimmed)
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lowered, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return trimmed
}

func (s *Server) answerMediaSearch(ctx context.Context, home domain.Home, query string) (assistantMessageContent, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "warn",
			Scope:   "media",
			Event:   "media.search.empty_query",
			Summary: "Media workflow matched, but no usable title was parsed.",
		})
		return assistantMessageContent{Text: "Tell me the movie or show name to search for download availability."}, nil
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "media",
		Event:   "media.search.start",
		Summary: "Starting media search through the primary Hank Agent.",
		Details: traceDetails(map[string]any{
			"limit": 10,
		}),
	})
	envelope, err := s.sendMediaCommand(ctx, home.ID, protocol.CommandMediaSearch, protocol.MediaSearchRequest{Query: query, Limit: 10})
	if err != nil || envelope.Error != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "media",
			Event:   "media.search.failed",
			Summary: "Media search could not reach or complete through the primary Hank Agent.",
		})
		return assistantMessageContent{Text: "I couldn't reach the media source through the primary Hank Agent right now."}, nil
	}
	payload, err := protocol.DecodePayload[protocol.MediaSearchResponse](envelope)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "media",
			Event:   "media.search.decode_failed",
			Summary: "Media search response could not be decoded.",
		})
		return assistantMessageContent{}, err
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "media",
		Event:   "media.search.results",
		Summary: "Media search returned results.",
		Details: traceDetails(map[string]any{
			"result_count": len(payload.Results),
		}),
	})
	if len(payload.Results) == 0 {
		return assistantMessageContent{Text: fmt.Sprintf("I couldn't find `%s` available for download.", query)}, nil
	}
	return s.assistantContentFromMediaSearchResponse(ctx, home, query, payload)
}

func (s *Server) answerMediaCancel(ctx context.Context, home domain.Home, jobID string, latest bool) (assistantMessageContent, error) {
	jobID = strings.TrimSpace(jobID)
	if latest {
		jobs, err := s.mediaDownloadJobs(ctx, home.ID)
		if err != nil {
			return assistantMessageContent{Text: "I couldn't look up Gramaton jobs through the primary Hank Agent right now."}, nil
		}
		for _, job := range jobs {
			if mediaJobIsActive(job.Status) {
				jobID = job.JobID
				break
			}
		}
		if jobID == "" && len(jobs) > 0 {
			jobID = jobs[0].JobID
		}
	}
	if jobID == "" {
		return assistantMessageContent{Text: "I couldn't find an active Gramaton job to cancel."}, nil
	}
	envelope, err := s.sendMediaCommand(ctx, home.ID, protocol.CommandMediaDownloadCancel, protocol.MediaDownloadCancelRequest{JobID: jobID})
	if err != nil {
		return assistantMessageContent{Text: "I couldn't reach the primary Hank Agent to cancel that media job."}, nil
	}
	if envelope.Error != nil {
		return assistantMessageContent{Text: fmt.Sprintf("I couldn't cancel media job `%s`: %s", jobID, envelope.Error.Message)}, nil
	}
	payload, err := protocol.DecodePayload[protocol.MediaDownloadCancelResponse](envelope)
	if err != nil {
		return assistantMessageContent{}, err
	}
	return mediaDownloadCancelledContent(payload.Job), nil
}

func (s *Server) mediaDownloadJobs(ctx context.Context, homeID string) ([]protocol.MediaDownloadJobStatus, error) {
	envelope, err := s.sendMediaCommand(ctx, homeID, protocol.CommandMediaDownloadJobs, protocol.MediaDownloadJobsRequest{})
	if err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, errors.New(envelope.Error.Message)
	}
	payload, err := protocol.DecodePayload[protocol.MediaDownloadJobsResponse](envelope)
	if err != nil {
		return nil, err
	}
	return payload.Jobs, nil
}

func mediaJobIsActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case protocol.MediaJobStatusQueued, protocol.MediaJobStatusRunning:
		return true
	default:
		return false
	}
}

func (s *Server) assistantContentFromMediaSearchResponse(ctx context.Context, home domain.Home, query string, payload protocol.MediaSearchResponse) (assistantMessageContent, error) {
	query = strings.TrimSpace(firstNonBlank(query, payload.Query))
	if len(payload.Results) == 0 {
		return assistantMessageContent{Text: fmt.Sprintf("I couldn't find `%s` available for download.", query)}, nil
	}
	if len(payload.Results) == 1 {
		card := assistantMediaCard(payload.Results[0], 1)
		return s.answerMediaSelection(ctx, home, card)
	}
	cards := make([]assistantResultCard, 0, len(payload.Results))
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("I found %d media matches for `%s`. Choose one of the options below:", len(payload.Results), query))
	for index, result := range payload.Results {
		card := assistantMediaCard(result, index+1)
		cards = append(cards, card)
		builder.WriteString(fmt.Sprintf("\n%s", mediaCardLine(card)))
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}, nil
}

func (s *Server) answerMediaSelection(ctx context.Context, home domain.Home, card assistantResultCard) (assistantMessageContent, error) {
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "media",
		Event:   "media.selection.start",
		Summary: "Preparing the selected media option.",
	})
	selectionTitle := card.Title
	if card.MediaFilter != nil && strings.TrimSpace(card.SearchText) != "" {
		selectionTitle = card.SearchText
	}
	selection := protocol.MediaSearchResult{
		ID:         card.MediaOptionID,
		Title:      selectionTitle,
		Year:       card.Year,
		Type:       card.MediaType,
		Summary:    card.Summary,
		PosterURL:  card.ImageURL,
		PagePath:   card.Path,
		SearchText: card.SearchText,
	}
	var filter protocol.MediaEpisodeFilter
	if card.MediaFilter != nil {
		filter = *card.MediaFilter
	}
	plan, err := s.planMediaDownload(ctx, home.ID, selection, filter)
	if err != nil {
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Level:   "error",
			Scope:   "media",
			Event:   "media.plan.failed",
			Summary: "Could not prepare the media download plan.",
		})
		return assistantMessageContent{Text: fmt.Sprintf("I found `%s`, but I couldn't prepare the download plan through the primary Hank Agent right now.", card.Title)}, nil
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "media",
		Event:   "media.plan.prepared",
		Summary: "Prepared the media download plan.",
		Details: traceDetails(map[string]any{
			"items":                   plan.ItemCount,
			"preferred_quality_count": plan.PreferredQualityCount,
			"fallback_quality_count":  plan.FallbackQualityCount,
			"missing_link_count":      plan.MissingLinkCount,
			"existing_count":          plan.ExistingCount,
		}),
	})
	if plan.ItemCount == 0 || plan.MissingLinkCount >= plan.ItemCount {
		return assistantMessageContent{Text: fmt.Sprintf("I found `%s`, but no downloadable movie or episode entries were available.", card.Title)}, nil
	}
	if plan.Selection.Type == protocol.MediaTypeSeries && len(plan.Seasons) > 0 && mediaFilterIsEmpty(filter) {
		return mediaInventoryContent(plan, card), nil
	}
	if !mediaPlanRequiresConfirmation(plan) {
		response, err := s.startMediaDownload(ctx, home.ID, plan.Selection, plan.Filter)
		if err != nil {
			s.recordAssistantTrace(ctx, assistantTraceEvent{
				Level:   "error",
				Scope:   "media",
				Event:   "media.download.failed",
				Summary: "Could not start the media download after planning.",
			})
			return assistantMessageContent{Text: fmt.Sprintf("I found `%s`, but I couldn't start the download through the primary Hank Agent right now.", card.Title)}, nil
		}
		s.recordAssistantTrace(ctx, assistantTraceEvent{
			Scope:   "media",
			Event:   "media.confirmation.skipped",
			Summary: "Media workflow is configured to start downloads without an approval pause.",
		})
		return mediaDownloadStartedContent(firstNonBlank(plan.Selection.Title, card.Title), plan.Selection, response.Job, plan.DestinationPath), nil
	}
	pending := assistantPendingAction{
		Kind: "media_download",
		MediaDownload: &assistantPendingMediaDownload{
			Selection:             plan.Selection,
			Filter:                plan.Filter,
			Title:                 firstNonBlank(plan.Selection.Title, card.Title),
			MediaType:             plan.Selection.Type,
			ItemCount:             plan.ItemCount,
			PreferredQualityCount: plan.PreferredQualityCount,
			FallbackQualityCount:  plan.FallbackQualityCount,
			MissingLinkCount:      plan.MissingLinkCount,
			ExistingCount:         plan.ExistingCount,
			DestinationPath:       plan.DestinationPath,
			Confirmation:          mediaConfirmationText(plan),
		},
	}
	return assistantMessageContent{
		Text:  mediaPlanText(plan),
		Cards: []assistantResultCard{card},
		Meta: map[string]interface{}{
			"pending_action": pending,
		},
	}, nil
}

func mediaInventoryContent(plan protocol.MediaDownloadPlan, sourceCard assistantResultCard) assistantMessageContent {
	title := firstNonBlank(plan.Selection.Title, sourceCard.Title, "Selected TV show")
	cards := mediaInventoryCards(plan, sourceCard)
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("I found `%s` and loaded %d season(s), %d episode(s). Choose all seasons, a season, or type a specific episode like `S1E2`.", title, len(plan.Seasons), plan.ItemCount))
	for _, season := range plan.Seasons {
		builder.WriteString(fmt.Sprintf("\nSeason %d: %d episode(s)", season.Season, season.EpisodeCount))
		if len(season.Episodes) > 0 {
			names := make([]string, 0, min(len(season.Episodes), 4))
			for _, episode := range season.Episodes {
				label := fmt.Sprintf("E%d", episode.Episode)
				if strings.TrimSpace(episode.Title) != "" {
					label += " " + strings.TrimSpace(episode.Title)
				}
				names = append(names, label)
				if len(names) >= 4 {
					break
				}
			}
			if len(names) > 0 {
				builder.WriteString(": " + strings.Join(names, ", "))
				if len(season.Episodes) > len(names) {
					builder.WriteString(", ...")
				}
			}
		}
	}
	return assistantMessageContent{Text: builder.String(), Cards: cards}
}

func mediaInventoryCards(plan protocol.MediaDownloadPlan, sourceCard assistantResultCard) []assistantResultCard {
	title := firstNonBlank(plan.Selection.Title, sourceCard.Title, "Selected TV show")
	base := assistantResultCard{
		Kind:          "media",
		Path:          firstNonBlank(plan.Selection.PagePath, sourceCard.Path),
		SearchText:    title,
		ImageURL:      firstNonBlank(plan.Selection.PosterURL, sourceCard.ImageURL),
		MediaOptionID: firstNonBlank(plan.Selection.ID, sourceCard.MediaOptionID),
		MediaType:     protocol.MediaTypeSeries,
		Year:          firstNonZero(plan.Selection.Year, sourceCard.Year),
	}
	allFilter := protocol.MediaEpisodeFilter{All: true}
	cards := []assistantResultCard{{
		Kind:          base.Kind,
		Title:         "All seasons",
		Summary:       fmt.Sprintf("%s - %d episode(s)", title, plan.ItemCount),
		ActionTitle:   "Choose",
		Path:          base.Path,
		SearchText:    base.SearchText,
		ImageURL:      base.ImageURL,
		MediaOptionID: base.MediaOptionID,
		MediaType:     base.MediaType,
		MediaFilter:   &allFilter,
		EpisodeCount:  plan.ItemCount,
		Year:          base.Year,
	}}
	for _, season := range plan.Seasons {
		filter := protocol.MediaEpisodeFilter{Season: season.Season}
		cards = append(cards, assistantResultCard{
			Kind:          base.Kind,
			Title:         fmt.Sprintf("Season %d", season.Season),
			Summary:       fmt.Sprintf("%s - %d episode(s)", title, season.EpisodeCount),
			ActionTitle:   "Choose",
			Path:          base.Path,
			SearchText:    base.SearchText,
			ImageURL:      base.ImageURL,
			MediaOptionID: base.MediaOptionID,
			MediaType:     base.MediaType,
			MediaFilter:   &filter,
			EpisodeCount:  season.EpisodeCount,
			Year:          base.Year,
		})
	}
	return cards
}

func mediaPlanRequiresConfirmation(plan protocol.MediaDownloadPlan) bool {
	if plan.RequireConfirmation == nil {
		return true
	}
	return *plan.RequireConfirmation
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func mediaFilterIsEmpty(filter protocol.MediaEpisodeFilter) bool {
	return filter.Season == 0 && filter.Episode == 0 && !filter.All
}

func (s *Server) planMediaDownload(ctx context.Context, homeID string, selection protocol.MediaSearchResult, filter protocol.MediaEpisodeFilter) (protocol.MediaDownloadPlan, error) {
	envelope, err := s.sendMediaCommand(ctx, homeID, protocol.CommandMediaPlanDownload, protocol.MediaPlanDownloadRequest{Selection: selection, Filter: filter})
	if err != nil {
		return protocol.MediaDownloadPlan{}, err
	}
	if envelope.Error != nil {
		return protocol.MediaDownloadPlan{}, errors.New(envelope.Error.Message)
	}
	payload, err := protocol.DecodePayload[protocol.MediaPlanDownloadResponse](envelope)
	if err != nil {
		return protocol.MediaDownloadPlan{}, err
	}
	return payload.Plan, nil
}

func (s *Server) startMediaDownload(ctx context.Context, homeID string, selection protocol.MediaSearchResult, filter protocol.MediaEpisodeFilter) (protocol.MediaDownloadStartResponse, error) {
	envelope, err := s.sendMediaCommand(ctx, homeID, protocol.CommandMediaDownloadStart, protocol.MediaDownloadStartRequest{Selection: selection, Filter: filter})
	if err != nil {
		return protocol.MediaDownloadStartResponse{}, err
	}
	if envelope.Error != nil {
		return protocol.MediaDownloadStartResponse{}, errors.New(envelope.Error.Message)
	}
	response, err := protocol.DecodePayload[protocol.MediaDownloadStartResponse](envelope)
	if err != nil {
		return protocol.MediaDownloadStartResponse{}, err
	}
	s.recordAssistantTrace(ctx, assistantTraceEvent{
		Scope:   "media",
		Event:   "media.download.started",
		Summary: "Primary Hank Agent started the media download job.",
		Details: traceDetails(map[string]any{
			"items": response.Job.TotalCount,
		}),
	})
	return response, nil
}

func mediaDownloadStartedContent(title string, selection protocol.MediaSearchResult, job protocol.MediaDownloadJobStatus, destination string) assistantMessageContent {
	title = firstNonBlank(title, job.Title, selection.Title, "selected media")
	destination = firstNonBlank(destination, "Media destination")
	return assistantMessageContent{
		Text: fmt.Sprintf("Started the media download job for `%s`.", title),
		Cards: []assistantResultCard{
			{
				Kind:        "media",
				Title:       title,
				Summary:     fmt.Sprintf("Job %s is %s. %d item(s) queued for %s.", job.JobID, job.Status, job.TotalCount, destination),
				ActionTitle: "View Job",
				Path:        selection.PagePath,
				MediaType:   selection.Type,
				ImageURL:    selection.PosterURL,
				JobID:       job.JobID,
			},
		},
	}
}

func mediaDownloadCancelledContent(job protocol.MediaDownloadJobStatus) assistantMessageContent {
	title := firstNonBlank(job.Title, "media job")
	return assistantMessageContent{
		Text: fmt.Sprintf("Cancelled media job `%s`.", job.JobID),
		Cards: []assistantResultCard{
			{
				Kind:        "media",
				Title:       title,
				Summary:     fmt.Sprintf("Job %s is %s.", job.JobID, job.Status),
				ActionTitle: "View Job",
				MediaType:   protocol.MediaTypeSeries,
				JobID:       job.JobID,
			},
		},
	}
}

func (s *Server) resolvePreviousMediaSelection(ctx context.Context, sessionID string, prompt string) (assistantResultCard, bool) {
	cards := s.latestMediaCards(ctx, sessionID)
	if len(cards) == 0 {
		return assistantResultCard{}, false
	}
	if match := mediaOptionPattern.FindStringSubmatch(prompt); len(match) == 2 {
		option, _ := strconv.Atoi(match[1])
		if option > 0 && option <= len(cards) {
			return cards[option-1], true
		}
	}
	promptKey := normalizedMediaSelection(prompt)
	if promptKey == "" {
		return assistantResultCard{}, false
	}
	for _, card := range cards {
		if normalizedMediaSelection(card.Title) == promptKey {
			return card, true
		}
	}
	if selected, ok := mediaScopeCardFromPrompt(prompt, cards); ok {
		return selected, true
	}
	for _, card := range cards {
		cardKey := normalizedMediaSelection(card.Title)
		searchKey := normalizedMediaSelection(card.SearchText)
		if strings.Contains(cardKey, promptKey) || strings.Contains(promptKey, cardKey) ||
			(searchKey != "" && (strings.Contains(searchKey, promptKey) || strings.Contains(promptKey, searchKey))) {
			return card, true
		}
	}
	return assistantResultCard{}, false
}

func mediaScopeCardFromPrompt(prompt string, cards []assistantResultCard) (assistantResultCard, bool) {
	base, ok := firstMediaInventoryCard(cards)
	if !ok {
		return assistantResultCard{}, false
	}
	promptKey := normalizedMediaSelection(prompt)
	switch promptKey {
	case "all", "all episodes", "all seasons", "everything":
		if card, ok := findMediaFilterCard(cards, protocol.MediaEpisodeFilter{All: true}); ok {
			return card, true
		}
		return mediaScopedCard(base, protocol.MediaEpisodeFilter{All: true}, "All seasons"), true
	}

	season, episode := mediaScopeNumbers(prompt)
	if season == 0 && episode == 0 {
		return assistantResultCard{}, false
	}
	filter := protocol.MediaEpisodeFilter{Season: season, Episode: episode}
	if card, ok := findMediaFilterCard(cards, filter); ok {
		return card, true
	}
	title := ""
	switch {
	case season > 0 && episode > 0:
		title = fmt.Sprintf("Season %d Episode %d", season, episode)
	case season > 0:
		title = fmt.Sprintf("Season %d", season)
	case episode > 0:
		title = fmt.Sprintf("Episode %d", episode)
	}
	return mediaScopedCard(base, filter, title), true
}

func firstMediaInventoryCard(cards []assistantResultCard) (assistantResultCard, bool) {
	for _, card := range cards {
		if card.MediaFilter != nil && card.MediaType == protocol.MediaTypeSeries {
			return card, true
		}
	}
	return assistantResultCard{}, false
}

func findMediaFilterCard(cards []assistantResultCard, filter protocol.MediaEpisodeFilter) (assistantResultCard, bool) {
	for _, card := range cards {
		if card.MediaFilter == nil {
			continue
		}
		if card.MediaFilter.Season == filter.Season && card.MediaFilter.Episode == filter.Episode && card.MediaFilter.All == filter.All {
			return card, true
		}
	}
	return assistantResultCard{}, false
}

func mediaScopeNumbers(prompt string) (int, int) {
	if match := mediaCompactSxePattern.FindStringSubmatch(prompt); len(match) == 3 {
		season, _ := strconv.Atoi(match[1])
		episode, _ := strconv.Atoi(match[2])
		return season, episode
	}
	season := 0
	episode := 0
	if match := mediaSeasonPattern.FindStringSubmatch(prompt); len(match) == 2 {
		season, _ = strconv.Atoi(match[1])
	}
	if match := mediaEpisodePattern.FindStringSubmatch(prompt); len(match) == 2 {
		episode, _ = strconv.Atoi(match[1])
	}
	return season, episode
}

func mediaScopedCard(base assistantResultCard, filter protocol.MediaEpisodeFilter, title string) assistantResultCard {
	filterCopy := filter
	if title == "" {
		title = base.Title
	}
	return assistantResultCard{
		Kind:          "media",
		Title:         title,
		Summary:       base.Summary,
		ActionTitle:   "Choose",
		Path:          base.Path,
		SearchText:    base.SearchText,
		ImageURL:      base.ImageURL,
		MediaOptionID: base.MediaOptionID,
		MediaType:     protocol.MediaTypeSeries,
		MediaFilter:   &filterCopy,
		EpisodeCount:  base.EpisodeCount,
		Year:          base.Year,
	}
}

func (s *Server) latestMediaCards(ctx context.Context, sessionID string) []assistantResultCard {
	messages, err := s.store.ListAssistantMessages(ctx, sessionID)
	if err != nil {
		return nil
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != assistantRoleAssistant {
			continue
		}
		var content assistantMessageContent
		if err := json.Unmarshal([]byte(messages[i].ContentJSON), &content); err != nil {
			continue
		}
		var cards []assistantResultCard
		for _, card := range content.Cards {
			if card.Kind == "media" && card.Path != "" && card.JobID == "" {
				cards = append(cards, card)
			}
		}
		if len(cards) > 0 {
			return cards
		}
	}
	return nil
}

func assistantMediaCard(result protocol.MediaSearchResult, _ int) assistantResultCard {
	title := result.Title
	if result.Year > 0 {
		title = fmt.Sprintf("%s (%d)", result.Title, result.Year)
	}
	summary := mediaResultSummary(result)
	return assistantResultCard{
		Kind:          "media",
		Title:         title,
		Summary:       summary,
		ActionTitle:   "Choose",
		Path:          result.PagePath,
		SearchText:    result.SearchText,
		ImageURL:      result.PosterURL,
		MediaOptionID: result.ID,
		MediaType:     result.Type,
		Year:          result.Year,
	}
}

func mediaResultSummary(result protocol.MediaSearchResult) string {
	parts := []string{}
	switch result.Type {
	case protocol.MediaTypeMovie:
		parts = append(parts, "Movie")
	case protocol.MediaTypeSeries:
		parts = append(parts, "TV show")
	}
	if result.Summary != "" {
		parts = append(parts, result.Summary)
	}
	if result.Rating != "" {
		parts = append(parts, "Rating "+result.Rating)
	}
	return strings.Join(parts, " | ")
}

func mediaCardLine(card assistantResultCard) string {
	summary := strings.TrimSpace(card.Summary)
	if summary == "" {
		return card.Title
	}
	return fmt.Sprintf("%s - %s", card.Title, summary)
}

func mediaPlanText(plan protocol.MediaDownloadPlan) string {
	title := firstNonBlank(plan.Selection.Title, "Selected media")
	if plan.Selection.Type == protocol.MediaTypeSeries {
		scope := mediaFilterSummary(plan.Filter)
		if scope != "" {
			return fmt.Sprintf("I found `%s` and prepared %s: %d available episode(s) for download. %d will use 1080p and %d will fall back to 720p. Confirm before I start the batch.", title, strings.ToLower(scope), plan.ItemCount, plan.PreferredQualityCount, plan.FallbackQualityCount)
		}
		return fmt.Sprintf("I found `%s` and prepared %d available episode(s) for download. %d will use 1080p and %d will fall back to 720p. Confirm before I start the batch.", title, plan.ItemCount, plan.PreferredQualityCount, plan.FallbackQualityCount)
	}
	return fmt.Sprintf("I found `%s` and prepared it for download. Confirm before I start the download.", title)
}

func mediaConfirmationText(plan protocol.MediaDownloadPlan) string {
	title := firstNonBlank(plan.Selection.Title, "selected media")
	if plan.Selection.Type == protocol.MediaTypeSeries {
		if scope := mediaFilterSummary(plan.Filter); scope != "" {
			return fmt.Sprintf("Confirm downloading %s of `%s` to the Media share root.", strings.ToLower(scope), title)
		}
		return fmt.Sprintf("Confirm downloading %d episode(s) of `%s` to the Media share root.", plan.ItemCount, title)
	}
	return fmt.Sprintf("Confirm downloading `%s` to the Media share root.", title)
}

func mediaFilterSummary(filter protocol.MediaEpisodeFilter) string {
	switch {
	case filter.Season > 0 && filter.Episode > 0:
		return fmt.Sprintf("Season %d Episode %d", filter.Season, filter.Episode)
	case filter.Season > 0:
		return fmt.Sprintf("Season %d", filter.Season)
	case filter.All:
		return "All seasons"
	default:
		return ""
	}
}

func normalizedMediaSelection(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "option ")
	var builder strings.Builder
	lastSpace := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			builder.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(builder.String())
}
