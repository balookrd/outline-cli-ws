package internal

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type relayResult struct {
	dir   string
	bytes int64
	err   error
}

func ProxyTCPOverOutlineWS(ctx context.Context, flowID uint64, client net.Conn, wsc WSConn, up UpstreamConfig, dst string) error {
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
	errC := make(chan relayResult, 2)

	wsDebugf("tcp relay start flow=%d upstream=%q dst=%q", flowID, up.Name, dst)
	go func() {
		n, e := io.Copy(ssconn, client)
		errC <- relayResult{dir: "client->upstream", bytes: n, err: e}
	}()
	go func() {
		n, e := io.Copy(client, ssconn)
		errC <- relayResult{dir: "upstream->client", bytes: n, err: e}
	}()

	r1 := <-errC
	wsDebugf("tcp relay side done flow=%d upstream=%q dst=%q dir=%s bytes=%d err=%v", flowID, up.Name, dst, r1.dir, r1.bytes, r1.err)
	e1 := r1.err
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

	var r2 relayResult
	var e2 error
	select {
	case r2 = <-errC:
		wsDebugf("tcp relay side done flow=%d upstream=%q dst=%q dir=%s bytes=%d err=%v", flowID, up.Name, dst, r2.dir, r2.bytes, r2.err)
		e2 = r2.err
	case <-ctx.Done():
		_ = ssconn.Close()
		_ = client.Close()
		if e1 != nil && !errors.Is(e1, io.EOF) {
			return e1
		}
		return ctx.Err()
	}

	if e1 != nil && !errors.Is(e1, io.EOF) {
		wsDebugf("tcp relay returning hard error flow=%d upstream=%q dst=%q dir=%s err=%v", flowID, up.Name, dst, r1.dir, e1)
		return e1
	}
	if e2 != nil && !errors.Is(e2, io.EOF) {
		wsDebugf("tcp relay returning hard error flow=%d upstream=%q dst=%q dir=%s err=%v", flowID, up.Name, dst, r2.dir, e2)
		return e2
	}
	wsDebugf("tcp relay completed cleanly flow=%d upstream=%q dst=%q first_dir=%s first_err=%v second_dir=%s second_err=%v", flowID, up.Name, dst, r1.dir, e1, r2.dir, e2)
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
		wsDebugPayload("in", w.upstream, w.proto, data)
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
	wsDebugPayload("out", w.upstream, w.proto, p)
	return len(p), nil
}

func (w *WSStreamConn) Close() error {
	// Close the underlying WebSocket stream so the server can free resources.
	// Previously this was a no-op which led to leaked streams and flaky TLS.
	w.closeOnce.Do(func() {
		wsDebugf("ws stream close requested upstream=%q proto=%q", w.upstream, w.proto)
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
