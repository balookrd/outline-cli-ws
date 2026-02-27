package internal

import (
	"context"
	"net"
	"time"

	"github.com/coder/websocket"
)

// WSPacketConn adapts WebSocket binary messages to net.PacketConn.
// One WS binary message = one datagram.
//
// It is intentionally minimal: deadlines are no-ops and the returned addr is dummy.
// The caller is responsible for owning/closing the underlying websocket.
//
// Note: ReadFrom will skip non-binary WS messages.
type WSPacketConn struct {
	ctx context.Context
	c   *websocket.Conn
}

func NewWSPacketConn(ctx context.Context, c *websocket.Conn) *WSPacketConn {
	return &WSPacketConn{ctx: ctx, c: c}
}

func (w *WSPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		typ, data, err := w.c.Read(w.ctx)
		if err != nil {
			return 0, nil, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		n := copy(p, data)
		return n, dummyAddr{}, nil
	}
}

func (w *WSPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if err := w.c.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *WSPacketConn) Close() error {
	return w.c.Close(websocket.StatusNormalClosure, "close")
}

func (w *WSPacketConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (w *WSPacketConn) SetDeadline(time.Time) error      { return nil }
func (w *WSPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (w *WSPacketConn) SetWriteDeadline(time.Time) error { return nil }
