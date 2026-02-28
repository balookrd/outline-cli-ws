package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// DialWSStream dials a websocket endpoint.
//
// It supports two handshakes:
//  1. Classic HTTP/1.1 upgrade (always available)
//  2. WebSocket over HTTP/2 (RFC 8441, Extended CONNECT) when enabled.
//
// HTTP/2 Extended CONNECT support in Go has historically been gated behind
// a GODEBUG flag in some toolchain versions (see golang/go#53208 referenced by
// various examples). If the runtime / stdlib does not support it, we fall back
// to the classic HTTP/1.1 websocket handshake.
//
// RFC 8441 requires:
//
//	:method = CONNECT
//	:protocol = websocket
//	:scheme, :path, :authority present
//
// and forbids the HTTP/1 Connection/Upgrade headers.
// See RFC 8441 Sections 4–5.
func DialWSStream(ctx context.Context, rawurl string, fwmark uint32) (WSConn, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}

	// Shared dialer with fwmark support.
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var ctrlErr error
			if err := c.Control(func(fd uintptr) {
				ctrlErr = setSocketMark(fd, fwmark)
			}); err != nil {
				return err
			}
			return ctrlErr
		},
	}

	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       d.DialContext,
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	// If the caller provided an explicit hint via query (?h2=1 / ?http2=1),
	// try RFC 8441 first, then fall back.
	q := u.Query()
	tryH2 := q.Get("h2") == "1" || q.Get("http2") == "1" || q.Get("h2c") == "1"
	// Explicit mode: HTTP/2 only (no fallback to classic HTTP/1.1 Upgrade).
	// Supported spellings:
	//   ?h2=only
	//   ?http2=only
	//   ?h2only=1
	h2Only := q.Get("h2") == "only" || q.Get("http2") == "only" || q.Get("h2only") == "1"

	if h2Only {
		if !(u.Scheme == "wss" || u.Scheme == "ws") {
			return nil, fmt.Errorf("h2-only mode requires ws/wss URL, got scheme=%q", u.Scheme)
		}
		h2c, h2err := dialRFC8441(ctx, u, tr)
		if h2err == nil {
			return h2c, nil
		}
		return nil, fmt.Errorf("h2-only connect failed: %w", h2err)
	}

	if tryH2 && (u.Scheme == "wss" || u.Scheme == "ws") {
		h2c, h2err := dialRFC8441(ctx, u, tr)
		if h2err == nil {
			return h2c, nil
		}
		// Only fall back on "not supported" style errors; otherwise surface.
		if !errors.Is(h2err, errRFC8441NotSupported) {
			return nil, h2err
		}
		// else: fall back to classic websocket.
	}

	// Classic websocket (HTTP/1.1 upgrade).
	return dialCoderWebSocket(ctx, u.String(), tr)
}

// ProbeWSS verifies the websocket handshake succeeds.
func ProbeWSS(ctx context.Context, rawurl string, fwmark uint32) (time.Duration, error) {
	start := time.Now()
	c, err := DialWSStream(ctx, rawurl, fwmark)
	if err != nil {
		return 0, err
	}
	_ = c.Close(WSStatusNormalClosure, "probe")
	return time.Since(start), nil
}
