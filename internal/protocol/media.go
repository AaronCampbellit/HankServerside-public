package protocol

import "time"

const (
	CommandMediaSearch         = "media.search"
	CommandMediaPlanDownload   = "media.plan_download"
	CommandMediaDownloadStart  = "media.download_start"
	CommandMediaDownloadStatus = "media.download_status"
	CommandMediaSettingsStatus = "media.settings_status"
	CommandMediaSettingsApply  = "media.settings_apply"
	CommandMediaDownloadJobs   = "media.download_jobs"
	CommandMediaDownloadCancel = "media.download_cancel"
	CommandMediaImageFetch     = "media.image_fetch"

	MediaTypeMovie  = "movie"
	MediaTypeSeries = "series"

	MediaJobStatusQueued    = "queued"
	MediaJobStatusRunning   = "running"
	MediaJobStatusCompleted = "completed"
	MediaJobStatusFailed    = "failed"
	MediaJobStatusCancelled = "cancelled"

	MediaDownloadModeSingle = "single"
	MediaDownloadModeRange  = "range"
)

type MediaSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type MediaSettings struct {
	Enabled              bool   `json:"enabled"`
	BaseURL              string `json:"base_url"`
	Username             string `json:"username,omitempty"`
	HasPassword          bool   `json:"has_password"`
	SourceID             string `json:"source_id,omitempty"`
	DestinationPath      string `json:"destination_path,omitempty"`
	MovieDestinationPath string `json:"movie_destination_path,omitempty"`
	TVDestinationPath    string `json:"tv_destination_path,omitempty"`
	PreferredQuality     string `json:"preferred_quality"`
	RequireConfirmation  bool   `json:"require_confirmation"`
}

type MediaDestinationOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	SourceID string `json:"source_id,omitempty"`
}

type MediaSettingsStatusRequest struct{}

type MediaSettingsStatusResponse struct {
	Settings           MediaSettings            `json:"settings"`
	DestinationOptions []MediaDestinationOption `json:"destination_options,omitempty"`
	Jobs               []MediaDownloadJobStatus `json:"jobs"`
}

type MediaSettingsApplyRequest struct {
	Settings MediaSettings `json:"settings"`
	Password string        `json:"password,omitempty"`
	Persist  bool          `json:"persist"`
}

type MediaSettingsApplyResponse struct {
	Settings MediaSettings `json:"settings"`
}

type MediaSearchResult struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	Type       string   `json:"type"`
	Summary    string   `json:"summary,omitempty"`
	Rating     string   `json:"rating,omitempty"`
	Genres     []string `json:"genres,omitempty"`
	PosterURL  string   `json:"poster_url,omitempty"`
	PagePath   string   `json:"page_path"`
	SearchText string   `json:"search_text,omitempty"`
}

type MediaSearchResponse struct {
	Query   string              `json:"query"`
	Results []MediaSearchResult `json:"results"`
}

type MediaEpisodeFilter struct {
	Season  int  `json:"season,omitempty"`
	Episode int  `json:"episode,omitempty"`
	All     bool `json:"all,omitempty"`
}

type MediaEpisodeEntry struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Title   string `json:"title,omitempty"`
}

type MediaSeasonSummary struct {
	Season       int                 `json:"season"`
	EpisodeCount int                 `json:"episode_count"`
	Episodes     []MediaEpisodeEntry `json:"episodes,omitempty"`
}

type MediaPlanDownloadRequest struct {
	Selection MediaSearchResult  `json:"selection"`
	Filter    MediaEpisodeFilter `json:"filter,omitempty"`
}

type MediaDownloadItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	MediaType   string `json:"media_type"`
	Season      int    `json:"season,omitempty"`
	Episode     int    `json:"episode,omitempty"`
	Quality     string `json:"quality,omitempty"`
	Filename    string `json:"filename"`
	PagePath    string `json:"page_path"`
	Existing    bool   `json:"existing,omitempty"`
	DownloadOK  bool   `json:"download_ok"`
	ErrorReason string `json:"error_reason,omitempty"`
}

type MediaDownloadPlan struct {
	Selection             MediaSearchResult    `json:"selection"`
	Filter                MediaEpisodeFilter   `json:"filter,omitempty"`
	Seasons               []MediaSeasonSummary `json:"seasons,omitempty"`
	Items                 []MediaDownloadItem  `json:"items"`
	ItemCount             int                  `json:"item_count"`
	PreferredQualityCount int                  `json:"preferred_quality_count"`
	FallbackQualityCount  int                  `json:"fallback_quality_count"`
	MissingLinkCount      int                  `json:"missing_link_count"`
	ExistingCount         int                  `json:"existing_count"`
	DestinationPath       string               `json:"destination_path"`
	RequireConfirmation   *bool                `json:"require_confirmation,omitempty"`
}

type MediaPlanDownloadResponse struct {
	Plan MediaDownloadPlan `json:"plan"`
}

type MediaDownloadStartRequest struct {
	Selection MediaSearchResult  `json:"selection"`
	Filter    MediaEpisodeFilter `json:"filter,omitempty"`
}

type MediaDownloadStartResponse struct {
	Job MediaDownloadJobStatus `json:"job"`
}

type MediaDownloadStatusRequest struct {
	JobID string `json:"job_id"`
}

type MediaDownloadStatusResponse struct {
	Job MediaDownloadJobStatus `json:"job"`
}

type MediaDownloadJobsRequest struct{}

type MediaDownloadJobsResponse struct {
	Jobs []MediaDownloadJobStatus `json:"jobs"`
}

type MediaDownloadCancelRequest struct {
	JobID string `json:"job_id"`
}

type MediaDownloadCancelResponse struct {
	Job MediaDownloadJobStatus `json:"job"`
}

type MediaImageFetchRequest struct {
	URL string `json:"url"`
}

type MediaImageFetchResponse struct {
	URL           string `json:"url"`
	ContentType   string `json:"content_type"`
	ContentBase64 string `json:"content_base64"`
}

type MediaDownloadJobStatus struct {
	JobID          string    `json:"job_id"`
	Status         string    `json:"status"`
	Title          string    `json:"title"`
	TotalCount     int       `json:"total_count"`
	CompletedCount int       `json:"completed_count"`
	SkippedCount   int       `json:"skipped_count"`
	FailedCount    int       `json:"failed_count"`
	CurrentIndex   int       `json:"current_index,omitempty"`
	CurrentFile    string    `json:"current_file,omitempty"`
	CurrentPath    string    `json:"current_path,omitempty"`
	CompletedPath  string    `json:"completed_path,omitempty"`
	BytesWritten   int64     `json:"bytes_written,omitempty"`
	BytesTotal     int64     `json:"bytes_total,omitempty"`
	DownloadMode   string    `json:"download_mode,omitempty"`
	Verification   string    `json:"verification_status,omitempty"`
	FallbackUsed   bool      `json:"fallback_used,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
}
