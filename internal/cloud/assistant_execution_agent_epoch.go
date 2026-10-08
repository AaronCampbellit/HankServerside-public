package cloud

import (
	"encoding/hex"
	"github.com/dropfile/HankServerside/internal/protocol"
	"strings"
)

func (r *Router) assistantJournalEpoch(connection *agentConnection) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.agentsByHomeID[connection.homeID][connection.agent.ID] != connection {
		return ""
	}
	epoch := ""
	for _, capability := range connection.capabilities {
		if !strings.HasPrefix(capability, protocol.CapabilityAssistantJournalEpochPrefix) {
			continue
		}
		value := strings.TrimPrefix(capability, protocol.CapabilityAssistantJournalEpochPrefix)
		if _, err := hex.DecodeString(value); err != nil || len(value) != 32 || epoch != "" {
			return ""
		}
		epoch = value
	}
	return epoch
}
