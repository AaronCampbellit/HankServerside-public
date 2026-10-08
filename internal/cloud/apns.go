package cloud

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

type APNSConfig struct {
	TeamID      string
	KeyID       string
	PrivateKey  string
	Topic       string
	Environment string
}

type PushNotification struct {
	Category string
	Title    string
	Body     string
	URL      string
	ThreadID string
}

type PushSender interface {
	Send(ctx context.Context, device domain.APNSDevice, notification PushNotification) error
}

type noopPushSender struct{}

func (noopPushSender) Send(context.Context, domain.APNSDevice, PushNotification) error {
	return nil
}

type apnsSender struct {
	cfg       APNSConfig
	key       *ecdsa.PrivateKey
	client    *http.Client
	logger    *slog.Logger
	tokenMu   sync.Mutex
	token     string
	tokenTime time.Time
}

func NewAPNSSender(cfg APNSConfig, logger *slog.Logger) PushSender {
	cfg.TeamID = strings.TrimSpace(cfg.TeamID)
	cfg.KeyID = strings.TrimSpace(cfg.KeyID)
	cfg.PrivateKey = strings.TrimSpace(strings.ReplaceAll(cfg.PrivateKey, `\n`, "\n"))
	cfg.Topic = strings.TrimSpace(cfg.Topic)
	cfg.Environment = normalizeAPNSEnvironment(cfg.Environment)
	if cfg.TeamID == "" || cfg.KeyID == "" || cfg.PrivateKey == "" || cfg.Topic == "" {
		return noopPushSender{}
	}
	key, err := parseAPNSPrivateKey(cfg.PrivateKey)
	if err != nil {
		if logger != nil {
			logger.Warn("APNs sender disabled because the private key could not be parsed", "error", err)
		}
		return noopPushSender{}
	}
	return &apnsSender{
		cfg:    cfg,
		key:    key,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

func (s *apnsSender) Send(ctx context.Context, device domain.APNSDevice, notification PushNotification) error {
	token := strings.TrimSpace(device.Token)
	if token == "" {
		return nil
	}
	authToken, err := s.authToken()
	if err != nil {
		return err
	}
	payload := apnsPayload(notification)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint(device)+"/3/device/"+token, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "bearer "+authToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("apns-topic", s.cfg.Topic)
	request.Header.Set("apns-push-type", "alert")
	request.Header.Set("apns-priority", "10")
	if notification.ThreadID != "" {
		request.Header.Set("apns-collapse-id", notification.ThreadID)
	}

	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("apns status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
}

func (s *apnsSender) endpoint(device domain.APNSDevice) string {
	environment := normalizeAPNSEnvironment(device.Environment)
	if environment == "" {
		environment = s.cfg.Environment
	}
	if environment == "production" {
		return "https://api.push.apple.com"
	}
	return "https://api.sandbox.push.apple.com"
}

func (s *apnsSender) authToken() (string, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.token != "" && time.Since(s.tokenTime) < 20*time.Minute {
		return s.token, nil
	}

	header := map[string]string{"alg": "ES256", "kid": s.cfg.KeyID}
	claims := map[string]any{"iss": s.cfg.TeamID, "iat": time.Now().Unix()}
	encodedHeader, err := jwtPart(header)
	if err != nil {
		return "", err
	}
	encodedClaims, err := jwtPart(claims)
	if err != nil {
		return "", err
	}
	signingInput := encodedHeader + "." + encodedClaims
	hash := sha256.Sum256([]byte(signingInput))
	r, sigS, err := ecdsa.Sign(rand.Reader, s.key, hash[:])
	if err != nil {
		return "", err
	}
	signature := append(fixedBigIntBytes(r, 32), fixedBigIntBytes(sigS, 32)...)
	s.token = signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
	s.tokenTime = time.Now()
	return s.token, nil
}

func jwtPart(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func fixedBigIntBytes(value *big.Int, size int) []byte {
	raw := value.Bytes()
	if len(raw) >= size {
		return raw[len(raw)-size:]
	}
	out := make([]byte, size)
	copy(out[size-len(raw):], raw)
	return out
}

func parseAPNSPrivateKey(value string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("missing PEM block")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if ecKey, ok := key.(*ecdsa.PrivateKey); ok {
			return ecKey, nil
		}
		return nil, errors.New("private key is not ECDSA")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func normalizeAPNSEnvironment(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "production", "prod":
		return "production"
	case "sandbox", "development", "debug":
		return "sandbox"
	default:
		return ""
	}
}

func apnsPayload(notification PushNotification) map[string]any {
	aps := map[string]any{
		"alert": map[string]string{
			"title": notification.Title,
			"body":  notification.Body,
		},
		"sound": "default",
	}
	if notification.Category != "" {
		aps["category"] = strings.ToUpper(strings.ReplaceAll(notification.Category, "_", "-"))
	}
	if notification.ThreadID != "" {
		aps["thread-id"] = notification.ThreadID
	}
	return map[string]any{
		"aps":           aps,
		"hank_url":      notification.URL,
		"hank_category": notification.Category,
	}
}
