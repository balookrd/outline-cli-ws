package internal

import (
	"context"
	"net"
	"time"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"nhooyr.io/websocket"
)

// conn that closes websocket when Close() called
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
// Returns a normal net.Conn (stream) ready for io.Copy.
func DialOutlineTCP(ctx context.Context, lb *LoadBalancer, up *UpstreamState, dst string) (net.Conn, error) {
	// get (possibly warm) ws
	wsc, err := lb.AcquireTCPWS(ctx, up)
	if err != nil {
		return nil, err
	}

	// Wrap websocket into net.Conn-like stream
	wsconn := NewWSStreamConn(ctx, wsc)

	// Shadowsocks cipher
	ciph, err := core.PickCipher(up.cfg.Cipher, nil, up.cfg.Secret)
	if err != nil {
		_ = wsc.Close(websocket.StatusNormalClosure, "cipher-error")
		return nil, err
	}

	ss := ciph.StreamConn(wsconn)

	// Send target header once
	tgt := socks.ParseAddr(dst)
	if tgt == nil {
		_ = ss.Close()
		_ = wsc.Close(websocket.StatusNormalClosure, "bad-addr")
		return nil, socks.ErrAddressNotSupported
	}
	if _, err := ss.Write(tgt); err != nil {
		_ = ss.Close()
		_ = wsc.Close(websocket.StatusNormalClosure, "write-addr-fail")
		return nil, err
	}

	// optional: set deadlines if you want
	_ = ss.SetDeadline(time.Time{})

	return &wsBoundConn{Conn: ss, wsc: wsc}, nil
}
