package internal

import (
	"context"
	"io"
	"net"
	"time"

	"nhooyr.io/websocket"
)

func ProxyTCPOverOutlineWS(ctx context.Context, client net.Conn, wsc *websocket.Conn, up UpstreamConfig, dst string) error {
	ssconn, err := newSSTCPConn(ctx, wsc, up, dst)
	if err != nil {
		return err
	}
	defer ssconn.Close()

	// Bi-directional copy
	errC := make(chan error, 2)
	go func() {
		_, e := io.Copy(ssconn, client)
		errC <- e
	}()
	go func() {
		_, e := io.Copy(client, ssconn)
		errC <- e
	}()
	return <-errC
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

func (w *WSStreamConn) SetDeadline(time.Time) error      { return nil }
func (w *WSStreamConn) SetReadDeadline(time.Time) error  { return nil }
func (w *WSStreamConn) SetWriteDeadline(time.Time) error { return nil }

type dummyTCPAddr string

func (d dummyTCPAddr) Network() string { return "tcp" }
func (d dummyTCPAddr) String() string  { return string(d) }
