package internal

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"nhooyr.io/websocket"
)

func ProxyTCPOverOutlineWS(ctx context.Context, client net.Conn, wsc *websocket.Conn, up UpstreamConfig, dst string) error {
	// WS as net.Conn (stream-ish)
	wsconn := NewWSStreamConn(ctx, wsc)

	// Shadowsocks AEAD cipher
	ciph, err := core.PickCipher(up.Cipher, nil, up.Secret)
	if err != nil {
		return err
	}

	// Shadowsocks stream over WS
	ssconn := ciph.StreamConn(wsconn)
	defer ssconn.Close()

	// Send SS TCP request header (target address)
	tgt := socks.ParseAddr(dst) // NOTE: returns 1 value in your version
	if tgt == nil {
		return socks.ErrAddressNotSupported
	}
	if _, err := ssconn.Write(tgt); err != nil {
		return err
	}

	// Bi-directional copy
	errc := make(chan error, 2)
	go func() {
		_, e := io.Copy(ssconn, client)
		errc <- e
	}()
	go func() {
		_, e := io.Copy(client, ssconn)
		errc <- e
	}()
	return <-errc
}

// ---- WS stream as net.Conn ----

type WSStreamConn struct {
	ctx context.Context
	c   *websocket.Conn
	rb  []byte
}

func NewWSStreamConn(ctx context.Context, c *websocket.Conn) *WSStreamConn {
	return &WSStreamConn{ctx: ctx, c: c}
}

func (w *WSStreamConn) Read(p []byte) (int, error) {
	for len(w.rb) == 0 {
		typ, data, err := w.c.Read(w.ctx)
		if err != nil {
			return 0, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		w.rb = data
	}
	n := copy(p, w.rb)
	w.rb = w.rb[n:]
	return n, nil
}

func (w *WSStreamConn) Write(p []byte) (int, error) {
	if err := w.c.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *WSStreamConn) Close() error { return nil } // ws закрывается снаружи

func (w *WSStreamConn) LocalAddr() net.Addr  { return dummyTCPAddr("local") }
func (w *WSStreamConn) RemoteAddr() net.Addr { return dummyTCPAddr("remote") }

func (w *WSStreamConn) SetDeadline(t time.Time) error      { return nil }
func (w *WSStreamConn) SetReadDeadline(t time.Time) error  { return nil }
func (w *WSStreamConn) SetWriteDeadline(t time.Time) error { return nil }

type dummyTCPAddr string

func (d dummyTCPAddr) Network() string { return "tcp" }
func (d dummyTCPAddr) String() string  { return string(d) }
