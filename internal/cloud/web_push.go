package cloud

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const maxWebPushPayloadBytes = 3 * 1024

type WebPushConfig struct {
	Enabled    bool
	PublicKey  string
	PrivateKey string
	Subject    string
}

type webPushRequestSender interface {
	Send(context.Context, domain.WebPushSubscription, []byte, string, WebPushConfig) (*http.Response, error)
}

type standardsWebPushSender struct {
	client *http.Client
}

func (s *standardsWebPushSender) Send(ctx context.Context, subscription domain.WebPushSubscription, payload []byte, topic string, cfg WebPushConfig) (*http.Response, error) {
	return webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys: webpush.Keys{
			P256dh: subscription.P256DH,
			Auth:   subscription.Auth,
		},
	}, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      webPushSubscriber(cfg.Subject),
		VAPIDPublicKey:  cfg.PublicKey,
		VAPIDPrivateKey: cfg.PrivateKey,
		TTL:             int(domain.WebPushDeliveryMaxAge / time.Second),
		Topic:           topic,
		Urgency:         webpush.UrgencyNormal,
		RecordSize:      webpush.MaxRecordSize,
	})
}

// webpush-go v1.4.0 adds mailto: to every subscriber that is not an HTTPS URL.
// Configuration accepts the standards-form mailto: URI, so pass only its
// address portion to the pinned dependency to avoid mailto:mailto:... claims.
func webPushSubscriber(subject string) string {
	return strings.TrimPrefix(strings.TrimSpace(subject), "mailto:")
}

type webPushPayload struct {
	SchemaVersion  int    `json:"schema_version"`
	NotificationID string `json:"notification_id"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Route          string `json:"route"`
	Tag            string `json:"tag"`
	UnreadCount    int    `json:"unread_count"`
	OccurredAt     string `json:"occurred_at"`
}

func encodeWebPushPayload(item domain.UserNotification, unreadCount int) ([]byte, error) {
	if !domain.ValidNotificationCategory(item.Category) || !strings.HasPrefix(item.TargetPath, "/dashboard") || strings.Contains(item.TargetPath, "\n") {
		return nil, errors.New("invalid notification payload")
	}
	occurredAt := item.LastOccurredAt.UTC()
	if occurredAt.IsZero() {
		return nil, errors.New("notification occurrence time is required")
	}
	payload, err := json.Marshal(webPushPayload{
		SchemaVersion: 1, NotificationID: item.ID, Category: item.Category, Severity: item.Severity,
		Title: item.Title, Body: item.Body, Route: item.TargetPath, Tag: item.CollapseKey, UnreadCount: unreadCount,
		OccurredAt: occurredAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, err
	}
	if len(payload) >= maxWebPushPayloadBytes {
		return nil, errors.New("Web Push payload exceeds size limit")
	}
	return payload, nil
}

func validateWebPushEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("Web Push endpoint must be an HTTPS URL without credentials or fragments")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("Web Push endpoint host is required")
	}
	if address, err := netip.ParseAddr(host); err == nil && !publicWebPushAddress(address) {
		return errors.New("Web Push endpoint resolves to a non-public address")
	}
	return nil
}

func publicWebPushAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range webPushReservedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var webPushReservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
}

func newWebPushHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range resolved {
			if !publicWebPushAddress(ip) {
				continue
			}
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("Web Push endpoint has no public address")
	}
	return &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type webPushDispatcher struct {
	store    *store.Store
	cfg      WebPushConfig
	logger   *slog.Logger
	sender   webPushRequestSender
	interval time.Duration
}

func newWebPushDispatcher(db *store.Store, cfg WebPushConfig, logger *slog.Logger, sender webPushRequestSender) *webPushDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	if sender == nil {
		sender = &standardsWebPushSender{client: newWebPushHTTPClient()}
	}
	return &webPushDispatcher{store: db, cfg: cfg, logger: logger, sender: sender, interval: 2 * time.Second}
}

func (d *webPushDispatcher) Run(ctx context.Context, owner string) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		if err := d.runOnce(ctx, owner, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Warn("Web Push dispatch cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *webPushDispatcher) runOnce(ctx context.Context, owner string, now time.Time) error {
	deliveries, err := d.store.ClaimDueWebPushDeliveries(ctx, owner, now, 25, time.Minute)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if err := d.dispatchClaim(ctx, owner, delivery, now); err != nil {
			return err
		}
	}
	return nil
}

func (d *webPushDispatcher) dispatchClaim(ctx context.Context, owner string, delivery domain.WebPushDelivery, now time.Time) error {
	deliveryContext, err := d.store.GetWebPushDeliveryContext(ctx, delivery.ID, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if !deliveryContext.SessionActive {
		return d.store.CancelWebPushDelivery(ctx, delivery.ID, owner, "session_inactive", now)
	}
	if !deliveryContext.CategoryEnabled {
		return d.store.CancelWebPushDelivery(ctx, delivery.ID, owner, "preference_disabled", now)
	}
	if deliveryContext.Notification.ReadAt != nil {
		return d.store.CancelWebPushDelivery(ctx, delivery.ID, owner, "notification_read", now)
	}
	if deliveryContext.Notification.LastOccurredAt.Before(now.Add(-domain.WebPushDeliveryMaxAge)) {
		return d.store.CancelWebPushDelivery(ctx, delivery.ID, owner, "notification_stale", now)
	}
	if err := validateWebPushEndpoint(deliveryContext.Subscription.Endpoint); err != nil {
		_ = d.store.RecordWebPushSubscriptionFailure(ctx, delivery.SubscriptionID, "invalid_endpoint", now)
		return d.store.FailWebPushDelivery(ctx, delivery.ID, owner, "invalid_endpoint", now)
	}
	unreadCount, err := d.store.CountUnreadUserNotifications(ctx, deliveryContext.Notification.UserID)
	if err != nil {
		return err
	}
	payload, err := encodeWebPushPayload(deliveryContext.Notification, unreadCount)
	if err != nil {
		_ = d.store.RecordWebPushSubscriptionFailure(ctx, delivery.SubscriptionID, "invalid_payload", now)
		return d.store.FailWebPushDelivery(ctx, delivery.ID, owner, "invalid_payload", now)
	}
	response, sendErr := d.sender.Send(ctx, deliveryContext.Subscription, payload, webPushTopic(deliveryContext.Notification.CollapseKey), d.cfg)
	if sendErr != nil {
		outcome := "send_error"
		_ = d.store.RecordWebPushSubscriptionFailure(ctx, delivery.SubscriptionID, outcome, now)
		if isTransientWebPushError(sendErr) {
			return d.store.RetryWebPushDelivery(ctx, delivery.ID, owner, now.Add(webPushRetryDelay(delivery.AttemptCount, 0)), outcome, now)
		}
		return d.store.FailWebPushDelivery(ctx, delivery.ID, owner, outcome, now)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
	statusClass := fmt.Sprintf("http_%d", response.StatusCode)
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		if err := d.store.CompleteWebPushDelivery(ctx, delivery.ID, owner, now); err != nil {
			return err
		}
		return d.store.RecordWebPushSubscriptionSuccess(ctx, delivery.SubscriptionID, now)
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		d.logger.Info("Web Push subscription expired", "subscription_id", delivery.SubscriptionID, "status", response.StatusCode)
		return d.store.InvalidateWebPushSubscription(ctx, delivery.SubscriptionID)
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		_ = d.store.RecordWebPushSubscriptionFailure(ctx, delivery.SubscriptionID, statusClass, now)
		retryAfter := parseWebPushRetryAfter(response.Header.Get("Retry-After"), now)
		return d.store.RetryWebPushDelivery(ctx, delivery.ID, owner, now.Add(webPushRetryDelay(delivery.AttemptCount, retryAfter)), statusClass, now)
	default:
		_ = d.store.RecordWebPushSubscriptionFailure(ctx, delivery.SubscriptionID, statusClass, now)
		return d.store.FailWebPushDelivery(ctx, delivery.ID, owner, statusClass, now)
	}
}

func webPushTopic(collapseKey string) string {
	sum := sha256.Sum256([]byte(collapseKey))
	return hex.EncodeToString(sum[:16])
}

func isTransientWebPushError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func parseWebPushRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}

func webPushRetryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > 6*time.Hour {
			return 6 * time.Hour
		}
		return retryAfter
	}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	base := time.Minute * time.Duration(1<<(attempt-1))
	jitterRange := max(int64(base/5), 1)
	jitter, err := rand.Int(rand.Reader, big.NewInt(jitterRange))
	if err != nil {
		return base
	}
	return base + time.Duration(jitter.Int64())
}
