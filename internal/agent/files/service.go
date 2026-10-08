package files

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cloudsoda/go-smb2"
	"github.com/dropfile/HankServerside/internal/protocol"
)

var ErrDisabled = errors.New("files root is not configured")

const (
	LocalSourceID      = "local"
	DefaultSMBSourceID = "smb"

	fileSourceTypeLocal = "local"
	fileSourceTypeSMB   = "smb"
	fileSearchCacheTTL  = 5 * time.Minute
	fileSearchMaxItems  = 250000
	fileSearchMaxDirs   = 50000
)

type ReadHandle interface {
	io.Reader
	io.Closer
}

type WriteHandle interface {
	io.Writer
	io.Closer
}

type RandomWriteHandle interface {
	io.WriterAt
	io.Closer
	Truncate(size int64) error
}

type SMBConfig struct {
	ID       string
	Name     string
	Host     string
	Share    string
	Username string
	Password string
	Domain   string
	Policy   AccessPolicy
}

func (c SMBConfig) Enabled() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.Share) != ""
}

// LocalConfig describes a directory on the host that the agent serves as a file
// source. The directory may live anywhere on the host filesystem; access stays
// confined by the resolve helpers and rooted write operations.
type LocalConfig struct {
	ID     string
	Name   string
	Root   string
	Policy AccessPolicy
}

func (c LocalConfig) Enabled() bool {
	return strings.TrimSpace(c.Root) != ""
}

type Config struct {
	// Root configures the default local source (id "local"). It is kept for
	// backward compatibility with the single-root deployments; additional host
	// folders are configured through LocalSources.
	Root         string
	LocalSources []LocalConfig
	Shares       []SMBConfig
	Policy       AccessPolicy
}

type AccessPolicy struct {
	Read            *bool    `json:"read,omitempty"`
	Write           *bool    `json:"write,omitempty"`
	Delete          *bool    `json:"delete,omitempty"`
	AllowedPrefixes []string `json:"allowed_prefixes,omitempty"`
	BlockedPrefixes []string `json:"blocked_prefixes,omitempty"`
	MaxUploadBytes  int64    `json:"max_upload_bytes,omitempty"`
}

func (p AccessPolicy) HasRules() bool {
	return p.Read != nil || p.Write != nil || p.Delete != nil || len(p.AllowedPrefixes) > 0 || len(p.BlockedPrefixes) > 0 || p.MaxUploadBytes > 0
}

type SourceSnapshot struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Type             string       `json:"type"`
	Root             string       `json:"root,omitempty"`
	SMBHost          string       `json:"smb_host,omitempty"`
	SMBShare         string       `json:"smb_share,omitempty"`
	SMBUsername      string       `json:"smb_username,omitempty"`
	SMBDomain        string       `json:"smb_domain,omitempty"`
	SMBEnabled       bool         `json:"smb_enabled,omitempty"`
	SMBPasswordSet   bool         `json:"smb_password_set,omitempty"`
	LocalRootEnabled bool         `json:"local_root_enabled,omitempty"`
	Policy           AccessPolicy `json:"policy,omitempty"`
}

type smbShareSnapshot struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Host        string       `json:"host"`
	Share       string       `json:"share"`
	Username    string       `json:"username,omitempty"`
	Domain      string       `json:"domain,omitempty"`
	Enabled     bool         `json:"enabled"`
	PasswordSet bool         `json:"password_set"`
	Policy      AccessPolicy `json:"policy,omitempty"`
}

type localSourceSnapshot struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Root    string       `json:"root"`
	Enabled bool         `json:"enabled"`
	Policy  AccessPolicy `json:"policy,omitempty"`
}

type Service struct {
	mu             sync.RWMutex
	localSources   []LocalConfig
	smbShares      []SMBConfig
	smbConnections map[string]*smbConnection
	searchCacheMu  sync.Mutex
	searchCache    map[string]fileSearchCacheEntry
	searchCacheGen map[string]uint64
	searchBuilds   map[string]chan struct{}
	listPagesMu    sync.Mutex
	listPages      map[string]fileListPageSnapshot
}

type fileListPageSnapshot struct {
	sourceID string
	path     string
	items    []protocol.FileItem
	end      int
	expires  time.Time
}

type fileSearchCacheEntry struct {
	items     []protocol.FileItem
	createdAt time.Time
}

type fileSourceSelection struct {
	ID     string
	Type   string
	Root   string
	Policy AccessPolicy
}

func New(root string) *Service {
	return NewWithConfig(Config{Root: root})
}

func NewWithConfig(cfg Config) *Service {
	locals := make([]LocalConfig, 0, len(cfg.LocalSources)+1)
	if strings.TrimSpace(cfg.Root) != "" {
		locals = append(locals, LocalConfig{
			ID:     LocalSourceID,
			Name:   "Primary Hank Agent files",
			Root:   cfg.Root,
			Policy: cfg.Policy,
		})
	}
	locals = append(locals, cfg.LocalSources...)
	return &Service{
		localSources:   normalizeLocalConfigs(locals),
		smbShares:      normalizeSMBConfigs(cfg.Shares),
		smbConnections: make(map[string]*smbConnection),
		searchCache:    make(map[string]fileSearchCacheEntry),
		searchCacheGen: make(map[string]uint64),
		listPages:      make(map[string]fileListPageSnapshot),
	}
}

func (s *Service) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return hasEnabledLocalConfig(s.localSources) || hasEnabledSMBConfig(s.smbShares)
}

func (s *Service) defaultSourceLocked() (fileSourceSelection, error) {
	for _, cfg := range s.smbShares {
		if cfg.Enabled() {
			return fileSourceSelection{ID: cfg.ID, Type: fileSourceTypeSMB, Policy: cfg.Policy}, nil
		}
	}
	for _, cfg := range s.localSources {
		if cfg.Enabled() {
			return fileSourceSelection{ID: cfg.ID, Type: fileSourceTypeLocal, Root: cfg.Root, Policy: cfg.Policy}, nil
		}
	}
	return fileSourceSelection{}, ErrDisabled
}

func (s *Service) sourceForID(sourceID string) (fileSourceSelection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sourceID = cleanSourceID(sourceID)
	if sourceID == "" {
		return s.defaultSourceLocked()
	}
	for _, cfg := range s.localSources {
		if cfg.ID == sourceID {
			if !cfg.Enabled() {
				return fileSourceSelection{}, ErrDisabled
			}
			return fileSourceSelection{ID: cfg.ID, Type: fileSourceTypeLocal, Root: cfg.Root, Policy: cfg.Policy}, nil
		}
	}
	for _, cfg := range s.smbShares {
		if cfg.ID == sourceID {
			if !cfg.Enabled() {
				return fileSourceSelection{}, ErrDisabled
			}
			return fileSourceSelection{ID: sourceID, Type: fileSourceTypeSMB, Policy: cfg.Policy}, nil
		}
	}
	if sourceID == LocalSourceID {
		return fileSourceSelection{}, ErrDisabled
	}
	return fileSourceSelection{}, fmt.Errorf("file source %q is not configured", sourceID)
}

func (s *Service) ApplySMBConfig(cfg SMBConfig) {
	s.ApplySMBConfigs([]SMBConfig{cfg})
}

func (s *Service) ApplySMBConfigs(configs []SMBConfig) {
	next := normalizeSMBConfigs(configs)

	s.mu.Lock()
	defer s.mu.Unlock()

	existingPasswords := make(map[string]string, len(s.smbShares))
	for _, cfg := range s.smbShares {
		existingPasswords[cfg.ID] = cfg.Password
	}
	for i := range next {
		if next[i].Password == "" {
			next[i].Password = existingPasswords[next[i].ID]
		}
	}

	keep := make(map[string]SMBConfig, len(next))
	for _, cfg := range next {
		keep[cfg.ID] = cfg
	}
	for sourceID, conn := range s.smbConnections {
		cfg, ok := keep[sourceID]
		if !ok || !sameSMBConfig(conn.cfg, cfg) {
			_ = conn.close()
			delete(s.smbConnections, sourceID)
		}
	}
	s.smbShares = next
	s.invalidateAllSearchIndexes()
}

func (s *Service) SMBConfigs() []SMBConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSMBConfigs(s.smbShares)
}

// ApplyLocalConfigs replaces the configured host folder sources. Callers are
// responsible for validating and creating the directories before applying.
func (s *Service) ApplyLocalConfigs(configs []LocalConfig) {
	next := normalizeLocalConfigs(configs)
	s.mu.Lock()
	s.localSources = next
	s.mu.Unlock()
	s.invalidateAllSearchIndexes()
}

func (s *Service) LocalConfigs() []LocalConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneLocalConfigs(s.localSources)
}

func normalizeLocalConfigs(configs []LocalConfig) []LocalConfig {
	normalized := make([]LocalConfig, 0, len(configs))
	seen := map[string]int{}
	for index, cfg := range configs {
		cfg.Root = strings.TrimSpace(cfg.Root)
		cfg.Name = strings.TrimSpace(cfg.Name)

		fallbackID := LocalSourceID
		if index > 0 {
			fallbackID = fmt.Sprintf("%s-%d", LocalSourceID, index+1)
		}
		if cfg.ID == "" {
			cfg.ID = firstNonBlank(cfg.Name, filepath.Base(cfg.Root), fallbackID)
		}
		cfg.ID = cleanSourceID(cfg.ID)
		if cfg.ID == "" {
			cfg.ID = fallbackID
		}
		baseID := cfg.ID
		if count := seen[baseID]; count > 0 {
			cfg.ID = fmt.Sprintf("%s-%d", baseID, count+1)
		}
		seen[baseID]++
		if cfg.Name == "" {
			cfg.Name = firstNonBlank(filepath.Base(cfg.Root), cfg.ID)
		}
		normalized = append(normalized, cfg)
	}
	return normalized
}

func hasEnabledLocalConfig(configs []LocalConfig) bool {
	for _, cfg := range configs {
		if cfg.Enabled() {
			return true
		}
	}
	return false
}

func cloneLocalConfigs(configs []LocalConfig) []LocalConfig {
	cloned := make([]LocalConfig, len(configs))
	copy(cloned, configs)
	return cloned
}

func normalizeSMBConfigs(configs []SMBConfig) []SMBConfig {
	normalized := make([]SMBConfig, 0, len(configs))
	seen := map[string]int{}
	for index, cfg := range configs {
		cfg.Host = NormalizeSMBHost(cfg.Host)
		cfg.Share = strings.TrimSpace(cfg.Share)
		cfg.Username = strings.TrimSpace(cfg.Username)
		cfg.Domain = strings.TrimSpace(cfg.Domain)
		cfg.Name = strings.TrimSpace(cfg.Name)

		fallbackID := DefaultSMBSourceID
		if index > 0 {
			fallbackID = fmt.Sprintf("%s-%d", DefaultSMBSourceID, index+1)
		}
		if cfg.ID == "" {
			cfg.ID = firstNonBlank(cfg.Name, cfg.Share, fallbackID)
		}
		cfg.ID = cleanSourceID(cfg.ID)
		if cfg.ID == "" {
			cfg.ID = fallbackID
		}
		baseID := cfg.ID
		if count := seen[baseID]; count > 0 {
			cfg.ID = fmt.Sprintf("%s-%d", baseID, count+1)
		}
		seen[baseID]++
		if cfg.Name == "" {
			cfg.Name = cfg.Share
		}
		if cfg.Name == "" {
			cfg.Name = cfg.ID
		}
		normalized = append(normalized, cfg)
	}
	return normalized
}

func hasEnabledSMBConfig(configs []SMBConfig) bool {
	for _, cfg := range configs {
		if cfg.Enabled() {
			return true
		}
	}
	return false
}

func cloneSMBConfigs(configs []SMBConfig) []SMBConfig {
	cloned := make([]SMBConfig, len(configs))
	copy(cloned, configs)
	return cloned
}

func cleanSourceID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		case char == '_' || char == '-' || char == '.':
			builder.WriteRune(char)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func NormalizeSMBHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	host = strings.ReplaceAll(host, "\\", "/")

	if parsed, err := url.Parse(host); err == nil && parsed.Scheme != "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
			return strings.TrimSpace(parsed.Hostname())
		default:
			if parsed.Host != "" {
				return strings.TrimSpace(parsed.Host)
			}
		}
	}

	host = strings.TrimPrefix(host, "smb://")
	host = strings.TrimPrefix(host, "cifs://")
	host = strings.TrimLeft(host, "/")
	if slash := strings.Index(host, "/"); slash >= 0 {
		host = host[:slash]
	}
	return strings.TrimSpace(host)
}

func (s *Service) Snapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sources := s.sourceSnapshotsLocked()
	primary := SMBConfig{}
	if len(s.smbShares) > 0 {
		primary = s.smbShares[0]
	}
	primaryLocalRoot := ""
	if len(s.localSources) > 0 {
		primaryLocalRoot = s.localSources[0].Root
	}
	return map[string]any{
		"root":               primaryLocalRoot,
		"smb_host":           primary.Host,
		"smb_share":          primary.Share,
		"smb_username":       primary.Username,
		"smb_domain":         primary.Domain,
		"smb_enabled":        primary.Enabled(),
		"smb_password_set":   primary.Password != "",
		"local_root_enabled": hasEnabledLocalConfig(s.localSources),
		"active_source_id":   defaultSourceID(sources),
		"file_sources":       sources,
		"sources":            sources,
		"shares":             s.smbShareSnapshotsLocked(),
		"folders":            s.localSourceSnapshotsLocked(),
	}
}

func (s *Service) SearchSources() []protocol.FileSourceInfo {
	return s.SearchSourcesWithRevision("")
}

func (s *Service) SearchSourcesWithRevision(key string) []protocol.FileSourceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sources := make([]protocol.FileSourceInfo, 0, len(s.smbShares)+len(s.localSources))
	for _, cfg := range s.smbShares {
		info := searchSourceInfo(cfg.ID, cfg.Name, cfg.Enabled(), cfg.Policy)
		info.Revision = fileSourceRevision(key, "smb", cfg.ID, cfg.Host, cfg.Share, cfg.Domain, cfg.Username, cfg.Password)
		sources = append(sources, info)
	}
	for _, cfg := range s.localSources {
		info := searchSourceInfo(cfg.ID, cfg.Name, cfg.Enabled(), cfg.Policy)
		info.Revision = fileSourceRevision(key, "local", cfg.ID, cfg.Root)
		sources = append(sources, info)
	}
	return sources
}

func fileSourceRevision(key string, fields ...string) string {
	mac := hmac.New(sha256.New, []byte(key))
	for _, field := range fields {
		_, _ = mac.Write([]byte(field))
		_, _ = mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func (s *Service) SearchDefaultSourceID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source, err := s.defaultSourceLocked()
	if err != nil {
		return ""
	}
	return source.ID
}

func searchSourceInfo(id, name string, enabled bool, policy AccessPolicy) protocol.FileSourceInfo {
	readable := policy.Read == nil || *policy.Read
	return protocol.FileSourceInfo{
		ID: id, Name: name, Enabled: enabled, Readable: readable,
		AllowedPrefixes: append([]string(nil), policy.AllowedPrefixes...),
		BlockedPrefixes: append([]string(nil), policy.BlockedPrefixes...),
	}
}

func (s *Service) sourceSnapshotsLocked() []SourceSnapshot {
	sources := make([]SourceSnapshot, 0, len(s.smbShares)+len(s.localSources))
	for _, cfg := range s.smbShares {
		sources = append(sources, SourceSnapshot{
			ID:             cfg.ID,
			Name:           cfg.Name,
			Type:           fileSourceTypeSMB,
			SMBHost:        cfg.Host,
			SMBShare:       cfg.Share,
			SMBUsername:    cfg.Username,
			SMBDomain:      cfg.Domain,
			SMBEnabled:     cfg.Enabled(),
			SMBPasswordSet: cfg.Password != "",
			Policy:         cfg.Policy,
		})
	}
	for _, cfg := range s.localSources {
		sources = append(sources, SourceSnapshot{
			ID:               cfg.ID,
			Name:             firstNonBlank(cfg.Name, cfg.ID),
			Type:             fileSourceTypeLocal,
			Root:             cfg.Root,
			LocalRootEnabled: cfg.Enabled(),
			Policy:           cfg.Policy,
		})
	}
	return sources
}

func (s *Service) localSourceSnapshotsLocked() []localSourceSnapshot {
	folders := make([]localSourceSnapshot, 0, len(s.localSources))
	for _, cfg := range s.localSources {
		folders = append(folders, localSourceSnapshot{
			ID:      cfg.ID,
			Name:    firstNonBlank(cfg.Name, cfg.ID),
			Root:    cfg.Root,
			Enabled: cfg.Enabled(),
			Policy:  cfg.Policy,
		})
	}
	return folders
}

func (s *Service) smbShareSnapshotsLocked() []smbShareSnapshot {
	shares := make([]smbShareSnapshot, 0, len(s.smbShares))
	for _, cfg := range s.smbShares {
		shares = append(shares, smbShareSnapshot{
			ID:          cfg.ID,
			Name:        cfg.Name,
			Host:        cfg.Host,
			Share:       cfg.Share,
			Username:    cfg.Username,
			Domain:      cfg.Domain,
			Enabled:     cfg.Enabled(),
			PasswordSet: cfg.Password != "",
			Policy:      cfg.Policy,
		})
	}
	return shares
}

func defaultSourceID(sources []SourceSnapshot) string {
	for _, source := range sources {
		if source.Type == fileSourceTypeSMB && source.SMBEnabled {
			return source.ID
		}
	}
	for _, source := range sources {
		if source.Type == fileSourceTypeLocal && source.LocalRootEnabled {
			return source.ID
		}
	}
	return ""
}

func (s fileSourceSelection) authorize(action string, path string, size int64) error {
	return s.Policy.allow(action, path, size)
}

func (p AccessPolicy) allow(action string, rawPath string, size int64) error {
	switch action {
	case "read":
		if p.Read != nil && !*p.Read {
			return errors.New("file source policy denies read")
		}
	case "write":
		if p.Write != nil && !*p.Write {
			return errors.New("file source policy denies write")
		}
		if p.MaxUploadBytes > 0 && size > p.MaxUploadBytes {
			return errors.New("file source policy upload size limit exceeded")
		}
	case "delete":
		if p.Delete != nil && !*p.Delete {
			return errors.New("file source policy denies delete")
		}
	}
	path := cleanPolicyPath(rawPath)
	for _, prefix := range p.BlockedPrefixes {
		if pathHasPolicyPrefix(path, prefix) {
			return errors.New("file source policy blocks this path")
		}
	}
	if len(p.AllowedPrefixes) > 0 {
		for _, prefix := range p.AllowedPrefixes {
			if pathHasPolicyPrefix(path, prefix) {
				return nil
			}
		}
		return errors.New("file source policy does not allow this path")
	}
	return nil
}

func cleanPolicyPath(value string) string {
	value = filepath.ToSlash(filepath.Clean("/" + strings.TrimSpace(value)))
	return strings.TrimSuffix(value, "/")
}

func pathHasPolicyPrefix(path string, prefix string) bool {
	prefix = cleanPolicyPath(prefix)
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func decodedBase64Size(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	size := base64.StdEncoding.DecodedLen(len(value))
	for strings.HasSuffix(value, "=") && size > 0 {
		size--
		value = strings.TrimSuffix(value, "=")
	}
	return int64(size)
}

func (s *Service) List(ctx context.Context, path string) ([]protocol.FileItem, error) {
	return s.ListSource(ctx, "", path)
}

func (s *Service) ListSource(ctx context.Context, sourceID string, path string) ([]protocol.FileItem, error) {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return nil, err
	}
	if err := source.authorize("read", path, 0); err != nil {
		return nil, err
	}
	var items []protocol.FileItem
	if source.Type == fileSourceTypeSMB {
		items, err = s.listSMB(ctx, source.ID, path)
	} else {
		items, err = s.listLocal(ctx, source.Root, path)
	}
	if err != nil {
		return nil, err
	}
	return decorateFileItemsSource(items, source.ID), nil
}

// ListPageSource bounds agent-to-cloud frames while reading a directory only
// once. The cursor is scoped to its source and path and expires after 5 minutes.
func (s *Service) ListPageSource(ctx context.Context, sourceID, path, cursor string) (protocol.FilesListPageResponse, error) {
	const maxPageBytes = 768 * 1024
	const maxPageItems = 500
	if cursor == "" {
		items, err := s.ListSource(ctx, sourceID, path)
		if err != nil {
			return protocol.FilesListPageResponse{}, err
		}
		end := fileListPageEnd(items, 0, maxPageItems, maxPageBytes)
		result := protocol.FilesListPageResponse{Items: items[:end]}
		if end == len(items) {
			return result, nil
		}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return protocol.FilesListPageResponse{}, err
		}
		cursor = hex.EncodeToString(token[:])
		s.listPagesMu.Lock()
		for key, snapshot := range s.listPages {
			if time.Now().After(snapshot.expires) {
				delete(s.listPages, key)
			}
		}
		s.listPages[cursor] = fileListPageSnapshot{sourceID: sourceID, path: path, items: items, end: end, expires: time.Now().Add(5 * time.Minute)}
		s.listPagesMu.Unlock()
		result.NextCursor = cursor
		return result, nil
	}
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return protocol.FilesListPageResponse{}, err
	}
	if err := source.authorize("read", path, 0); err != nil {
		return protocol.FilesListPageResponse{}, err
	}
	s.listPagesMu.Lock()
	defer s.listPagesMu.Unlock()
	snapshot, ok := s.listPages[cursor]
	if !ok || snapshot.sourceID != sourceID || snapshot.path != path || time.Now().After(snapshot.expires) {
		if ok && time.Now().After(snapshot.expires) {
			delete(s.listPages, cursor)
		}
		return protocol.FilesListPageResponse{}, errors.New("file listing cursor expired")
	}
	end := fileListPageEnd(snapshot.items, snapshot.end, maxPageItems, maxPageBytes)
	result := protocol.FilesListPageResponse{Items: snapshot.items[snapshot.end:end]}
	if end == len(snapshot.items) {
		delete(s.listPages, cursor)
	} else {
		snapshot.end = end
		snapshot.expires = time.Now().Add(5 * time.Minute)
		s.listPages[cursor] = snapshot
		result.NextCursor = cursor
	}
	return result, nil
}

func fileListPageEnd(items []protocol.FileItem, start, maxItems, maxBytes int) int {
	used, end := 0, start
	for end < len(items) && end-start < maxItems {
		// Allow room for JSON fields, escaping and the surrounding envelope.
		cost := 256 + 6*(len(items[end].Path)+len(items[end].Name)+len(items[end].SourceID))
		if end > start && used+cost > maxBytes {
			break
		}
		used += cost
		end++
	}
	return end
}

func (s *Service) Stat(ctx context.Context, path string) (protocol.FileItem, error) {
	return s.StatSource(ctx, "", path)
}

func (s *Service) StatSource(ctx context.Context, sourceID string, path string) (protocol.FileItem, error) {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return protocol.FileItem{}, err
	}
	if err := source.authorize("read", path, 0); err != nil {
		return protocol.FileItem{}, err
	}
	var item protocol.FileItem
	if source.Type == fileSourceTypeSMB {
		item, err = s.statSMB(ctx, source.ID, path)
	} else {
		item, err = s.statLocal(ctx, source.Root, path)
	}
	if err != nil {
		return protocol.FileItem{}, err
	}
	item.SourceID = source.ID
	return item, nil
}

func (s *Service) Search(ctx context.Context, query string, limit int) ([]protocol.FileItem, error) {
	return s.SearchSource(ctx, "", query, limit)
}

func (s *Service) SearchSource(ctx context.Context, sourceID string, query string, limit int) ([]protocol.FileItem, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	source, err := s.sourceForID(sourceID)
	if err != nil {
		return nil, err
	}
	items, err := s.searchIndex(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	matches := make([]protocol.FileItem, 0)
	for _, item := range items {
		if fileSearchScore(item, query) > 0 {
			matches = append(matches, item)
		}
	}

	sort.Slice(matches, func(i int, j int) bool {
		left := fileSearchScore(matches[i], query)
		right := fileSearchScore(matches[j], query)
		if left == right {
			return matches[i].Path < matches[j].Path
		}
		return left > right
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func (s *Service) searchIndex(ctx context.Context, sourceID string) ([]protocol.FileItem, error) {
	var generation uint64
	for {
		now := time.Now()
		s.searchCacheMu.Lock()
		entry, ok := s.searchCache[sourceID]
		if ok && now.Sub(entry.createdAt) < fileSearchCacheTTL {
			s.searchCacheMu.Unlock()
			return append([]protocol.FileItem(nil), entry.items...), nil
		}
		if build := s.searchBuilds[sourceID]; build != nil {
			s.searchCacheMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-build:
				continue
			}
		}
		if s.searchBuilds == nil {
			s.searchBuilds = make(map[string]chan struct{})
		}
		build := make(chan struct{})
		s.searchBuilds[sourceID] = build
		generation = s.searchCacheGen[sourceID]
		s.searchCacheMu.Unlock()
		defer func() {
			s.searchCacheMu.Lock()
			delete(s.searchBuilds, sourceID)
			close(build)
			s.searchCacheMu.Unlock()
		}()
		break
	}

	source, err := s.sourceForID(sourceID)
	if err != nil {
		return nil, err
	}
	if source.Policy.Read != nil && !*source.Policy.Read {
		return nil, errors.New("file source policy denies read")
	}
	queue := []string{""}
	items := make([]protocol.FileItem, 0)
	if len(source.Policy.AllowedPrefixes) > 0 {
		queue = make([]string, 0, len(source.Policy.AllowedPrefixes))
		for _, prefix := range source.Policy.AllowedPrefixes {
			root := cleanPath(prefix)
			if source.authorize("read", root, 0) != nil {
				continue
			}
			item, err := s.StatSource(ctx, sourceID, root)
			if err != nil {
				return nil, err
			}
			if item.IsDirectory {
				queue = append(queue, root)
			} else {
				items = append(items, item)
			}
		}
	}
	items, err = walkSearchIndex(ctx, source, queue, items, func(ctx context.Context, path string) ([]protocol.FileItem, error) {
		return s.ListSource(ctx, sourceID, path)
	})
	if err != nil {
		return nil, err
	}
	s.storeSearchIndexIfCurrent(sourceID, generation, items, time.Now())
	return items, nil
}

func walkSearchIndex(ctx context.Context, source fileSourceSelection, queue []string, items []protocol.FileItem, list func(context.Context, string) ([]protocol.FileItem, error)) ([]protocol.FileItem, error) {
	visited := make(map[string]struct{})
	for len(queue) > 0 {
		batchSize := min(len(queue), 8)
		batch := queue[:batchSize]
		queue = queue[batchSize:]
		results := make([][]protocol.FileItem, batchSize)
		errorsByDirectory := make([]error, batchSize)
		group, groupCtx := errgroup.WithContext(ctx)
		for i, current := range batch {
			if source.authorize("read", current, 0) != nil {
				continue
			}
			key := cleanPath(current)
			if _, seen := visited[key]; seen {
				continue
			}
			if len(visited) >= fileSearchMaxDirs {
				return nil, fmt.Errorf("file search index exceeds %d directories", fileSearchMaxDirs)
			}
			visited[key] = struct{}{}
			group.Go(func() error {
				children, err := list(groupCtx, current)
				results[i] = children
				errorsByDirectory[i] = err
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i, children := range results {
			if err := errorsByDirectory[i]; err != nil {
				if batch[i] == "" || isSearchConnectionError(err) {
					return nil, err
				}
				// A single unreadable folder must not hide results in other folders.
				continue
			}
			permitted := children[:0]
			for _, child := range children {
				if source.authorize("read", child.Path, 0) == nil {
					permitted = append(permitted, child)
				}
			}
			if len(items)+len(permitted) > fileSearchMaxItems {
				return nil, fmt.Errorf("file search index exceeds %d items", fileSearchMaxItems)
			}
			items = append(items, permitted...)
			for _, item := range permitted {
				if item.IsDirectory && !isSearchMetadataDirectory(item.Path) {
					queue = append(queue, item.Path)
				}
			}
		}
	}
	return items, nil
}

func isSearchMetadataDirectory(path string) bool {
	return strings.EqualFold(filepath.Base(filepath.ToSlash(path)), ".git")
}

func isSearchConnectionError(err error) bool {
	if errors.Is(err, ErrDisabled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var smbTransport *smb2.TransportError
	if errors.As(err, &smbTransport) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func (s *Service) searchIndexGeneration(sourceID string) uint64 {
	s.searchCacheMu.Lock()
	defer s.searchCacheMu.Unlock()
	return s.searchCacheGen[sourceID]
}

func (s *Service) storeSearchIndexIfCurrent(sourceID string, generation uint64, items []protocol.FileItem, createdAt time.Time) {
	s.searchCacheMu.Lock()
	defer s.searchCacheMu.Unlock()
	if s.searchCacheGen[sourceID] != generation {
		return
	}
	s.searchCache[sourceID] = fileSearchCacheEntry{items: append([]protocol.FileItem(nil), items...), createdAt: createdAt}
}

func (s *Service) invalidateSearchIndex(sourceID string) {
	s.searchCacheMu.Lock()
	delete(s.searchCache, sourceID)
	s.searchCacheGen[sourceID]++
	s.searchCacheMu.Unlock()
}

func (s *Service) invalidateAllSearchIndexes() {
	s.searchCacheMu.Lock()
	clear(s.searchCache)
	for sourceID := range s.searchCacheGen {
		s.searchCacheGen[sourceID]++
	}
	s.searchCacheMu.Unlock()
}

func (s *Service) CreateDirectory(ctx context.Context, path string) error {
	return s.CreateDirectorySource(ctx, "", path)
}

func (s *Service) CreateDirectorySource(ctx context.Context, sourceID string, path string) error {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return err
	}
	if err := source.authorize("write", path, 0); err != nil {
		return err
	}
	var createErr error
	if source.Type == fileSourceTypeSMB {
		createErr = s.createDirectorySMB(ctx, source.ID, path)
	} else {
		createErr = s.createDirectoryLocal(ctx, source.Root, path)
	}
	if createErr == nil {
		s.invalidateSearchIndex(source.ID)
	}
	return createErr
}

func (s *Service) Rename(ctx context.Context, from string, to string) error {
	return s.RenameSource(ctx, "", from, to)
}

func (s *Service) RenameSource(ctx context.Context, sourceID string, from string, to string) error {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return err
	}
	if err := source.authorize("write", from, 0); err != nil {
		return err
	}
	if err := source.authorize("write", to, 0); err != nil {
		return err
	}
	var renameErr error
	if source.Type == fileSourceTypeSMB {
		renameErr = s.renameSMB(ctx, source.ID, from, to)
	} else {
		renameErr = s.renameLocal(ctx, source.Root, from, to)
	}
	if renameErr == nil {
		s.invalidateSearchIndex(source.ID)
	}
	return renameErr
}

func (s *Service) MoveBetweenSources(ctx context.Context, sourceID string, destinationSourceID string, from string, to string, isDirectory bool) error {
	_, err := s.MoveBetweenSourcesWithProgress(ctx, sourceID, destinationSourceID, from, to, isDirectory, nil)
	if err == nil {
		if source, sourceErr := s.sourceForID(sourceID); sourceErr == nil {
			s.invalidateSearchIndex(source.ID)
		}
		if destination, destinationErr := s.sourceForID(destinationSourceID); destinationErr == nil {
			s.invalidateSearchIndex(destination.ID)
		}
	}
	return err
}

func (s *Service) copyBetweenSources(ctx context.Context, sourceID string, destinationSourceID string, from string, to string, isDirectory bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if isDirectory {
		if err := s.CreateDirectorySource(ctx, destinationSourceID, to); err != nil {
			return err
		}
		items, err := s.ListSource(ctx, sourceID, from)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := s.copyBetweenSources(ctx, sourceID, destinationSourceID, item.Path, filepath.ToSlash(filepath.Join(to, item.Name)), item.IsDirectory); err != nil {
				return err
			}
		}
		return nil
	}

	reader, _, err := s.OpenReaderSource(ctx, sourceID, from, 0)
	if err != nil {
		return err
	}
	defer reader.Close()

	writer, _, err := s.OpenWriterSource(ctx, destinationSourceID, to, 0)
	if err != nil {
		return err
	}
	if _, err := io.Copy(writer, reader); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return s.verifyCopiedFile(ctx, sourceID, destinationSourceID, from, to)
}

func (s *Service) verifyCopiedFile(ctx context.Context, sourceID string, destinationSourceID string, from string, to string) error {
	sourceInfo, err := s.StatSource(ctx, sourceID, from)
	if err != nil {
		return err
	}
	destinationInfo, err := s.StatSource(ctx, destinationSourceID, to)
	if err != nil {
		return err
	}
	if sourceInfo.IsDirectory || destinationInfo.IsDirectory {
		return fmt.Errorf("copy verification expected files")
	}
	if sourceInfo.Size != destinationInfo.Size {
		return fmt.Errorf("copy verification failed: size mismatch")
	}
	sourceHash, err := s.fileHash(ctx, sourceID, from)
	if err != nil {
		return err
	}
	destinationHash, err := s.fileHash(ctx, destinationSourceID, to)
	if err != nil {
		return err
	}
	if sourceHash != destinationHash {
		return fmt.Errorf("copy verification failed: checksum mismatch")
	}
	return nil
}

func (s *Service) fileHash(ctx context.Context, sourceID string, filePath string) (string, error) {
	reader, _, err := s.OpenReaderSource(ctx, sourceID, filePath, 0)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (s *Service) Delete(ctx context.Context, path string, isDirectory bool) error {
	return s.DeleteSource(ctx, "", path, isDirectory)
}

func (s *Service) DeleteSource(ctx context.Context, sourceID string, path string, isDirectory bool) error {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return err
	}
	if err := source.authorize("delete", path, 0); err != nil {
		return err
	}
	var deleteErr error
	if source.Type == fileSourceTypeSMB {
		deleteErr = s.deleteSMB(ctx, source.ID, path, isDirectory)
	} else {
		deleteErr = s.deleteLocal(ctx, source.Root, path, isDirectory)
	}
	if deleteErr == nil {
		s.invalidateSearchIndex(source.ID)
	}
	return deleteErr
}

func (s *Service) RollbackMoveDestination(ctx context.Context, destinationSourceID string, to string, isDirectory bool) error {
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("rollback destination path is required")
	}
	if _, err := s.StatSource(ctx, destinationSourceID, to); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return s.DeleteSource(ctx, destinationSourceID, to, isDirectory)
}

func (s *Service) Download(ctx context.Context, path string) (string, error) {
	return s.DownloadSource(ctx, "", path)
}

func (s *Service) DownloadSource(ctx context.Context, sourceID string, path string) (string, error) {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return "", err
	}
	if err := source.authorize("read", path, 0); err != nil {
		return "", err
	}
	if source.Type == fileSourceTypeSMB {
		return s.downloadSMB(ctx, source.ID, path)
	}
	return s.downloadLocal(ctx, source.Root, path)
}

func (s *Service) Upload(ctx context.Context, path string, contentBase64 string) error {
	return s.UploadSource(ctx, "", path, contentBase64)
}

func (s *Service) UploadSource(ctx context.Context, sourceID string, path string, contentBase64 string) error {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return err
	}
	if err := source.authorize("write", path, decodedBase64Size(contentBase64)); err != nil {
		return err
	}
	var uploadErr error
	if source.Type == fileSourceTypeSMB {
		uploadErr = s.uploadSMB(ctx, source.ID, path, contentBase64)
	} else {
		uploadErr = s.uploadLocal(ctx, source.Root, path, contentBase64)
	}
	if uploadErr == nil {
		s.invalidateSearchIndex(source.ID)
	}
	return uploadErr
}

func (s *Service) OpenReader(ctx context.Context, path string, offset int64) (ReadHandle, fs.FileInfo, error) {
	return s.OpenReaderSource(ctx, "", path, offset)
}

func (s *Service) OpenReaderSource(ctx context.Context, sourceID string, path string, offset int64) (ReadHandle, fs.FileInfo, error) {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return nil, nil, err
	}
	if err := source.authorize("read", path, 0); err != nil {
		return nil, nil, err
	}
	if source.Type == fileSourceTypeSMB {
		return s.openReaderSMB(ctx, source.ID, path, offset)
	}
	return s.openReaderLocal(ctx, source.Root, path, offset)
}

func (s *Service) OpenWriter(ctx context.Context, path string, offset int64) (WriteHandle, int64, error) {
	return s.OpenWriterSource(ctx, "", path, offset)
}

func (s *Service) OpenWriterSource(ctx context.Context, sourceID string, path string, offset int64) (WriteHandle, int64, error) {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return nil, 0, err
	}
	if err := source.authorize("write", path, offset); err != nil {
		return nil, 0, err
	}
	maxBytes := source.Policy.MaxUploadBytes
	if source.Type == fileSourceTypeSMB {
		writer, size, err := s.openWriterSMB(ctx, source.ID, path, offset)
		if err != nil {
			return nil, 0, err
		}
		return s.invalidateSearchOnClose(source.ID, limitWriteHandle(writer, maxBytes, offset)), size, nil
	}
	writer, size, err := s.openWriterLocal(ctx, source.Root, path, offset)
	if err != nil {
		return nil, 0, err
	}
	return s.invalidateSearchOnClose(source.ID, limitWriteHandle(writer, maxBytes, offset)), size, nil
}

func (s *Service) invalidateSearchOnClose(sourceID string, writer WriteHandle) WriteHandle {
	return &searchInvalidatingWriteHandle{
		WriteHandle: writer,
		onClose: func() {
			s.invalidateSearchIndex(sourceID)
		},
	}
}

func (s *Service) OpenRandomWriter(ctx context.Context, path string) (RandomWriteHandle, error) {
	return s.OpenRandomWriterSource(ctx, "", path)
}

func (s *Service) OpenRandomWriterSource(ctx context.Context, sourceID string, path string) (RandomWriteHandle, error) {
	source, err := s.sourceForID(sourceID)
	if err != nil {
		return nil, err
	}
	if err := source.authorize("write", path, 0); err != nil {
		return nil, err
	}
	maxBytes := source.Policy.MaxUploadBytes
	if source.Type == fileSourceTypeSMB {
		writer, err := s.openRandomWriterSMB(ctx, source.ID, path)
		if err != nil {
			return nil, err
		}
		return limitRandomWriteHandle(writer, maxBytes), nil
	}
	writer, err := s.openRandomWriterLocal(ctx, source.Root, path)
	if err != nil {
		return nil, err
	}
	return limitRandomWriteHandle(writer, maxBytes), nil
}

func (s *Service) listLocal(ctx context.Context, root string, path string) ([]protocol.FileItem, error) {
	resolved, err := s.resolveLocal(root, path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}

	items := make([]protocol.FileItem, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		relative := filepath.Join(cleanPath(path), entry.Name())
		items = append(items, fileItem(relative, info))
	}

	sortItems(items)
	return items, nil
}

func (s *Service) statLocal(_ context.Context, root string, path string) (protocol.FileItem, error) {
	resolved, err := s.resolveLocal(root, path)
	if err != nil {
		return protocol.FileItem{}, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return protocol.FileItem{}, err
	}

	return fileItem(cleanPath(path), info), nil
}

func (s *Service) createDirectoryLocal(_ context.Context, root string, path string) error {
	writeRoot, relative, err := s.localRootForWrite(root, path)
	if err != nil {
		return err
	}
	defer writeRoot.Close()
	return writeRoot.MkdirAll(relative, 0o755)
}

func (s *Service) renameLocal(_ context.Context, root string, from string, to string) error {
	writeRoot, relativeTo, err := s.localRootForWrite(root, to)
	if err != nil {
		return err
	}
	defer writeRoot.Close()
	resolvedFrom, err := s.resolveLocal(writeRoot.Name(), from)
	if err != nil {
		return err
	}
	relativeFrom, err := localRootRelativePath(writeRoot.Name(), resolvedFrom)
	if err != nil {
		return err
	}
	if err := writeRoot.MkdirAll(filepath.Dir(relativeTo), 0o755); err != nil {
		return err
	}
	return writeRoot.Rename(relativeFrom, relativeTo)
}

func (s *Service) deleteLocal(_ context.Context, root string, path string, isDirectory bool) error {
	resolved, err := s.resolveLocal(root, path)
	if err != nil {
		return err
	}
	if isDirectory {
		return os.RemoveAll(resolved)
	}
	return os.Remove(resolved)
}

func (s *Service) downloadLocal(_ context.Context, root string, path string) (string, error) {
	resolved, err := s.resolveLocal(root, path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

func (s *Service) uploadLocal(_ context.Context, root string, path string, contentBase64 string) error {
	writeRoot, relative, err := s.localRootForWrite(root, path)
	if err != nil {
		return err
	}
	defer writeRoot.Close()

	data, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return err
	}

	file, err := openLocalWriteFile(writeRoot, relative, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (s *Service) openReaderLocal(_ context.Context, root string, path string, offset int64) (ReadHandle, fs.FileInfo, error) {
	resolved, err := s.resolveLocal(root, path)
	if err != nil {
		return nil, nil, err
	}

	file, err := os.Open(resolved)
	if err != nil {
		return nil, nil, err
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("path is a directory")
	}
	if offset < 0 || offset > info.Size() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("invalid read offset")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, nil, err
	}

	return file, info, nil
}

func (s *Service) openWriterLocal(_ context.Context, root string, path string, offset int64) (WriteHandle, int64, error) {
	writeRoot, relative, err := s.localRootForWrite(root, path)
	if err != nil {
		return nil, 0, err
	}
	defer writeRoot.Close()

	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := openLocalWriteFile(writeRoot, relative, flags)
	if err != nil {
		return nil, 0, err
	}
	if offset == 0 {
		return file, 0, nil
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}

	size := info.Size()
	if offset < 0 || offset > size {
		_ = file.Close()
		return nil, 0, fmt.Errorf("invalid write offset")
	}
	if size > offset {
		if err := file.Truncate(offset); err != nil {
			_ = file.Close()
			return nil, 0, err
		}
		size = offset
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, 0, err
	}

	return file, size, nil
}

func (s *Service) openRandomWriterLocal(_ context.Context, root string, path string) (RandomWriteHandle, error) {
	writeRoot, relative, err := s.localRootForWrite(root, path)
	if err != nil {
		return nil, err
	}
	defer writeRoot.Close()

	return openLocalWriteFile(writeRoot, relative, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
}

// localRootForWrite preserves resolved internal aliases, then anchors mutations
// to an open root so later symlink changes cannot redirect them outside it.
func (s *Service) localRootForWrite(rootDir string, path string) (*os.Root, string, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return nil, "", ErrDisabled
	}
	absoluteRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, "", err
	}
	canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, "", err
	}
	writeRoot, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, "", err
	}
	resolved, err := s.resolveLocalForWrite(canonicalRoot, path)
	if err != nil {
		_ = writeRoot.Close()
		return nil, "", err
	}
	relative, err := localRootRelativePath(canonicalRoot, resolved)
	if err != nil {
		_ = writeRoot.Close()
		return nil, "", err
	}
	return writeRoot, relative, nil
}

func localRootRelativePath(root string, resolved string) (string, error) {
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(relative) {
		return "", fmt.Errorf("path escapes root")
	}
	return relative, nil
}

func openLocalWriteFile(root *os.Root, relative string, flags int) (*os.File, error) {
	relative, err := resolveLocalWriteFileAlias(root, relative)
	if err != nil {
		return nil, err
	}
	if err := root.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
		return nil, err
	}
	return root.OpenFile(relative, flags, 0o644)
}

// Missing targets leave terminal aliases unresolved by EvalSymlinks. Resolve
// those aliases explicitly so absolute internal links keep working; the final
// open still uses Root and enforces containment if a target changes meanwhile.
func resolveLocalWriteFileAlias(root *os.Root, relative string) (string, error) {
	for followed := 0; ; followed++ {
		info, err := root.Lstat(relative)
		if errors.Is(err, os.ErrNotExist) {
			return relative, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return relative, nil
		}
		if followed >= 40 {
			return "", fmt.Errorf("too many symbolic links")
		}
		target, err := root.Readlink(relative)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			// Preserve target components such as symlink/../file for EvalSymlinks.
			target = root.Name() + string(filepath.Separator) + filepath.Dir(relative) + string(filepath.Separator) + target
		}
		if real, err := filepath.EvalSymlinks(target); err == nil {
			return localRootRelativePath(root.Name(), real)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent, name := filepath.Split(target)
		realParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", err
		}
		relative, err = localRootRelativePath(root.Name(), filepath.Join(realParent, name))
		if err != nil {
			return "", err
		}
	}
}

type searchInvalidatingWriteHandle struct {
	WriteHandle
	onClose func()
}

func (w *searchInvalidatingWriteHandle) Close() error {
	err := w.WriteHandle.Close()
	if err == nil && w.onClose != nil {
		w.onClose()
	}
	return err
}

type maxWriteHandle struct {
	inner   WriteHandle
	max     int64
	current int64
}

func limitWriteHandle(inner WriteHandle, max int64, current int64) WriteHandle {
	if inner == nil || max <= 0 {
		return inner
	}
	return &maxWriteHandle{inner: inner, max: max, current: current}
}

func (w *maxWriteHandle) Write(data []byte) (int, error) {
	if w.max > 0 && w.current+int64(len(data)) > w.max {
		return 0, errors.New("file source policy upload size limit exceeded")
	}
	n, err := w.inner.Write(data)
	w.current += int64(n)
	return n, err
}

func (w *maxWriteHandle) Close() error {
	return w.inner.Close()
}

type maxRandomWriteHandle struct {
	inner RandomWriteHandle
	max   int64
}

func limitRandomWriteHandle(inner RandomWriteHandle, max int64) RandomWriteHandle {
	if inner == nil || max <= 0 {
		return inner
	}
	return &maxRandomWriteHandle{inner: inner, max: max}
}

func (w *maxRandomWriteHandle) WriteAt(data []byte, offset int64) (int, error) {
	if w.max > 0 && offset+int64(len(data)) > w.max {
		return 0, errors.New("file source policy upload size limit exceeded")
	}
	return w.inner.WriteAt(data, offset)
}

func (w *maxRandomWriteHandle) Truncate(size int64) error {
	if w.max > 0 && size > w.max {
		return errors.New("file source policy upload size limit exceeded")
	}
	return w.inner.Truncate(size)
}

func (w *maxRandomWriteHandle) Close() error {
	return w.inner.Close()
}

func (s *Service) resolveLocal(rootDir string, path string) (string, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return "", ErrDisabled
	}

	cleaned := cleanPath(path)
	joined := filepath.Join(rootDir, filepath.FromSlash(cleaned))
	resolved := filepath.Clean(joined)
	root, err := filepath.EvalSymlinks(filepath.Clean(rootDir))
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else {
		return "", err
	}

	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}

	return resolved, nil
}

func (s *Service) resolveLocalForWrite(rootDir string, path string) (string, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return "", ErrDisabled
	}
	resolved, err := s.resolveLocal(rootDir, path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	cleaned := cleanPath(path)
	joined := filepath.Join(rootDir, filepath.FromSlash(cleaned))
	root, err := filepath.EvalSymlinks(filepath.Clean(rootDir))
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(filepath.Clean(joined))
	existingParent := parent
	missing := []string{filepath.Base(joined)}
	for {
		if realParent, err := filepath.EvalSymlinks(existingParent); err == nil {
			if realParent != root && !strings.HasPrefix(realParent, root+string(filepath.Separator)) {
				return "", fmt.Errorf("path escapes root")
			}
			for i := len(missing) - 1; i >= 0; i-- {
				realParent = filepath.Join(realParent, missing[i])
			}
			return realParent, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if filepath.Clean(existingParent) == root || filepath.Dir(existingParent) == existingParent {
			return "", err
		}
		missing = append(missing, filepath.Base(existingParent))
		existingParent = filepath.Dir(existingParent)
	}
}

func resolveSharePath(path string) string {
	cleaned := cleanPath(path)
	if cleaned == "" {
		return "."
	}
	return filepath.ToSlash(cleaned)
}

func cleanPath(path string) string {
	cleaned := filepath.ToSlash(filepath.Clean("/" + strings.TrimSpace(path)))
	return strings.TrimPrefix(cleaned, "/")
}

func sortItems(items []protocol.FileItem) {
	sort.Slice(items, func(i int, j int) bool {
		if items[i].IsDirectory != items[j].IsDirectory {
			return items[i].IsDirectory
		}
		return items[i].Name < items[j].Name
	})
}

func decorateFileItemsSource(items []protocol.FileItem, sourceID string) []protocol.FileItem {
	for i := range items {
		items[i].SourceID = sourceID
	}
	return items
}

func fileSearchScore(item protocol.FileItem, query string) int {
	query = strings.ToLower(strings.TrimSpace(query))
	name := strings.ToLower(item.Name)
	path := strings.ToLower(item.Path)
	score := 0
	if strings.Contains(name, query) {
		score += 8
	}
	if strings.Contains(path, query) {
		score += 5
	}
	for _, token := range strings.Fields(query) {
		if strings.Contains(name, token) {
			score += 3
		}
		if strings.Contains(path, token) {
			score++
		}
	}
	if score > 0 && item.IsDirectory {
		score++
	}
	return score
}

func fileItem(path string, info fs.FileInfo) protocol.FileItem {
	return protocol.FileItem{
		Path:        filepath.ToSlash(path),
		Name:        info.Name(),
		IsDirectory: info.IsDir(),
		Size:        info.Size(),
		ModifiedAt:  info.ModTime().UTC(),
	}
}
