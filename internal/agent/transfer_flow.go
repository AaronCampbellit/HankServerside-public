package agent

import (
	"context"
	"sync"

	"github.com/dropfile/HankServerside/internal/protocol"
)

type downloadWindow struct {
	mu                  sync.Mutex
	acked, sent, window int64
	changed             chan struct{}
}

func (f *downloadWindow) reserve(ctx context.Context, end int64) bool {
	for {
		f.mu.Lock()
		if end >= f.sent && end-f.acked <= f.window {
			f.sent = end
			f.mu.Unlock()
			return ctx.Err() == nil
		}
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-f.changed:
		}
	}
}

func (f *downloadWindow) acknowledge(offset int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if offset <= f.acked || offset > f.sent {
		return
	}
	f.acked = offset
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func (c *Client) handleTransferAck(envelope protocol.Envelope) {
	ack, err := protocol.DecodePayload[protocol.FileTransferAck](envelope)
	if err != nil {
		return
	}
	c.flowMu.Lock()
	flow := c.downloadFlows[envelope.RequestID]
	c.flowMu.Unlock()
	if flow != nil {
		flow.acknowledge(ack.Offset)
	}
}
