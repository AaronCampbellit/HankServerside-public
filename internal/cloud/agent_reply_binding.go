package cloud

import "github.com/dropfile/HankServerside/internal/protocol"

// agentReplyBinding is captured from the selected connection before dispatch.
// The receiver constructs its binding from authenticated credentials and its
// own peer, never from envelope claims or the router's current connection.
// Peer identity survives re-registration on the same socket and lets replaced
// sockets finish their own work without accepting each other's replies.
type agentReplyBinding struct {
	homeID  string
	agentID string
	peer    *wsPeer
}

func (c *agentConnection) replyBinding() agentReplyBinding {
	return agentReplyBinding{homeID: c.homeID, agentID: c.agent.ID, peer: c.peer}
}

func (b agentReplyBinding) accepts(sender agentReplyBinding, envelope protocol.Envelope) bool {
	return b.peer != nil && b.homeID != "" && b.agentID != "" && b == sender &&
		(envelope.HomeID == "" || envelope.HomeID == sender.homeID) &&
		(envelope.AgentID == "" || envelope.AgentID == sender.agentID)
}
