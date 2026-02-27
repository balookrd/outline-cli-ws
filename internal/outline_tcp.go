package internal

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

func ProxyTCPOverOutlineWS(ctx context.Context, client net.Conn, wsc WSConn, up UpstreamConfig, dst string) error {
	ssconn, err := newSSTCPConn(ctx, wsc, up, dst)
	if err != nil {
		return err
	}
	defer ssconn.Close()

	// Bi-directional copy.
	// We wait for *both* directions to finish, but we also MUST actively
	// propagate half-close/close signals, otherwise one io.Copy may block forever
	// (typical when the client aborts: client->server stops, server->client keeps
	// waiting).
	errC := make(chan error, 2)

	go func() {
		_, e := io.Copy(ssconn, client)
		// Client->server finished: tell upstream we won't send more.
		_ = closeWrite(ssconn)
		errC <- e
	}()
	go func() {
		_, e := io.Copy(client, ssconn)
		// Server->client finished: tell client we won't send more.
		_ = closeWrite(client)
		errC <- e
	}()

	firstErr := <-errC

	// Ensure both sides are unblocked.
	// For WebSocket streams, CloseWrite == Close.
	_ = ssconn.Close()
	_ = client.Close()

	// If the caller already canceled, don't wait.
	select {
	case <-ctx.Done():
		return firstErr
	default:
	}

	secondErr := <-errC
	if firstErr != nil {
		return firstErr
	}
	return secondErr
}

// closeWrite attempts a TCP half-close if supported, otherwise falls back to Close.
func closeWrite(c net.Conn) error {
	// net.TCPConn has CloseWrite.
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// ---- WS stream as net.Conn ----

type WSStreamConn struct {
	ctx    context.Context
	cancel context.CancelFunc

	c  WSConn
	rb []byte

	closeOnce sync.Once
}

func NewWSStreamConn(ctx context.Context, c WSConn) *WSStreamConn {
	ctx2, cancel := context.WithCancel(ctx)
	return &WSStreamConn{ctx: ctx2, cancel: cancel, c: c}
}

func (w *WSStreamConn) Read(p []byte) (int, error) {
	for len(w.rb) == 0 {
		typ, data, err := w.c.Read(w.ctx)
		if err != nil {
			return 0, err
		}
		if typ != WSMessageBinary {
			continue
		}
		w.rb = data
	}
	n := copy(p, w.rb)
	w.rb = w.rb[n:]
	return n, nil
}

func (w *WSStreamConn) Write(p []byte) (int, error) {
	if err := w.c.Write(w.ctx, WSMessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *WSStreamConn) Close() error {
	// Close the underlying WebSocket stream so the server can free resources.
	// Previously this was a no-op which led to leaked streams and flaky TLS.
	w.closeOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		_ = w.c.Close(1000, "")
	})
	return nil
}

// CloseWrite is a best-effort half-close signal.
// WebSocket doesn't support true TCP half-close, so we translate it to a normal
// close handshake.
func (w *WSStreamConn) CloseWrite() error { return w.Close() }

func (w *WSStreamConn) LocalAddr() net.Addr  { return dummyTCPAddr("local") }
func (w *WSStreamConn) RemoteAddr() net.Addr { return dummyTCPAddr("remote") }

func (w *WSStreamConn) SetDeadline(time.Time) error      { return nil }
func (w *WSStreamConn) SetReadDeadline(time.Time) error  { return nil }
func (w *WSStreamConn) SetWriteDeadline(time.Time) error { return nil }

type dummyTCPAddr string

func (d dummyTCPAddr) Network() string { return "tcp" }
func (d dummyTCPAddr) String() string  { return string(d) }
