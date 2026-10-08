package cloud

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/linuxrelease"
	"github.com/dropfile/HankServerside/internal/maintenance"
	"github.com/dropfile/HankServerside/internal/observability"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/storageops"
	"github.com/dropfile/HankServerside/internal/store"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxHTTPBodyBytes = 1 << 20
const maxWSMessageBytes = 2 << 20

type Server struct {
	addr                       string
	store                      *store.Store
	router                     *Router
	logger                     *slog.Logger
	http                       *http.Server
	metrics                    *observability.Metrics
	limiter                    *rateLimiter
	transferControlMu          sync.Mutex
	transfers                  *transferRegistry
	appTickets                 *appWebSocketTicketRegistry
	appPackages                *appPackageStagingRegistry
	notes                      *cloudNotesService
	kanban                     *mcpKanbanService
	mcpAttachments             *mcpNoteAttachmentService
	collaboration              *noteCollaborationHub
	agentRequests              *agentRequestRegistry
	syncs                      *homeSyncController
	health                     *agentHealthMonitor
	shellSessions              *shellSessionRegistry
	storage                    *storageops.Service
	storageEvents              map[string]struct{}
	storageEventsMu            sync.Mutex
	notificationEvents         map[string]time.Time
	notificationEventsMu       sync.Mutex
	pushSender                 PushSender
	notificationService        *notificationService
	realtimeCancel             context.CancelFunc
	sessionTTL                 time.Duration
	requestTimeout             time.Duration
	assistantAI                AssistantAIConfig
	chatGPTDeviceAuths         *chatGPTDeviceAuthRegistry
	desktop                    *desktopService
	desktopRelay               desktopRelay
	desktopRelayAuth           desktopRelayAuthorizer
	noteAttachmentRoot         string
	assistantTrace             *assistantTraceLog
	loginBackoff               *loginBackoffRegistry
	adminActionTokens          *adminActionTokenRegistry
	quickLinkHTTPClient        *http.Client
	quickLinkCheckTimeout      time.Duration
	secretEncryptionConfigured bool
	plaintextSecretsAllowed    bool
	agentLifecycleMu           sync.Mutex
	fileJobRecoveryMu          sync.Mutex
	fileSearchWorkers          sync.Map
	fleetReconcileWorkers      sync.Map
	runtimeID                  string
	runtimeVersion             string
	metricsScrapeToken         string
	alertmanagerWebhookToken   string
	monitoring                 *monitoringDelivery
	linuxAgentRelease          *linuxrelease.Release
	linuxUpdates               *linuxUpdateCoordinator
	runtimeCancel              context.CancelFunc
	maintenanceCancel          context.CancelFunc
	webPushCancel              context.CancelFunc
	webPushConfig              WebPushConfig
	httpBaseCancel             context.CancelFunc
	mcpEnabled                 bool
	mcpPublicBaseURL           string
	mcpDocsDir                 string
	mcpDocs                    *mcpDocsIndex
	mcpSubscriptions           *mcpSubscriptionHub
	mcpLegacyHandlerOnce       sync.Once
	mcpLegacyHandler           http.Handler
	mcpLegacySessionsMu        sync.Mutex
	mcpLegacySessions          map[string]mcpLegacySessionBinding
	entra                      *entraService
}

type authContext struct {
	User    domain.User
	Session domain.AppSession
}

func NewServer(addr string, db *store.Store, sessionTTL time.Duration, requestTimeout time.Duration, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if sessionTTL <= 0 {
		sessionTTL = 7 * 24 * time.Hour
	}
	if requestTimeout <= 0 {
		requestTimeout = 120 * time.Second
	}

	realtimeCtx, realtimeCancel := context.WithCancel(context.Background())
	httpBaseCtx, httpBaseCancel := context.WithCancel(context.Background())
	server := &Server{
		addr:                  addr,
		store:                 db,
		router:                NewRouter(),
		logger:                logger,
		metrics:               observability.NewMetrics(),
		limiter:               newRateLimiter(),
		health:                newAgentHealthMonitor(),
		shellSessions:         newShellSessionRegistry(4, 8, 5*time.Minute),
		transfers:             newTransferRegistry(),
		appTickets:            newAppWebSocketTicketRegistry(),
		appPackages:           newAppPackageStagingRegistry(),
		notes:                 newCloudNotesService(db),
		collaboration:         newNoteCollaborationHub(db),
		agentRequests:         newAgentRequestRegistry(),
		syncs:                 newHomeSyncController(),
		storage:               storageops.NewService("", "", ""),
		storageEvents:         make(map[string]struct{}),
		notificationEvents:    make(map[string]time.Time),
		pushSender:            noopPushSender{},
		realtimeCancel:        realtimeCancel,
		sessionTTL:            sessionTTL,
		requestTimeout:        requestTimeout,
		chatGPTDeviceAuths:    newChatGPTDeviceAuthRegistry(),
		noteAttachmentRoot:    filepath.Join("/tmp", "hank-note-attachments"),
		assistantTrace:        newAssistantTraceLog(maxAssistantTraceLimit),
		loginBackoff:          newLoginBackoffRegistry(),
		adminActionTokens:     newAdminActionTokenRegistry(),
		quickLinkHTTPClient:   &http.Client{Timeout: 5 * time.Second},
		quickLinkCheckTimeout: 5 * time.Second,
		runtimeID:             newID("run"),
		runtimeVersion:        "dev",
		httpBaseCancel:        httpBaseCancel,
	}
	server.mcpSubscriptions = newMCPSubscriptionHub(defaultMCPSubscriptionConfig(), server.metrics)
	server.kanban = newMCPKanbanService(server.store, server.notes, func() time.Time { return time.Now().UTC() })
	server.notificationService = newNotificationService(server.store, server.logger, func(ctx context.Context, userIDs []string, notification PushNotification) {
		server.sendNotificationToUsers(ctx, userIDs, notification)
	})
	server.mcpAttachments = newMCPNoteAttachmentService(server.store, server.noteAttachmentRoot, func() time.Time { return time.Now().UTC() })
	server.desktopRelay = newInProcessDesktopRelay(defaultDesktopRelayLimits(), func(_ context.Context, event desktopRelayLifecycleEvent) {
		go server.recordDesktopRelayLifecycle(event)
	})
	server.desktop = newDesktopService(server.store, server.router, func() time.Time { return time.Now().UTC() }, newToken, server.desktopRelay)
	server.desktopRelayAuth = storeDesktopRelayAuthorizer{store: server.store}
	server.linuxUpdates = newLinuxUpdateCoordinator(server)

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.handleLoginPage)
	mux.HandleFunc("/join", server.handleJoinPage)
	mux.HandleFunc("/password-change", server.handlePasswordChangePage)
	mux.HandleFunc("/dashboard", server.handleDashboardPage)
	mux.HandleFunc("/dashboard/hank", server.handleHankPage)
	mux.HandleFunc("/dashboard/home-assistant", server.handleHomeAssistantPage)
	mux.HandleFunc("/dashboard/profile-notes", server.handleProfileNotesPage)
	mux.HandleFunc("/dashboard/file-server", server.handleFileServerPage)
	mux.HandleFunc("/dashboard/agents", server.handleAgentsPage)
	mux.HandleFunc("/dashboard/notifications/event", server.handleNotificationEventPage)
	mux.HandleFunc("/dashboard/settings", server.handleSettingsPage)
	mux.HandleFunc("/dashboard/settings/home", server.handleSettingsHomePage)
	mux.HandleFunc("/dashboard/settings/quick-links", server.handleSettingsQuickLinksPage)
	mux.HandleFunc("/dashboard/settings/notifications", server.handleSettingsNotificationsPage)
	mux.HandleFunc("/dashboard/settings/people", server.handleSettingsPeoplePage)
	mux.HandleFunc("/dashboard/settings/connections", server.handleSettingsConnectionsPage)
	mux.HandleFunc("/dashboard/settings/ai", server.handleSettingsAIPage)
	mux.HandleFunc("/dashboard/settings/apps", server.handleSettingsAppsPage)
	mux.HandleFunc("/dashboard/settings/backups", server.handleSettingsBackupsPage)
	mux.HandleFunc("/dashboard/settings/recovery", server.handleSettingsRecoveryPage)
	mux.HandleFunc("/dashboard/settings/logs", server.handleSettingsLogsPage)
	mux.HandleFunc("/dashboard/settings/join-home", server.handleSettingsJoinHomePage)
	mux.HandleFunc("/dashboard/settings/people-pane", redirectToSettingsRoute("/dashboard/settings/people"))
	mux.HandleFunc("/dashboard/settings/connections-pane", redirectToSettingsRoute("/dashboard/settings/connections"))
	mux.HandleFunc("/dashboard/settings/ai-pane", redirectToSettingsRoute("/dashboard/settings/ai"))
	mux.HandleFunc("/dashboard/settings/backups-pane", redirectToSettingsRoute("/dashboard/settings/backups"))
	mux.HandleFunc("/dashboard/settings/recovery-pane", redirectToSettingsRoute("/dashboard/settings/recovery"))
	mux.HandleFunc("/dashboard/settings/apps-pane", redirectToSettingsRoute("/dashboard/settings/apps"))
	mux.HandleFunc("/dashboard/settings/logs-pane", redirectToSettingsRoute("/dashboard/settings/logs"))
	mux.HandleFunc("/dashboard/settings/join-home-pane", redirectToSettingsRoute("/dashboard/settings/join-home"))
	mux.HandleFunc("/docs/deployment", serveDeploymentGuide)
	mux.HandleFunc("/manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		servePWAControlFile(w, r, "/manifest.webmanifest", "manifest.webmanifest", "application/manifest+json; charset=utf-8")
	})
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		servePWAControlFile(w, r, "/sw.js", "sw.js", "application/javascript; charset=utf-8")
	})
	mux.HandleFunc("/offline.html", func(w http.ResponseWriter, r *http.Request) {
		servePWAControlFile(w, r, "/offline.html", "offline.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("/favicon.ico", serveUIFavicon)
	mux.HandleFunc("/assets/", serveUIAsset)
	mux.HandleFunc("/healthz", server.handleHealthz)
	mux.HandleFunc("/readyz", server.handleReadyz)
	mux.HandleFunc("/metrics", server.handleMetrics)
	mux.HandleFunc("/v1/integrations/alertmanager", server.handleAlertmanagerWebhook)
	mux.HandleFunc("/v1/auth/register", server.handleAuthRegister)
	mux.HandleFunc("/v1/auth/login", server.handleAuthLogin)
	mux.HandleFunc("/v1/auth/logout", server.handleAuthLogout)
	mux.HandleFunc("/v1/auth/entra/start", server.handleEntraStart)
	mux.HandleFunc("/v1/auth/entra/link/start", server.handleEntraLinkStart)
	mux.HandleFunc("/v1/auth/entra/callback", server.handleEntraCallback)
	mux.HandleFunc("/v1/auth/change-password", server.handleAuthChangePassword)
	mux.HandleFunc("/v1/auth/invitations/preview", server.handleAuthInvitationPreview)
	mux.HandleFunc("/v1/auth/invitations/signup", server.handleAuthInvitationSignup)
	mux.HandleFunc("/v1/ui/bootstrap", server.handleUIBootstrap)
	mux.HandleFunc("/v1/me", server.handleMe)
	mux.HandleFunc("/v1/me/devices/apns", server.handleAPNSDeviceRegistration)
	mux.HandleFunc("/v1/me/devices/", server.handleAPNSDevice)
	mux.HandleFunc("/v1/me/notification-settings", server.handleNotificationSettings)
	mux.HandleFunc("/v1/me/notifications", server.handleUserNotifications)
	mux.HandleFunc("/v1/me/notifications/", server.handleUserNotifications)
	mux.HandleFunc("/v1/me/web-push/config", server.handleWebPushConfig)
	mux.HandleFunc("/v1/me/web-push/subscriptions", server.handleWebPushSubscriptions)
	mux.HandleFunc("/v1/me/web-push/subscriptions/", server.handleWebPushSubscriptions)
	mux.HandleFunc("/v1/oauth/openai/status", server.handleOpenAIOAuthStatus)
	mux.HandleFunc("/v1/oauth/openai/start", server.handleOpenAIOAuthStart)
	mux.HandleFunc("/.well-known/oauth-protected-resource", server.handleMCPProtectedResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/", server.handleMCPProtectedResourceMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server", server.handleMCPAuthServerMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server/", server.handleMCPAuthServerMetadata)
	mux.HandleFunc("/v1/oauth/mcp/register", server.handleMCPRegister)
	mux.HandleFunc(mcpConsentScriptPath, server.handleMCPConsentScript)
	mux.HandleFunc("/v1/oauth/mcp/authorize", server.handleMCPAuthorize)
	mux.HandleFunc("/v1/oauth/mcp/token", server.handleMCPToken)
	mux.HandleFunc("/v1/mcp", server.handleMCPEndpoint)
	mux.HandleFunc("/v1/me/mcp", server.handleProfileMCP)
	mux.HandleFunc("/v1/me/mcp/", server.handleProfileMCP)
	mux.HandleFunc("/v1/me/notes", server.handleProfileNotesHTTP)
	mux.HandleFunc("/v1/me/notes/", server.handleProfileNotesHTTP)
	mux.HandleFunc("/v1/me/profile", server.handleProfileSettingsHTTP)
	mux.HandleFunc("/v1/me/profile-secret-vault", server.handleProfileSecretVaultHTTP)
	mux.HandleFunc("/v1/me/profile-backup", server.handleProfileBackupHTTP)
	mux.HandleFunc("/v1/ws/app-ticket", server.handleAppWebSocketTicket)
	mux.HandleFunc("/v1/fleet/", server.handleFleet)
	mux.HandleFunc("/v1/home", server.handleHome)
	mux.HandleFunc("/v1/home/invitations/accept", server.handleHomeInvitationAccept)
	mux.HandleFunc("/v1/home/", server.handleHomeSubroutes)
	mux.HandleFunc("/v1/agent/desktop-enrollment", server.handleAgentDesktopEnrollment)
	mux.HandleFunc("/v1/agent/enrollments/linux/consume", server.handleLinuxAgentEnrollmentConsume)
	mux.HandleFunc("/v1/agent/enrollments/consume", server.handleLinuxAgentEnrollmentConsume)
	mux.HandleFunc("/v1/agent/credentials/", server.handleAgentCredentials)
	mux.HandleFunc("/install/linux/", server.handleLinuxInstaller)
	mux.HandleFunc("/install/linux-release/", server.handleLinuxAgentRelease)
	mux.HandleFunc("/v1/agents/", server.handleAgentResourceRoutes)
	mux.HandleFunc("/v1/desktop-sessions/", server.handleDesktopSessionRoutes)
	mux.HandleFunc("/v1/file-transfers/", server.handleFileTransfer)
	mux.HandleFunc("/ws/agent", server.handleAgentWebSocket)
	mux.HandleFunc("/ws/app", server.handleAppWebSocket)
	mux.HandleFunc("/ws/desktop/browser/", server.handleDesktopBrowserWebSocket)
	mux.HandleFunc("/ws/desktop/agent/", server.handleDesktopAgentWebSocket)

	server.http = &http.Server{
		Addr:              addr,
		Handler:           securityHeadersMiddleware(requestIDMiddleware(server.metricsMiddleware(routeDeadlineMiddleware(mux)))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		BaseContext: func(net.Listener) context.Context {
			return httpBaseCtx
		},
	}

	go server.forwardStoreNotifications(realtimeCtx)
	go server.forwardStorageEvents(realtimeCtx)
	go server.runAgentHealthMonitor(realtimeCtx)

	return server
}

func (s *Server) ConfigureAssistantAI(cfg AssistantAIConfig) {
	cfg.normalize()
	s.assistantAI = cfg
}

func (s *Server) ConfigureStorageOps(stateDir, logDir, intentSecret string) {
	s.storage = storageops.NewService(stateDir, logDir, intentSecret)
}

func (s *Server) ConfigureNoteAttachmentStorage(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	s.noteAttachmentRoot = root
	if s.mcpAttachments != nil {
		s.mcpAttachments.attachmentRoot = root
	}
}

func (s *Server) ConfigureAPNS(cfg APNSConfig) {
	s.pushSender = NewAPNSSender(cfg, s.logger)
}

func (s *Server) ConfigureWebPush(cfg WebPushConfig) error {
	if cfg.Enabled && (strings.TrimSpace(cfg.PublicKey) == "" || strings.TrimSpace(cfg.PrivateKey) == "" || strings.TrimSpace(cfg.Subject) == "") {
		return errors.New("complete VAPID configuration is required")
	}
	if s.webPushCancel != nil {
		s.webPushCancel()
		s.webPushCancel = nil
	}
	if s.notificationService != nil {
		s.notificationService.webPushEnabled = false
	}
	s.webPushConfig = cfg
	if !cfg.Enabled {
		return nil
	}
	if s.notificationService != nil {
		s.notificationService.webPushEnabled = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.webPushCancel = cancel
	dispatcher := newWebPushDispatcher(s.store, cfg, s.logger, nil)
	go dispatcher.Run(ctx, s.runtimeID+"-web-push")
	return nil
}

func (s *Server) ConfigureSecretStorageStatus(encrypted bool, plaintextAllowed bool) {
	s.secretEncryptionConfigured = encrypted
	s.plaintextSecretsAllowed = plaintextAllowed
}

// ConfigureMetricsScrapeToken enables a dedicated bearer token for Prometheus
// scrapes of /metrics, so monitoring does not depend on an expiring admin
// session. Admin-session access continues to work either way.
func (s *Server) ConfigureMetricsScrapeToken(token string) {
	s.metricsScrapeToken = strings.TrimSpace(token)
}

func (s *Server) metricsScrapeTokenMatches(r *http.Request) bool {
	if s.metricsScrapeToken == "" {
		return false
	}
	presented, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.metricsScrapeToken)) == 1
}

func (s *Server) StartRuntime(ctx context.Context, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	s.runtimeVersion = version
	if err := s.store.UpsertCloudRuntime(ctx, s.runtimeID, s.runtimeVersion); err != nil {
		return err
	}
	if expired, err := s.store.ExpireRelayRequests(ctx, time.Now().UTC()); err != nil {
		s.logger.Warn("failed to expire relay requests on startup", "runtime_id", s.runtimeID, "error", err)
	} else if expired > 0 {
		s.logger.Info("expired stale relay requests on startup", "runtime_id", s.runtimeID, "count", expired)
	}
	if expired, err := s.store.ExpireDesktopSessions(ctx, time.Now().UTC()); err != nil {
		s.logger.Warn("failed to expire desktop sessions on startup", "runtime_id", s.runtimeID, "error", err)
	} else if expired > 0 {
		s.logger.Info("expired stale desktop sessions on startup", "runtime_id", s.runtimeID, "count", expired)
	}
	if err := s.store.MarkInterruptedFileOperationJobs(ctx, time.Now().UTC()); err != nil {
		s.logger.Warn("failed to mark interrupted file operation jobs", "runtime_id", s.runtimeID, "error", err)
	}
	if err := s.repairNoteAttachmentBackupPermissions(); err != nil {
		s.logger.Warn("failed to repair note attachment backup permissions", "runtime_id", s.runtimeID, "error", err)
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	s.runtimeCancel = cancel
	go s.runMonitoringDelivery(runtimeCtx)
	s.startAssistantIndexWorker(runtimeCtx)
	go s.runAssistantExecutionWorker(runtimeCtx, s.runtimeID+"-assistant", s.newAssistantExecutionModel)
	s.linuxUpdates.Start(runtimeCtx)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runtimeCtx.Done():
				return
			case <-ticker.C:
				if err := s.store.HeartbeatCloudRuntime(context.Background(), s.runtimeID); err != nil {
					s.logger.Warn("failed to heartbeat cloud runtime", "runtime_id", s.runtimeID, "error", err)
				}
				if expired, err := s.store.ExpireDesktopSessions(context.Background(), time.Now().UTC()); err != nil {
					s.logger.Warn("failed to expire desktop sessions", "runtime_id", s.runtimeID, "error", err)
				} else if expired > 0 {
					s.logger.Info("expired desktop sessions", "runtime_id", s.runtimeID, "count", expired)
				}
			}
		}
	}()
	return nil
}

func (s *Server) StartMaintenance(interval time.Duration, retention time.Duration) {
	if s.maintenanceCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.maintenanceCancel = cancel
	jobs := maintenance.Jobs{
		Store:     s.store,
		Retention: retention,
		Logger:    s.logger,
		AfterStorePrune: func(summary store.LifecyclePruneSummary) {
			s.metrics.AddMCPAttachmentCleanup("expired_sessions", summary.MCPAttachmentUploadsExpired)
			s.metrics.AddMCPAttachmentCleanup("upload_rows", summary.MCPAttachmentUploadsDeleted)
		},
		AfterPrune: func(ctx context.Context, now time.Time, retention time.Duration) error {
			return s.pruneNoteAttachmentFiles(ctx, now, retention)
		},
	}
	go func() {
		if err := jobs.Run(ctx, interval); err != nil && ctx.Err() == nil {
			s.logger.Warn("maintenance loop stopped", "error", err)
		}
	}()
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("starting Hank server", "addr", s.addr)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.mcpSubscriptions != nil {
		s.mcpSubscriptions.closeAll("shutdown")
		if err := s.mcpSubscriptions.wait(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("mcp subscription drain failed", "error", err)
		}
	}
	if s.runtimeCancel != nil {
		s.runtimeCancel()
	}
	if s.maintenanceCancel != nil {
		s.maintenanceCancel()
	}
	if s.webPushCancel != nil {
		s.webPushCancel()
	}
	if s.store != nil {
		_ = s.store.ShutdownCloudRuntime(context.Background(), s.runtimeID)
	}
	if s.realtimeCancel != nil {
		s.realtimeCancel()
	}
	if s.httpBaseCancel != nil {
		s.httpBaseCancel()
	}
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "hank-server",
		"time":    time.Now().UTC(),
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	payload := map[string]any{
		"ok":               true,
		"storage":          "ready",
		"deployment_mode":  "single_home",
		"runtime_id":       s.runtimeID,
		"online_agents":    s.router.AgentCount(),
		"online_apps":      s.router.AppCount(),
		"pending_requests": s.router.PendingCount(),
		"secret_storage": map[string]any{
			"encrypted":         s.secretEncryptionConfigured,
			"plaintext_allowed": s.plaintextSecretsAllowed,
		},
	}
	if status, err := s.store.GetCloudRuntime(r.Context()); err == nil {
		payload["runtime"] = map[string]any{
			"deployment_id": status.DeploymentID,
			"runtime_id":    status.RuntimeID,
			"version":       status.Version,
			"started_at":    status.StartedAt,
			"heartbeat_at":  status.HeartbeatAt,
			"shutdown_at":   status.ShutdownAt,
		}
	}
	if count, err := s.store.CountHomes(r.Context()); err == nil {
		payload["deployment_home_count"] = count
		if count > 1 {
			payload["ok"] = false
			payload["error"] = "multiple deployment homes found; single-home mode requires repair"
			writeJSON(w, http.StatusServiceUnavailable, payload)
			return
		}
	}
	if err := s.store.CheckMigrations(r.Context()); err != nil {
		payload["ok"] = false
		payload["migration_error"] = err.Error()
		writeJSON(w, http.StatusServiceUnavailable, payload)
		return
	}
	payload["migrations"] = "ready"
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsScrapeTokenMatches(r) {
		auth, ok := s.requireAuth(w, r)
		if !ok {
			return
		}
		_, membership, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if membership.Role != domain.HomeRoleAdmin {
			http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
			return
		}
	}
	s.refreshDesktopSessionMetrics(r.Context())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = io.WriteString(w, s.metrics.RenderPrometheus())
	if db := s.store.DB(); db != nil {
		stats := db.Stats()
		_, _ = fmt.Fprintf(w, "hank_db_max_connections %d\nhank_db_wait_count_total %d\nhank_db_wait_duration_seconds_total %g\n", stats.MaxOpenConnections, stats.WaitCount, stats.WaitDuration.Seconds())
		_, _ = io.WriteString(w, "hank_db_open_connections "+strconv.Itoa(stats.OpenConnections)+"\n")
		_, _ = io.WriteString(w, "hank_db_in_use_connections "+strconv.Itoa(stats.InUse)+"\n")
		_, _ = io.WriteString(w, "hank_db_idle_connections "+strconv.Itoa(stats.Idle)+"\n")
	}
	if err := s.store.Ping(r.Context()); err != nil {
		_, _ = io.WriteString(w, "hank_db_ping_success 0\n")
	} else {
		_, _ = io.WriteString(w, "hank_db_ping_success 1\n")
	}
	if status, err := s.store.GetCloudRuntime(r.Context()); err == nil {
		age := time.Since(status.HeartbeatAt).Seconds()
		if age < 0 {
			age = 0
		}
		_, _ = io.WriteString(w, "hank_cloud_runtime_heartbeat_age_seconds "+strconv.FormatFloat(age, 'f', 3, 64)+"\n")
		_, _ = io.WriteString(w, "hank_cloud_runtime_up 1\n")
	} else {
		_, _ = io.WriteString(w, "hank_cloud_runtime_up 0\n")
	}
	if home, err := s.store.GetSingletonHome(r.Context()); err == nil {
		primaryOnline := "0"
		if _, online := s.router.GetAgent(home.ID); online {
			primaryOnline = "1"
		}
		_, _ = io.WriteString(w, "hank_primary_agent_online "+primaryOnline+"\n")
		if counts, err := s.store.CountFileOperationJobsByStatus(r.Context(), home.ID); err == nil {
			for _, status := range []string{"queued", "running", "completed", "failed", "cancelled", "rollback_required", "rolled_back"} {
				_, _ = io.WriteString(w, "hank_file_operation_jobs{status="+strconv.Quote(status)+"} "+strconv.FormatInt(counts[status], 10)+"\n")
			}
		}
		if total, err := s.store.TotalReadyNoteAttachmentBytes(r.Context(), home.ID); err == nil {
			_, _ = io.WriteString(w, "hank_attachment_storage_bytes "+strconv.FormatInt(total, 10)+"\n")
		}
	}
	if s.storage != nil {
		if status, err := s.storage.Status(); err == nil {
			_, _ = io.WriteString(w, storageops.RenderMetrics(status))
		}
	}
}

func (s *Server) refreshDesktopSessionMetrics(ctx context.Context) {
	if s.metrics == nil || s.store == nil {
		return
	}
	counts, err := s.store.DesktopSessionStatePlatformCounts(ctx)
	if err != nil {
		return
	}
	for _, platform := range []string{"windows", "macos", "unknown"} {
		for _, state := range []string{"requested", "offered", "agent_ready", "joining", "active", "reconnecting", "denied", "expired", "terminated", "failed"} {
			s.metrics.SetDesktopSessions(state, platform, counts[platform+"\x00"+state])
		}
	}
}

func (s *Server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type request struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	var body request
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	if body.Email == "" || len(body.Password) < 8 {
		http.Error(w, "email and password are required; password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	if count, err := s.store.CountHomes(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if count > 0 {
		http.Error(w, "registration is disabled after first setup", http.StatusForbidden)
		return
	}

	if !s.limiter.Allow("register:"+clientIP(r), 10, time.Minute) {
		s.metrics.IncAuthFailure("register_rate_limited")
		s.logger.Warn("registration rate limited", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r))
		http.Error(w, "too many registration attempts", http.StatusTooManyRequests)
		return
	}

	if _, err := s.store.GetUserByEmail(r.Context(), body.Email); err == nil {
		http.Error(w, "user already exists", http.StatusConflict)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	user := domain.User{
		ID:           newID("usr"),
		Email:        body.Email,
		PasswordHash: string(passwordHash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.CreateUser(r.Context(), user); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	home, _, err := s.store.BootstrapSingletonHome(r.Context(), user, "Home")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	session, rawToken, err := s.createSession(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("app user registered", "request_id", requestIDFromContext(r.Context()), "user_id", user.ID, "email", user.Email, "session_id", session.ID)
	s.audit(r.Context(), "session.created", auditSeverityInfo, user.ID, "", home.ID, requestIDFromContext(r.Context()), "session", session.ID, map[string]any{"reason": "registration"})
	setSessionCookie(w, r, rawToken, session.ExpiresAt)

	writeJSON(w, http.StatusCreated, map[string]any{
		"user":          sanitizeUser(user),
		"session_id":    session.ID,
		"session_token": rawToken,
		"expires_at":    session.ExpiresAt,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type request struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	var body request
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	retryAfter, blocked, backoffErr := s.store.LoginBackoffBlocked(r.Context(), body.Email)
	if backoffErr != nil {
		retryAfter, blocked = s.loginBackoff.Blocked(body.Email)
	}
	if blocked {
		s.metrics.IncAuthFailure("login_email_backoff")
		s.audit(r.Context(), "login.failed", auditSeverityWarning, "", "", s.auditHomeIDForEmail(r.Context(), body.Email), requestIDFromContext(r.Context()), "email", stableAuditTarget(body.Email), map[string]any{"reason": "login_backoff"})
		s.logger.Warn("login email backoff active", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r), "email", body.Email, "retry_after", retryAfter.String())
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	allowed, limitErr := s.store.AllowRateLimit(r.Context(), "login", clientIP(r), 20, time.Minute)
	if limitErr != nil {
		allowed = s.limiter.Allow("login:"+clientIP(r), 20, time.Minute)
	}
	if !allowed {
		s.metrics.IncAuthFailure("login_rate_limited")
		s.audit(r.Context(), "login.failed", auditSeverityWarning, "", "", s.auditHomeIDForEmail(r.Context(), body.Email), requestIDFromContext(r.Context()), "email", stableAuditTarget(body.Email), map[string]any{"reason": "rate_limited"})
		s.logger.Warn("login rate limited", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r), "email", body.Email)
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}

	user, err := s.store.GetUserByEmail(r.Context(), body.Email)
	if err != nil {
		_ = s.store.RecordLoginFailure(r.Context(), body.Email)
		s.loginBackoff.RecordFailure(body.Email)
		s.audit(r.Context(), "login.failed", auditSeverityWarning, "", "", "", requestIDFromContext(r.Context()), "email", stableAuditTarget(body.Email), map[string]any{"reason": "unknown_user"})
		s.metrics.IncAuthFailure("login_unknown_user")
		s.logger.Warn("login failed for unknown user", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r), "email", body.Email)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if !user.PasswordLoginEnabled || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.Password)) != nil {
		_ = s.store.RecordLoginFailure(r.Context(), body.Email)
		s.loginBackoff.RecordFailure(body.Email)
		s.audit(r.Context(), "login.failed", auditSeverityWarning, user.ID, "", s.auditHomeIDForUser(r.Context(), user.ID), requestIDFromContext(r.Context()), "user", user.ID, map[string]any{"reason": "bad_password"})
		s.metrics.IncAuthFailure("login_bad_password")
		s.logger.Warn("login failed with bad password", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r), "user_id", user.ID, "email", body.Email)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	_ = s.store.RecordLoginSuccess(r.Context(), body.Email)
	s.loginBackoff.RecordSuccess(body.Email)

	session, rawToken, err := s.createSession(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("app user logged in", "request_id", requestIDFromContext(r.Context()), "user_id", user.ID, "session_id", session.ID)
	s.audit(r.Context(), "login.succeeded", auditSeverityInfo, user.ID, "", s.auditHomeIDForUser(r.Context(), user.ID), requestIDFromContext(r.Context()), "session", session.ID, nil)
	setSessionCookie(w, r, rawToken, session.ExpiresAt)

	writeJSON(w, http.StatusOK, map[string]any{
		"user":          sanitizeUser(user),
		"session_id":    session.ID,
		"session_token": rawToken,
		"expires_at":    session.ExpiresAt,
	})
}

func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.NewPassword) < 8 {
		http.Error(w, "new password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(auth.User.PasswordHash), []byte(body.CurrentPassword)); err != nil {
		http.Error(w, "invalid current password", http.StatusUnauthorized)
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), auth.User.ID, string(passwordHash), false, "", true, auth.Session.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	user, err := s.store.GetUserByID(r.Context(), auth.User.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r.Context(), "password.changed", auditSeverityInfo, auth.User.ID, "", s.auditHomeIDForUser(r.Context(), auth.User.ID), requestIDFromContext(r.Context()), "user", auth.User.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": sanitizeUser(user)})
}

func (s *Server) handleAuthInvitationPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	invitation, ok := s.loadUsableInvitation(w, r, body.Token)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email":      invitation.Email,
		"role":       invitation.Role,
		"expires_at": invitation.ExpiresAt,
	})
}

func (s *Server) handleAuthInvitationSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Token    string `json:"token"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	if len(body.Password) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	invitation, ok := s.loadUsableInvitation(w, r, body.Token)
	if !ok {
		return
	}
	if !strings.EqualFold(invitation.Email, body.Email) {
		http.Error(w, "invitation email does not match signup email", http.StatusForbidden)
		return
	}
	if _, err := s.store.GetUserByEmail(r.Context(), body.Email); err == nil {
		http.Error(w, "user already exists; sign in to accept this invite", http.StatusConflict)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	user := domain.User{
		ID:           newID("usr"),
		Email:        body.Email,
		PasswordHash: string(passwordHash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.CreateUserAndAcceptHomeInvitation(r.Context(), invitation.ID, user, invitation.Role); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	session, rawToken, err := s.createSession(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r.Context(), "invitation.signup", auditSeverityInfo, user.ID, "", invitation.HomeID, requestIDFromContext(r.Context()), "invitation", invitation.ID, map[string]any{"email_hash": stableAuditTarget(user.Email)})
	setSessionCookie(w, r, rawToken, session.ExpiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":          sanitizeUser(user),
		"session_id":    session.ID,
		"session_token": rawToken,
		"expires_at":    session.ExpiresAt,
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if err := s.store.RevokeSession(r.Context(), auth.Session.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.DeleteAPNSDevicesForSession(r.Context(), auth.Session.ID); err != nil {
		s.logger.Warn("failed to delete APNs devices for revoked session", "session_id", auth.Session.ID, "error", err)
	}
	if err := s.store.DeleteWebPushSubscriptionsForSession(r.Context(), auth.Session.ID); err != nil {
		s.logger.Warn("failed to delete Web Push subscriptions for revoked session", "session_id", auth.Session.ID, "error", err)
	}
	clearSessionCookie(w, r)
	s.logger.Info("app session revoked", "request_id", requestIDFromContext(r.Context()), "user_id", auth.User.ID, "session_id", auth.Session.ID)
	s.audit(r.Context(), "session.revoked", auditSeverityInfo, auth.User.ID, "", s.auditHomeIDForUser(r.Context(), auth.User.ID), requestIDFromContext(r.Context()), "session", auth.Session.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       sanitizeUser(auth.User),
		"session_id": auth.Session.ID,
		"expires_at": auth.Session.ExpiresAt,
	})
}

func (s *Server) handleAppWebSocketTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}

	rawTicket := newToken()
	expiresAt, err := s.store.CreateAppWebSocketTicket(r.Context(), hashToken(rawTicket), auth.Session.ID, auth.User.ID, 90*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("issued app websocket ticket", "request_id", requestIDFromContext(r.Context()), "user_id", auth.User.ID, "session_id", auth.Session.ID, "expires_at", expiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{
		"ticket":         rawTicket,
		"expires_at":     expiresAt,
		"websocket_path": "/ws/app?app_ticket=" + rawTicket,
	})
}

func (s *Server) handleAgentWebSocket(w http.ResponseWriter, r *http.Request) {
	if err := enforceSameOriginIfPresent(r); err != nil {
		s.metrics.IncAuthFailure("agent_bad_origin")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Hank-Agent-ID"))
	token := ""
	if headerToken, err := bearerToken(r.Header.Get("Authorization")); err == nil {
		token = headerToken
	}
	if agentID == "" || token == "" {
		s.metrics.IncAuthFailure("agent_missing_credentials")
		s.logger.Warn("agent websocket missing credentials", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r))
		http.Error(w, "unauthorized agent", http.StatusUnauthorized)
		return
	}

	allowed, limitErr := s.store.AllowRateLimit(r.Context(), "agent", clientIP(r), 40, time.Minute)
	if limitErr != nil {
		allowed = s.limiter.Allow("agent:"+clientIP(r), 40, time.Minute)
	}
	if !allowed {
		s.metrics.IncAuthFailure("agent_rate_limited")
		s.logger.Warn("agent websocket rate limited", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r), "agent_id", agentID)
		http.Error(w, "too many agent auth attempts", http.StatusTooManyRequests)
		return
	}

	record, err := s.store.ValidateAgentToken(r.Context(), hashToken(token))
	if err != nil || record.Agent.ID != agentID {
		s.metrics.IncAuthFailure("agent_bad_token")
		s.logger.Warn("agent websocket rejected", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r), "agent_id", agentID)
		http.Error(w, "unauthorized agent", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		s.logger.Error("failed to accept agent websocket", "error", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "connection closed")
	conn.SetReadLimit(maxWSMessageBytes)

	ctx := r.Context()
	peer := newWSPeer(conn)
	sender := agentReplyBinding{homeID: record.Home.ID, agentID: record.Agent.ID, peer: peer}
	agentConnectionID := ""
	registeredPrimary := false
	fileSearchStarted := false

	s.logger.Info("agent websocket connected", "request_id", requestIDFromContext(r.Context()), "agent_id", record.Agent.ID, "home_id", record.Home.ID)
	defer func() {
		s.disconnectAgentConnection(record.Home.ID, record.Agent.ID, agentConnectionID, registeredPrimary)
		s.logger.Info("agent websocket disconnected", "request_id", requestIDFromContext(r.Context()), "agent_id", record.Agent.ID, "home_id", record.Home.ID)
	}()

	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			if closeStatus(err) != websocket.StatusNormalClosure {
				s.logger.Warn("agent websocket read failed", "agent_id", record.Agent.ID, "error", err)
			}
			return
		}

		switch envelope.Type {
		case protocol.TypeAgentRegister:
			fileSearchStarted = false
			payload, err := protocol.DecodePayload[protocol.AgentRegister](envelope)
			if err != nil {
				s.writePeerError(ctx, peer, protocol.TypeAgentError, "", record.Agent.ID, record.Home.ID, "bad_register_payload", err.Error(), nil)
				continue
			}
			if payload.AgentID != "" && payload.AgentID != record.Agent.ID {
				s.writePeerError(ctx, peer, protocol.TypeAgentError, "", record.Agent.ID, record.Home.ID, "agent_id_mismatch", "register payload agent ID does not match token agent ID", nil)
				continue
			}

			now := time.Now().UTC()
			agent := record.Agent
			agent.Status = domain.AgentStatusOnline
			agent.LastSeenAt = &now
			agent.UpdatedAt = now
			agentType, err := registeredAgentType(record.Agent.AgentType, payload.AgentType)
			if err != nil {
				s.writePeerError(ctx, peer, protocol.TypeAgentError, "", record.Agent.ID, record.Home.ID, "agent_type_mismatch", err.Error(), nil)
				continue
			}
			agent.AgentType = agentType
			registeredPrimary = agentType == AgentTypePrimary
			if payload.HomeName != "" && registeredPrimary {
				record.Home.Name = payload.HomeName
			}
			connectionID, err := s.registerAgentConnection(ctx, record.Home.ID, agent, peer, payload, agentConnectionID)
			if err != nil {
				s.logger.Error("failed to register agent", "agent_id", agent.ID, "error", err)
				return
			}
			agentConnectionID = connectionID

			updateAcknowledged := false
			if payload.UpdateAssignmentID != "" && payload.Metadata["platform"] == "linux" && slices.Contains(payload.Capabilities, protocol.CommandSystemUpdateApply) {
				updateAcknowledged, err = s.store.AcknowledgeLinuxAgentVersion(ctx, record.Home.ID, agent.ID, payload.UpdateAssignmentID, payload.Metadata["app_version"], now)
				if err != nil {
					s.logger.Error("failed to acknowledge Linux agent update", "agent_id", agent.ID, "assignment_id", payload.UpdateAssignmentID, "error", err)
					return
				}
			}
			reply, err := protocol.NewEnvelope(protocol.TypeAgentRegistered, "", agent.ID, record.Home.ID, protocol.AgentRegistered{
				AcceptedAt:         now,
				HomeID:             record.Home.ID,
				Message:            "agent registered",
				Capabilities:       payload.Capabilities,
				UpdateAssignmentID: payload.UpdateAssignmentID,
				UpdateAcknowledged: updateAcknowledged,
			})
			if err != nil {
				s.logger.Error("failed to encode registration reply", "agent_id", agent.ID, "error", err)
				return
			}
			if err := peer.Write(ctx, reply); err != nil {
				s.logger.Warn("failed to write registration reply", "agent_id", agent.ID, "error", err)
				return
			}
			go s.linuxUpdates.ReconcileAgent(context.Background(), record.Home.ID, agent.ID)
			go s.reconcileFleetAgent(ctx, record.Home.ID, agent.ID)
			if supportsFileSearchIndex(payload.Capabilities) {
				fileSearchStarted = true
				go s.startFileSearchIndexer(ctx, record.Home.ID, agent.ID)
			}

		case protocol.TypeAgentHeartbeat:
			payload, err := protocol.DecodePayload[protocol.AgentHeartbeat](envelope)
			if err != nil {
				s.writePeerError(ctx, peer, protocol.TypeAgentError, envelope.RequestID, record.Agent.ID, record.Home.ID, "bad_heartbeat_payload", err.Error(), nil)
				continue
			}
			now := time.Now().UTC()
			if !s.recordAgentHeartbeat(ctx, record.Home.ID, record.Agent.ID, agentConnectionID, payload, now) {
				continue
			}
			go s.reconcileFleetAgent(ctx, record.Home.ID, record.Agent.ID)
			if !fileSearchStarted && supportsFileSearchIndex(payload.Capabilities) {
				fileSearchStarted = true
				go s.startFileSearchIndexer(ctx, record.Home.ID, record.Agent.ID)
			}
			if shouldScheduleNoteSync(registeredPrimary, payload.Capabilities) {
				if state, err := s.store.GetHomeNoteSyncState(ctx, record.Home.ID); err == nil {
					if state.LastManifestAt == nil || now.Sub(*state.LastManifestAt) > time.Minute {
						s.scheduleHomeSync(record.Home, record.Agent.ID)
					}
				} else if errors.Is(err, store.ErrNotFound) {
					s.scheduleHomeSync(record.Home, record.Agent.ID)
				}
			}

		case protocol.TypeAgentEvent:
			if envelope.AgentID != "" && envelope.AgentID != record.Agent.ID {
				s.logger.Warn("agent event rejected for forged identity", "authenticated_agent_id", record.Agent.ID, "claimed_agent_id", envelope.AgentID, "home_id", record.Home.ID)
				continue
			}
			envelope.AgentID = record.Agent.ID
			s.handleAgentEvent(ctx, record.Home.ID, envelope)

		case protocol.TypeCloudResponse:
			s.handleAgentResponse(ctx, sender, envelope)

		case protocol.TypeFileTransferReady:
			s.handleTransferReady(sender, envelope)

		case protocol.TypeFileTransferData:
			s.handleTransferData(sender, envelope)

		case protocol.TypeFileTransferComplete:
			s.handleTransferComplete(sender, envelope)

		case protocol.TypeFileTransferError:
			s.handleTransferError(sender, envelope)

		default:
			s.writePeerError(ctx, peer, protocol.TypeAgentError, envelope.RequestID, record.Agent.ID, record.Home.ID, "unsupported_message", "unsupported envelope type", nil)
		}
	}
}

func shouldScheduleNoteSync(registeredPrimary bool, capabilities []string) bool {
	return registeredPrimary && slices.Contains(capabilities, "notes.sync")
}

func (s *Server) handleAppWebSocket(w http.ResponseWriter, r *http.Request) {
	if err := enforceSameOriginIfPresent(r); err != nil {
		s.metrics.IncAuthFailure("app_ws_bad_origin")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	auth, err := s.appAuthFromRequest(r)
	if err != nil {
		s.metrics.IncAuthFailure("app_ws_unauthorized")
		s.logger.Warn("app websocket unauthorized", "request_id", requestIDFromContext(r.Context()), "client_ip", clientIP(r))
		http.Error(w, "unauthorized app session", http.StatusUnauthorized)
		return
	}
	if auth.User.PasswordChangeRequired {
		http.Error(w, "password_change_required", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		s.logger.Error("failed to accept app websocket", "error", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "connection closed")
	conn.SetReadLimit(maxWSMessageBytes)

	ctx := r.Context()
	appPeer := newWSPeer(conn)
	appConn := s.router.RegisterApp(auth.Session.ID, auth.User.ID, appPeer)
	_ = s.store.MarkAppConnection(ctx, appConn.connectionID, s.runtimeID, auth.Session.ID, auth.User.ID, true)
	s.metrics.SetOnlineApps(s.router.AppCount())
	s.logger.Info("app websocket connected", "request_id", requestIDFromContext(r.Context()), "user_id", auth.User.ID, "session_id", auth.Session.ID, "connection_id", appConn.connectionID)
	defer func() {
		s.collaboration.removeApp(auth.Session.ID)
		s.router.UnregisterApp(appConn.connectionID)
		_ = s.store.MarkAppConnection(context.Background(), appConn.connectionID, s.runtimeID, auth.Session.ID, auth.User.ID, false)
		s.metrics.SetOnlineApps(s.router.AppCount())
		s.logger.Info("app websocket disconnected", "request_id", requestIDFromContext(r.Context()), "user_id", auth.User.ID, "session_id", auth.Session.ID, "connection_id", appConn.connectionID)
	}()

	for {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, conn, &envelope); err != nil {
			if closeStatus(err) != websocket.StatusNormalClosure {
				s.logger.Warn("app websocket read failed", "session_id", auth.Session.ID, "error", err)
			}
			return
		}

		if envelope.Type != protocol.TypeAppCommand {
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "unsupported_message", "app websocket only accepts app.command messages", nil)
			continue
		}

		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil {
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "bad_command_payload", err.Error(), nil)
			continue
		}
		if envelope.RequestID == "" {
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "invalid_request", "request_id is required", nil)
			continue
		}

		// Destination execution receipts are internal to the approval-bound worker.
		// Even an administrator must not forge or replay them through raw relay.
		if strings.HasPrefix(command.Command, "assistant.") || strings.HasPrefix(command.Command, "fleet.") {
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "permission_denied", "assistant operations require the task execution API", nil)
			continue
		}
		if command.Command == "files.list_page" {
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "permission_denied", "file catalog paging is internal", nil)
			continue
		}

		if s.handleRealtimeCommand(ctx, appConn, appPeer, envelope, auth, command) {
			continue
		}

		profileScopedNotes, err := isProfileScopedNotesCommand(command)
		if err != nil {
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "bad_command_payload", err.Error(), nil)
			continue
		}
		if profileScopedNotes {
			if err := s.handleCloudNotesCommand(ctx, appPeer, envelope, auth, command); err != nil {
				s.logger.Warn("cloud profile notes command failed", "request_id", envelope.RequestID, "command", command.Command, "error", err)
			}
			continue
		}

		home, membership, err := s.requireSingletonHomeMembership(ctx, auth.User.ID)
		if err != nil {
			s.metrics.IncRouteFailure("home_not_found")
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "home_not_found", "home not found for this user", nil)
			continue
		}
		envelope.HomeID = home.ID

		if feature := featureForCommand(command.Command); feature != "" {
			if err := s.requireHomeFeature(ctx, home, membership, auth.User.ID, feature); err != nil {
				code := "permission_denied"
				if errors.Is(err, errFeaturePermissionDenied) {
					s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, code, err.Error(), nil)
					continue
				}
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "permission_check_failed", err.Error(), nil)
				continue
			}
		}
		if strings.HasPrefix(command.Command, "files.") {
			if err := s.authorizeFileCommandPolicy(ctx, home.ID, command); err != nil {
				s.audit(ctx, "file_operation.denied", auditSeverityWarning, auth.User.ID, "", home.ID, envelope.RequestID, "file_policy", command.Command, map[string]any{"reason": err.Error()})
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "permission_denied", err.Error(), nil)
				continue
			}
		}
		if strings.HasPrefix(command.Command, "hermes.") && membership.Role != domain.HomeRoleAdmin {
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "permission_denied", errAdminRoleRequired.Error(), nil)
			continue
		}
		// RMM management commands (device actions, wake-on-LAN, remote shell)
		// are admin-only. Shell execution is additionally audited on every
		// invocation; the target agent must also have shell.exec enabled
		// locally by the machine owner.
		if isManagementCommand(command.Command) {
			if membership.Role != domain.HomeRoleAdmin {
				s.audit(ctx, "agent.command.denied", auditSeverityWarning, auth.User.ID, "", home.ID, envelope.RequestID, "agent_command", command.Command, map[string]any{"reason": "admin_required", "agent_id": strings.TrimSpace(envelope.AgentID)})
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "permission_denied", errAdminRoleRequired.Error(), nil)
				continue
			}
			if command.Command == "shell.exec" {
				s.audit(ctx, "agent.shell.requested", auditSeverityWarning, auth.User.ID, "", home.ID, envelope.RequestID, "agent_shell", strings.TrimSpace(envelope.AgentID), map[string]any{"command_hash": stableAuditTarget(string(command.Body))})
			}
		}
		if command.Command == protocol.CommandAppsInvoke {
			if err := s.authorizeAppsInvokeCommand(ctx, home.ID, membership, command); err != nil {
				code := "permission_denied"
				if errors.Is(err, errAppInvokeBadPayload) {
					code = "bad_command_payload"
				} else if errors.Is(err, errAppInvokeUnavailable) {
					code = "app_unavailable"
				} else if !errors.Is(err, errAppInvokeAccessDenied) {
					code = "permission_check_failed"
				}
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, code, err.Error(), nil)
				continue
			}
		}
		if command.Command == protocol.CommandAppsInvoke {
			command.Body, err = stampAppInvocation(command.Body, home.ID, auth.User.ID, membership.Role)
			if err != nil {
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", home.ID, "bad_command_payload", "App context must be an object", nil)
				continue
			}
		}

		if strings.HasPrefix(command.Command, "notes.") {
			if err := s.handleCloudNotesCommand(ctx, appPeer, envelope, auth, command); err != nil {
				s.logger.Warn("cloud notes command failed", "request_id", envelope.RequestID, "home_id", envelope.HomeID, "command", command.Command, "error", err)
			}
			continue
		}

		if body, handled, err := s.managedFileRecovery(ctx, home, auth, command); handled {
			if err != nil {
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", home.ID, "file_job_rejected", err.Error(), nil)
			} else {
				response, encodeErr := protocol.NewEnvelope(protocol.TypeAppResponse, envelope.RequestID, "", home.ID, body)
				if encodeErr == nil {
					_ = appPeer.Write(ctx, response)
				}
			}
			continue
		}

		agentConn, ok := s.router.ResolveAgent(home.ID, strings.TrimSpace(envelope.AgentID))
		if !ok {
			s.metrics.IncRouteFailure("agent_offline")
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "agent_offline", "target Hank Agent is offline", nil)
			continue
		}
		if command.Command == "shell.exec" {
			var request protocol.ShellExecRequest
			if json.Unmarshal(command.Body, &request) != nil || request.Validate() != nil {
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", home.ID, "invalid_command_payload", "invalid shell command or timeout", nil)
				continue
			}
			if !s.router.supportsCurrentAgent(agentConn, "shell.exec") {
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", home.ID, "shell_disabled", "remote shell is unavailable on the selected agent", nil)
				continue
			}
		}
		if strings.HasPrefix(command.Command, "apps.") {
			primary, present := s.router.GetAgent(home.ID)
			if !present || primary != agentConn {
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", home.ID, "unsupported_target", "Installable apps run on the primary Hank Agent", nil)
				continue
			}
			if protocol.RequiresAppSandbox(command.Command) && !s.router.supportsCurrentAgent(agentConn, protocol.CapabilityAppsSandboxV1) {
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", home.ID, "agent_update_required", "Update the Hank Agent to use restricted apps", nil)
				continue
			}
		}
		var fileJobID string
		command, fileJobID, err = s.prepareManagedFileCommand(ctx, home, auth, agentConn.agent.ID, command)
		if err != nil {
			s.metrics.IncRouteFailure("file_job_rejected")
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "file_job_rejected", err.Error(), nil)
			continue
		}

		var shellSessionID string
		if strings.HasPrefix(command.Command, "shell.session.") {
			shellSessionID, err = s.authorizeShellSessionCommand(command, home.ID, auth.User.ID, agentConn.agent.ID)
			if err != nil {
				s.audit(ctx, "agent.shell.denied", auditSeverityWarning, auth.User.ID, "", home.ID, envelope.RequestID, "agent_shell", agentConn.agent.ID, map[string]any{"reason": err.Error(), "operation": command.Command})
				s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "shell_session_denied", err.Error(), nil)
				continue
			}
			s.audit(ctx, "agent.shell.session", auditSeverityWarning, auth.User.ID, "", home.ID, envelope.RequestID, "agent_shell", agentConn.agent.ID, map[string]any{"operation": command.Command})
		}

		timeout := s.timeoutForCommand(command.Command)
		if command.Command == "shell.exec" {
			var request protocol.ShellExecRequest
			_ = json.Unmarshal(command.Body, &request) // validated above
			timeout = request.Timeout() + 10*time.Second
		}
		persistedBody := command.Body
		if command.Command == protocol.CommandShellSessionInput {
			persistedBody = nil
		}
		if err := s.store.CreateRelayRequest(ctx, envelope.RequestID, envelope.HomeID, appConn.connectionID, command.Command, persistedBody, timeout); err != nil {
			s.shellSessions.complete(command.Command, shellSessionID, false)
			s.metrics.IncRouteFailure("relay_persist_failed")
			s.failFileJob(context.Background(), fileJobID, "failed", "request could not be persisted")
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "request_rejected", "request could not be persisted", nil)
			continue
		}
		pending, err := s.router.AddPending(context.Background(), envelope.RequestID, envelope.HomeID, command.Command, shellSessionID, fileJobID, agentConn.replyBinding(), appConn, timeout, s.handlePendingTimeout)
		if err != nil {
			s.shellSessions.complete(command.Command, shellSessionID, false)
			_ = s.store.FailRelayRequest(context.Background(), envelope.RequestID, "failed", "request_rejected", err.Error())
			s.failFileJob(context.Background(), fileJobID, "failed", err.Error())
			code := "request_rejected"
			statusMessage := err.Error()
			if errors.Is(err, ErrTooManyInFlight) {
				code = "too_many_in_flight_requests"
			}
			s.metrics.IncRouteFailure(code)
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, code, statusMessage, nil)
			continue
		}
		pending.homeAssistant = homeAssistantAuditForCommand(command, auth.User.ID, "human")
		pending.fileSearchDirs = fileSearchInvalidations(command)
		s.logger.Info("routing app command", "request_id", envelope.RequestID, "session_id", auth.Session.ID, "user_id", auth.User.ID, "home_id", envelope.HomeID, "agent_id", agentConn.agent.ID, "command", command.Command)

		relay, err := protocol.NewEnvelope(protocol.TypeCloudCommand, envelope.RequestID, agentConn.agent.ID, envelope.HomeID, command)
		if err != nil {
			s.shellSessions.complete(command.Command, shellSessionID, false)
			if _, ok := s.router.ResolvePending(envelope.RequestID); ok {
				s.metrics.IncRouteFailure("encoding_failed")
			}
			_ = s.store.FailRelayRequest(context.Background(), envelope.RequestID, "failed", "encoding_failed", err.Error())
			s.failFileJob(context.Background(), fileJobID, "failed", err.Error())
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "encoding_failed", err.Error(), nil)
			continue
		}

		s.markFileJobRunning(ctx, fileJobID)
		if err := agentConn.peer.Write(ctx, relay); err != nil {
			s.shellSessions.complete(command.Command, shellSessionID, false)
			if failedPending, ok := s.router.ResolvePending(envelope.RequestID); ok {
				s.metrics.IncRouteFailure("route_write_failed")
				s.auditHomeAssistantControl(context.Background(), failedPending, "failed", "route_write_failed")
			}
			_ = s.store.FailRelayRequest(context.Background(), envelope.RequestID, "failed", "route_write_failed", err.Error())
			s.failFileJob(context.Background(), fileJobID, "failed", err.Error())
			s.writePeerError(ctx, appPeer, protocol.TypeAppError, envelope.RequestID, "", envelope.HomeID, "route_write_failed", err.Error(), nil)
			continue
		}
		_ = s.store.MarkRelayRequestSent(context.Background(), envelope.RequestID, agentConn.connectionID)
	}
}

func (s *Server) timeoutForCommand(command string) time.Duration {
	if strings.HasPrefix(command, "mcp.context.") {
		return 20 * time.Second
	}
	if command == "files.search" {
		if s.requestTimeout < 3*time.Minute {
			return 3 * time.Minute
		}
	}
	if (command == "files.list" || command == "files.list_page") && s.requestTimeout < 2*time.Minute {
		return 2 * time.Minute
	}
	if command == "files.move" {
		timeout := s.requestTimeout * 15
		if timeout < 30*time.Minute {
			return 30 * time.Minute
		}
		return timeout
	}
	return s.requestTimeout
}

func (s *Server) handleAgentResponse(ctx context.Context, sender agentReplyBinding, envelope protocol.Envelope) {
	if s.agentRequests.Resolve(sender, envelope) {
		return
	}

	pending, ok := s.router.ResolveAgentPending(sender, envelope)
	if !ok {
		return
	}

	envelope.HomeID, envelope.AgentID = sender.homeID, sender.agentID
	if err := s.completePendingFileJob(ctx, pending, envelope); err != nil {
		envelope = protocol.NewErrorEnvelope(protocol.TypeCloudResponse, envelope.RequestID, envelope.AgentID, envelope.HomeID, "invalid_job_response", err.Error(), nil)
	}
	duration := time.Since(pending.startedAt)
	failed := envelope.Error != nil
	outcome := "succeeded"
	reason := ""
	if failed {
		outcome = "failed"
		reason = envelope.Error.Code
	}
	s.auditHomeAssistantControl(ctx, pending, outcome, reason)
	s.shellSessions.complete(pending.command, pending.shellSessionID, !failed)
	s.metrics.RecordCommand(pending.command, duration, failed)
	s.metrics.RecordRelay(duration, failed)
	s.logger.Info("agent response relayed", "request_id", envelope.RequestID, "home_id", envelope.HomeID, "agent_id", envelope.AgentID, "command", pending.command, "duration", duration.String(), "failed", failed)

	if envelope.Error != nil {
		_ = s.store.FailRelayRequest(context.Background(), envelope.RequestID, "failed", envelope.Error.Code, envelope.Error.Message)
		_ = pending.app.peer.Write(ctx, protocol.NewErrorEnvelope(protocol.TypeAppError, envelope.RequestID, envelope.AgentID, envelope.HomeID, envelope.Error.Code, envelope.Error.Message, envelope.Error.Details))
		return
	}
	_ = s.store.CompleteRelayRequest(context.Background(), envelope.RequestID, envelope.Payload)
	for _, dir := range pending.fileSearchDirs {
		if err := s.store.MarkFileSearchDirectoryDue(ctx, pending.homeID, pending.target.agentID, dir.SourceID, dir.Path); err != nil {
			s.logger.Warn("failed to schedule file search refresh", "agent_id", pending.target.agentID, "error", err)
		}
	}

	response := protocol.Envelope{
		Version:   protocol.Version,
		Type:      protocol.TypeAppResponse,
		RequestID: envelope.RequestID,
		AgentID:   envelope.AgentID,
		HomeID:    envelope.HomeID,
		Timestamp: time.Now().UTC(),
		Payload:   envelope.Payload,
	}
	_ = pending.app.peer.Write(ctx, response)
	s.emitCommandSideEffect(ctx, pending.command, envelope.Payload)
}

func (s *Server) handlePendingTimeout(ctx context.Context, pending *pendingRequest) {
	s.shellSessions.complete(pending.command, pending.shellSessionID, false)
	duration := time.Since(pending.startedAt)
	s.metrics.IncRouteFailure("request_timeout")
	s.metrics.RecordCommand(pending.command, duration, true)
	s.metrics.RecordRelay(duration, true)
	_ = s.store.FailRelayRequest(context.Background(), pending.requestID, "timed_out", "request_timeout", "agent did not respond before timeout")
	s.failFileJob(context.Background(), pending.fileJobID, "rollback_required", "agent did not respond before timeout")
	s.logger.Warn("app command timed out", "request_id", pending.requestID, "home_id", pending.homeID, "command", pending.command, "duration", duration.String())
	s.auditHomeAssistantControl(ctx, pending, "failed", "request_timeout")
	_ = pending.app.peer.Write(ctx, protocol.NewErrorEnvelope(protocol.TypeAppError, pending.requestID, "", pending.homeID, "request_timeout", "agent did not respond before timeout", nil))
}

func homeAssistantAuditForCommand(command protocol.RoutedCommand, actorUserID string, initiator string) *pendingHomeAssistantAudit {
	if command.Command != "homeassistant.call_service" {
		return nil
	}
	var request protocol.HomeAssistantCallServiceRequest
	if err := json.Unmarshal(command.Body, &request); err != nil {
		return nil
	}
	var body struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil || strings.TrimSpace(body.EntityID) == "" {
		return nil
	}
	operation := strings.Trim(strings.TrimSpace(request.Domain)+"."+strings.TrimSpace(request.Service), ".")
	return &pendingHomeAssistantAudit{
		actorUserID: strings.TrimSpace(actorUserID),
		entityID:    strings.TrimSpace(body.EntityID),
		operation:   operation,
		initiator:   strings.TrimSpace(initiator),
	}
}

func (s *Server) auditHomeAssistantControl(ctx context.Context, pending *pendingRequest, outcome string, reason string) {
	if pending == nil || pending.homeAssistant == nil {
		return
	}
	control := pending.homeAssistant
	metadata := map[string]any{
		"initiator": control.initiator,
		"operation": control.operation,
		"outcome":   outcome,
	}
	if strings.TrimSpace(reason) != "" {
		metadata["reason"] = strings.TrimSpace(reason)
	}
	severity := auditSeverityInfo
	if outcome != "succeeded" {
		severity = auditSeverityWarning
	}
	s.audit(ctx, "homeassistant.control."+outcome, severity, control.actorUserID, "", pending.homeID, pending.requestID, "homeassistant_entity", control.entityID, metadata)
}

func (s *Server) createSession(ctx context.Context, userID string) (domain.AppSession, string, error) {
	rawToken := newToken()
	now := time.Now().UTC()
	session := domain.AppSession{
		ID:        newID("sess"),
		UserID:    userID,
		TokenHash: hashToken(rawToken),
		ExpiresAt: now.Add(s.sessionTTL),
		CreatedAt: now,
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return domain.AppSession{}, "", err
	}
	return session, rawToken, nil
}

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) (authContext, bool) {
	auth, err := s.appAuthFromRequest(r)
	if err != nil {
		s.metrics.IncAuthFailure("app_http_unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return authContext{}, false
	}
	if err := s.requireCSRFForCookieWrite(r); err != nil {
		s.metrics.IncAuthFailure("csrf_invalid")
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return authContext{}, false
	}
	if auth.User.PasswordChangeRequired && !passwordChangeAllowedPath(r.URL.Path) {
		http.Error(w, "password_change_required", http.StatusForbidden)
		return authContext{}, false
	}
	return auth, true
}

func (s *Server) requireCSRFForCookieWrite(r *http.Request) error {
	if !unsafeHTTPMethod(r.Method) {
		return nil
	}
	if sessionTokenFromCookie(r) == "" {
		return nil
	}
	if _, err := bearerToken(r.Header.Get("Authorization")); err == nil {
		return nil
	}
	cookieToken := csrfTokenFromCookie(r)
	headerToken := strings.TrimSpace(r.Header.Get(csrfHeaderName))
	if cookieToken == "" || headerToken == "" || cookieToken != headerToken {
		return errors.New("csrf token mismatch")
	}
	return nil
}

func (s *Server) appAuthFromRequest(r *http.Request) (authContext, error) {
	rawToken := sessionTokenFromCookie(r)
	if rawToken == "" {
		token, err := bearerToken(r.Header.Get("Authorization"))
		if err == nil {
			rawToken = token
		}
	}
	if rawToken == "" {
		appTicket := strings.TrimSpace(r.URL.Query().Get("app_ticket"))
		if appTicket != "" {
			ticket, err := s.store.ConsumeAppWebSocketTicket(r.Context(), hashToken(appTicket))
			if err != nil {
				return authContext{}, err
			}
			session, err := s.store.GetSessionByID(r.Context(), ticket.SessionID)
			if err != nil {
				return authContext{}, err
			}
			user, err := s.store.GetUserByID(r.Context(), session.UserID)
			if err != nil {
				return authContext{}, err
			}
			return authContext{User: user, Session: session}, nil
		}
	}
	if rawToken == "" {
		return authContext{}, errors.New("missing session token")
	}

	session, err := s.store.GetSessionByHash(r.Context(), hashToken(rawToken))
	if err != nil {
		return authContext{}, err
	}
	user, err := s.store.GetUserByID(r.Context(), session.UserID)
	if err != nil {
		return authContext{}, err
	}
	return authContext{User: user, Session: session}, nil
}

func (s *Server) writePeerError(ctx context.Context, peer *wsPeer, messageType string, requestID string, agentID string, homeID string, code string, message string, details map[string]any) {
	_ = peer.Write(ctx, protocol.NewErrorEnvelope(messageType, requestID, agentID, homeID, code, message, details))
}

func sanitizeUser(user domain.User) map[string]any {
	return map[string]any{
		"id":                       user.ID,
		"email":                    user.Email,
		"display_name":             user.DisplayName,
		"password_change_required": user.PasswordChangeRequired,
		"password_login_enabled":   user.PasswordLoginEnabled,
		"password_changed_at":      user.PasswordChangedAt,
		"password_reset_at":        user.PasswordResetAt,
		"created_at":               user.CreatedAt,
		"updated_at":               user.UpdatedAt,
	}
}

func passwordChangeAllowedPath(path string) bool {
	switch path {
	case "/v1/me", "/v1/ui/bootstrap", "/v1/auth/change-password", "/v1/auth/logout":
		return true
	default:
		return false
	}
}

func (s *Server) loadUsableInvitation(w http.ResponseWriter, r *http.Request, token string) (domain.HomeInvitation, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return domain.HomeInvitation{}, false
	}
	invitation, err := s.store.GetHomeInvitationByTokenHash(r.Context(), hashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return domain.HomeInvitation{}, false
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return domain.HomeInvitation{}, false
	}
	if invitation.AcceptedAt != nil {
		http.Error(w, "invitation already accepted", http.StatusConflict)
		return domain.HomeInvitation{}, false
	}
	if invitation.ExpiresAt != nil && invitation.ExpiresAt.Before(time.Now().UTC()) {
		http.Error(w, "invitation expired", http.StatusGone)
		return domain.HomeInvitation{}, false
	}
	return invitation, true
}

func parseJSON(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPBodyBytes)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func nonNilSlice[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newID("req")
		}
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type requestIDContextKey struct{}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}
	return r.RemoteAddr
}

func closeStatus(err error) websocket.StatusCode {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Code
	}
	return websocket.StatusInternalError
}

func int64ToString(value int64) string {
	return strconv.FormatInt(value, 10)
}
