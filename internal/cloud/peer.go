package cloud

import (
	"context"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type wsPeer struct {
	conn      *websocket.Conn
	writeOnce sync.Once
	writeGate chan struct{}
}

func newWSPeer(conn *websocket.Conn) *wsPeer {
	return &wsPeer{conn: conn}
}

func (p *wsPeer) Write(ctx context.Context, payload any) error {
	p.writeOnce.Do(func() { p.writeGate = make(chan struct{}, 1) })
	select {
	case p.writeGate <- struct{}{}:
		defer func() { <-p.writeGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return wsjson.Write(ctx, p.conn, payload)
}

func (p *wsPeer) Close(status websocket.StatusCode, reason string) error {
	return p.conn.Close(status, reason)
}
