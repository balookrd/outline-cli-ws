package internal

import (
	"context"
	"net"

	"nhooyr.io/websocket"
)

// conn that closes websocket when Close() called
// (useful for per-flow connections in TUN mode)
type wsBoundConn struct {
	net.Conn
	wsc *websocket.Conn
}

func (c *wsBoundConn) Close() error {
	_ = c.Conn.Close()
	if c.wsc != nil {
		_ = c.wsc.Close(websocket.StatusNormalClosure, "close")
	}
	return nil
}

// DialOutlineTCP dials dst (host:port) through Outline TCP-over-WS (websocket-stream).
// Returns a net.Conn ready for io.Copy.
func DialOutlineTCP(ctx context.Context, lb *LoadBalancer, up *UpstreamState, dst string) (net.Conn, error) {
	wsc, err := lb.AcquireTCPWS(ctx, up)
	if err != nil {
		return nil, err
	}

	ss, err := newSSTCPConn(ctx, wsc, up.cfg, dst)
	if err != nil {
		_ = wsc.Close(websocket.StatusNormalClosure, "dial-error")
		return nil, err
	}

	return &wsBoundConn{Conn: ss, wsc: wsc}, nil
}
