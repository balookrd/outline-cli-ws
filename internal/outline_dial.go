package internal

import (
	"context"
	"net"
)

// conn that closes websocket when Close() called
// (useful for per-flow connections in TUN mode)
type wsBoundConn struct {
	net.Conn
	wsc WSConn
}

func (c *wsBoundConn) Close() error {
	_ = c.Conn.Close()
	if c.wsc != nil {
		_ = c.wsc.Close(WSStatusNormalClosure, "close")
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
		_ = wsc.Close(WSStatusNormalClosure, "dial-error")
		return nil, err
	}

	return &wsBoundConn{Conn: ss, wsc: wsc}, nil
}
