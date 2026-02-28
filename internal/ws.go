package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
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
	start := time.Now()
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	upstream, proto := upstreamFromURL(u)

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

	tryH2, h2Only, tryH3, h3Only := parseTransportHints(u.Query())

	if h3Only {
		log.Printf("[WS] upstream %q requested h3-only mode; falling back to h2/http1 because native h3 client transport is unavailable in this build", u.Redacted())
	}
	if tryH3 {
		log.Printf("[WS] upstream %q requested ws-over-quic mode; trying h2/http1 compatibility dial path", u.Redacted())
		if !tryH2 {
			tryH2 = true
		}
	}

	if h2Only {
		if !isWebSocketLikeScheme(u.Scheme) {
			return nil, fmt.Errorf("h2-only mode requires ws/wss URL, got scheme=%q", u.Scheme)
		}
		h2c, h2err := dialRFC8441(ctx, u, tr)
		if h2err == nil {
			observeDial(upstream, proto, time.Since(start))
			return h2c, nil
		}
		return nil, fmt.Errorf("h2-only connect failed: %w", h2err)
	}

	if tryH2 && isWebSocketLikeScheme(u.Scheme) {
		h2c, h2err := dialRFC8441(ctx, u, tr)
		if h2err == nil {
			observeDial(upstream, proto, time.Since(start))
			return h2c, nil
		}
		// Only fall back on "not supported" style errors; otherwise surface.
		if !errors.Is(h2err, errRFC8441NotSupported) {
			return nil, h2err
		}
		// else: fall back to classic websocket.
	}

	// Classic websocket (HTTP/1.1 upgrade).
	c, err := dialCoderWebSocket(ctx, u.String(), tr)
	if err != nil {
		return nil, err
	}
	observeDial(upstream, proto, time.Since(start))
	return c, nil
}

func parseTransportHints(q url.Values) (tryH2, h2Only, tryH3, h3Only bool) {
	tryH2 = q.Get("h2") == "1" || q.Get("http2") == "1" || q.Get("h2c") == "1"
	h2Only = q.Get("h2") == "only" || q.Get("http2") == "only" || q.Get("h2only") == "1"
	tryH3 = q.Get("h3") == "1" || q.Get("http3") == "1" || q.Get("quic") == "1"
	h3Only = q.Get("h3") == "only" || q.Get("http3") == "only" || q.Get("h3only") == "1" || q.Get("quic") == "only"
	return
}

func isWebSocketLikeScheme(s string) bool {
	s = strings.ToLower(s)
	return s == "ws" || s == "wss" || s == "http" || s == "https"
}

func upstreamFromURL(u *url.URL) (name, proto string) {
	if u == nil {
		return "unknown", "tcp"
	}
	name = u.Host
	if name == "" {
		name = "unknown"
	}
	if strings.Contains(strings.ToLower(u.Path), "udp") {
		return name, "udp"
	}
	return name, "tcp"
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
