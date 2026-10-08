package cloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	mcpSubscriptionIDMetaKey         = "io.modelcontextprotocol/subscriptionId"
	maxMCPSubscriptionRequestIDBytes = 1024
)

type mcpSubscriptionFilter struct {
	ToolsListChanged bool
}

func decodeMCPSubscriptionFilter(raw json.RawMessage) (mcpSubscriptionFilter, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return mcpSubscriptionFilter{}, errors.New("notifications must be an object")
	}
	var requested struct {
		ToolsListChanged     *bool `json:"toolsListChanged"`
		PromptsListChanged   *bool `json:"promptsListChanged"`
		ResourcesListChanged *bool `json:"resourcesListChanged"`
	}
	if err := json.Unmarshal(trimmed, &requested); err != nil {
		return mcpSubscriptionFilter{}, errors.New("notifications contains an invalid flag")
	}
	return mcpSubscriptionFilter{ToolsListChanged: requested.ToolsListChanged != nil && *requested.ToolsListChanged}, nil
}

func (s *Server) handleMCPSubscription(w http.ResponseWriter, r *http.Request, auth mcpAuthContext, requestID json.RawMessage, rawFilter json.RawMessage) {
	if len(requestID) > maxMCPSubscriptionRequestIDBytes {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(nil, -32600, "subscription request id is too large"))
		return
	}
	filter, err := decodeMCPSubscriptionFilter(rawFilter)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(requestID, -32602, "invalid subscription notification filter"))
		return
	}
	if s.mcpSubscriptions == nil {
		writeJSON(w, http.StatusOK, jsonrpcErrorResponse(requestID, -32603, "Subscription limit reached"))
		return
	}
	subscription, err := s.mcpSubscriptions.register(requestID, auth.Token.ID, auth.User.ID, filter.ToolsListChanged)
	if err != nil {
		writeJSON(w, http.StatusOK, jsonrpcErrorResponse(requestID, -32603, "Subscription limit reached"))
		return
	}
	closeReason := "client_cancelled"
	defer func() { s.mcpSubscriptions.unregister(subscription, closeReason) }()

	setMCPNoStoreHeaders(w.Header())
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	writeFrame := func(payload any) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(s.mcpSubscriptions.config.WriteTimeout))
		if err := writeMCPSSEFrame(w, payload); err != nil {
			s.metrics.IncMCPSubscriptionWriteFailure()
			closeReason = "write_failed"
			return false
		}
		if err := controller.Flush(); err != nil {
			s.metrics.IncMCPSubscriptionWriteFailure()
			closeReason = "write_failed"
			return false
		}
		return true
	}
	writeHeartbeat := func() bool {
		_ = controller.SetWriteDeadline(time.Now().Add(s.mcpSubscriptions.config.WriteTimeout))
		if err := writeMCPSSEHeartbeat(w); err != nil {
			s.metrics.IncMCPSubscriptionWriteFailure()
			closeReason = "write_failed"
			return false
		}
		if err := controller.Flush(); err != nil {
			s.metrics.IncMCPSubscriptionWriteFailure()
			closeReason = "write_failed"
			return false
		}
		return true
	}
	gracefulClose := func(reason string) {
		closeReason = reason
		result := map[string]any{
			"resultType": "complete",
			"_meta":      mcpSubscriptionResultMeta(requestID),
		}
		writeFrame(jsonrpcResultResponse(requestID, result))
	}

	honored := map[string]any{}
	if filter.ToolsListChanged {
		honored["toolsListChanged"] = true
	}
	acknowledged := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/subscriptions/acknowledged",
		"params": map[string]any{
			"notifications": honored,
			"_meta":         mcpSubscriptionMeta(requestID),
		},
	}
	if !writeFrame(acknowledged) {
		return
	}
	if !filter.ToolsListChanged {
		gracefulClose("empty_filter")
		return
	}

	heartbeats := time.NewTicker(s.mcpSubscriptions.config.HeartbeatInterval)
	defer heartbeats.Stop()
	credentialChecks := time.NewTicker(s.mcpSubscriptions.config.CredentialCheckInterval)
	defer credentialChecks.Stop()
	var expiry <-chan time.Time
	var expiryTimer *time.Timer
	if !auth.Token.AccessExpiresAt.IsZero() {
		duration := time.Until(auth.Token.AccessExpiresAt)
		if duration < 0 {
			duration = 0
		}
		expiryTimer = time.NewTimer(duration)
		expiry = expiryTimer.C
		defer expiryTimer.Stop()
	}

	for {
		select {
		case <-subscription.toolsChanged:
			changed := map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/tools/list_changed",
				"params":  map[string]any{"_meta": mcpSubscriptionMeta(requestID)},
			}
			if !writeFrame(changed) {
				return
			}
		case reason := <-subscription.closeRequested:
			gracefulClose(reason)
			return
		case <-heartbeats.C:
			if !writeHeartbeat() {
				return
			}
		case <-credentialChecks.C:
			if !s.mcpSubscriptionCredentialValid(r, auth) {
				gracefulClose("credential_invalid")
				return
			}
		case <-expiry:
			gracefulClose("token_expired")
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) mcpSubscriptionCredentialValid(r *http.Request, auth mcpAuthContext) bool {
	if s.store == nil || auth.Token.AccessTokenHash == "" {
		return true
	}
	token, err := s.store.GetMCPTokenByAccessHash(r.Context(), auth.Token.AccessTokenHash)
	if err != nil {
		return false
	}
	return token.ID == auth.Token.ID && token.UserID == auth.User.ID
}

func (s *Server) notifyMCPToolsChanged() {
	if s.mcpSubscriptions != nil {
		s.mcpSubscriptions.publishToolsChanged()
	}
}

func writeMCPSSEFrame(w io.Writer, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: message\ndata: %s\n\n", encoded)
	return err
}

func writeMCPSSEHeartbeat(w io.Writer) error {
	_, err := io.WriteString(w, ": keepalive\n\n")
	return err
}

func mcpSubscriptionMeta(requestID json.RawMessage) map[string]any {
	return map[string]any{mcpSubscriptionIDMetaKey: requestID}
}

func mcpSubscriptionResultMeta(requestID json.RawMessage) map[string]any {
	meta := mcpModernResultMeta()
	meta[mcpSubscriptionIDMetaKey] = requestID
	return meta
}
