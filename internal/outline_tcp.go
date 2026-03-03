package internal

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const tcpRelayDrainTimeout = 3 * time.Second

func ProxyTCPOverOutlineWS(ctx context.Context, client net.Conn, wsc WSConn, up UpstreamConfig, dst string) error {
	ssconn, err := newSSTCPConn(ctx, wsc, up, dst)
	if err != nil {
		return err
	}
	defer ssconn.Close()

	// Full-duplex relay.
	//
	// Important: do not proactively CloseWrite() on one side when the opposite
	// io.Copy finishes. For WS-backed streams this can translate to full close and
	// break remaining reverse-direction traffic.
	errC := make(chan error, 2)

	go func() {
		_, e := io.Copy(ssconn, client)
		errC <- e
	}()
	go func() {
		_, e := io.Copy(client, ssconn)
		errC <- e
	}()

	e1 := <-errC
	if e1 != nil && !errors.Is(e1, io.EOF) {
		// Hard error: force teardown so the other copy unblocks.
		_ = ssconn.Close()
		_ = client.Close()
	}

	// If caller canceled, return early (cleanup via deferred closes).
	select {
	case <-ctx.Done():
		if e1 != nil && !errors.Is(e1, io.EOF) {
			return e1
		}
		return ctx.Err()
	default:
	}

	var e2 error
	select {
	case e2 = <-errC:
	case <-time.After(tcpRelayDrainTimeout):
		// Keep full-duplex behavior, but avoid leaking half-dead tunnels forever.
		_ = ssconn.Close()
		_ = client.Close()
		if e1 != nil && !errors.Is(e1, io.EOF) {
			return e1
		}
		return nil
	}

	if e1 != nil && !errors.Is(e1, io.EOF) {
		return e1
	}
	if e2 != nil && !errors.Is(e2, io.EOF) {
		return e2
	}
	return nil
}

// ---- WS stream as net.Conn ----

type WSStreamConn struct {
	ctx    context.Context
	cancel context.CancelFunc

	c        WSConn
	rb       []byte
	upstream string
	proto    string

	closeOnce sync.Once
}

func NewWSStreamConn(ctx context.Context, c WSConn, upstream, proto string) *WSStreamConn {
	ctx2, cancel := context.WithCancel(ctx)
	return &WSStreamConn{ctx: ctx2, cancel: cancel, c: c, upstream: upstream, proto: proto}
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
		observeWSFrame("in", len(data))
		observeUpstreamTraffic(w.upstream, w.proto, "in", len(data))
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
	observeWSFrame("out", len(p))
	observeUpstreamTraffic(w.upstream, w.proto, "out", len(p))
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

func (w *WSStreamConn) LocalAddr() net.Addr  { return dummyTCPAddr("local") }
func (w *WSStreamConn) RemoteAddr() net.Addr { return dummyTCPAddr("remote") }

func (w *WSStreamConn) SetDeadline(time.Time) error      { return nil }
func (w *WSStreamConn) SetReadDeadline(time.Time) error  { return nil }
func (w *WSStreamConn) SetWriteDeadline(time.Time) error { return nil }

type dummyTCPAddr string

func (d dummyTCPAddr) Network() string { return "tcp" }
func (d dummyTCPAddr) String() string  { return string(d) }
