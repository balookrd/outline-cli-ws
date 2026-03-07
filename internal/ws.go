package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// DialWSStream dials a websocket endpoint.
//
// It supports three handshakes:
//  1. Classic HTTP/1.1 upgrade (always available)
//  2. WebSocket over HTTP/2 (RFC 8441, Extended CONNECT) when enabled
//  3. WebSocket over HTTP/3 (RFC 9220, Extended CONNECT) when enabled.
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
	uDial := stripHealthcheckQueryParams(u)

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

	tryH2, h2Only, tryH3, h3Only, connectOnly := parseTransportHints(u.Query())
	wsDebugf("dial start url=%q scheme=%q hints: tryH2=%v h2Only=%v tryH3=%v h3Only=%v connectOnly=%v", uDial.Redacted(), u.Scheme, tryH2, h2Only, tryH3, h3Only, connectOnly)

	if tryH3 && isWebSocketLikeScheme(u.Scheme) {
		wsDebugf("attempt h3/rfc9220 dial url=%q", uDial.Redacted())
		h3c, h3err := dialRFC9220(ctx, uDial)
		if h3err == nil {
			wsDebugf("h3/rfc9220 dial succeeded url=%q", uDial.Redacted())
			observeDial(upstream, proto, time.Since(start))
			return h3c, nil
		}
		wsDebugf("h3/rfc9220 dial failed url=%q err=%v", uDial.Redacted(), h3err)
		if h3Only {
			return nil, fmt.Errorf("h3-only connect failed: %w", h3err)
		}
		wsDebugf("fallback to h2/http1 after h3 failure url=%q", uDial.Redacted())
		if !tryH2 {
			tryH2 = true
		}
	} else if h3Only {
		return nil, fmt.Errorf("h3-only mode requires ws/wss URL, got scheme=%q", u.Scheme)
	}

	if h2Only {
		if !isWebSocketLikeScheme(u.Scheme) {
			return nil, fmt.Errorf("h2-only mode requires ws/wss URL, got scheme=%q", u.Scheme)
		}
		wsDebugf("attempt h2/rfc8441 dial url=%q", uDial.Redacted())
		h2c, h2err := dialRFC8441(ctx, uDial, tr)
		if h2err == nil {
			wsDebugf("h2/rfc8441 dial succeeded url=%q", uDial.Redacted())
			observeDial(upstream, proto, time.Since(start))
			return h2c, nil
		}
		wsDebugf("h2-only dial failed url=%q err=%v", uDial.Redacted(), h2err)
		return nil, fmt.Errorf("h2-only connect failed: %w", h2err)
	}

	if tryH2 && isWebSocketLikeScheme(u.Scheme) {
		wsDebugf("attempt h2/rfc8441 dial url=%q", uDial.Redacted())
		h2c, h2err := dialRFC8441(ctx, uDial, tr)
		if h2err == nil {
			wsDebugf("h2/rfc8441 dial succeeded url=%q", uDial.Redacted())
			observeDial(upstream, proto, time.Since(start))
			return h2c, nil
		}
		wsDebugf("h2/rfc8441 dial failed url=%q err=%v", uDial.Redacted(), h2err)
		// Only fall back on "not supported" style errors; otherwise surface.
		if !errors.Is(h2err, errRFC8441NotSupported) {
			return nil, h2err
		}
		// else: fall back to classic websocket.
	}

	if connectOnly {
		return nil, fmt.Errorf("extended-connect-only mode blocks h1 websocket upgrade fallback")
	}

	// Classic websocket (HTTP/1.1 upgrade).
	wsDebugf("attempt h1 websocket upgrade url=%q", uDial.Redacted())
	c, err := dialCoderWebSocket(ctx, uDial.String(), tr)
	if err != nil {
		wsDebugf("h1 websocket upgrade failed url=%q err=%v", uDial.Redacted(), err)
		return nil, err
	}
	wsDebugf("h1 websocket upgrade succeeded url=%q", uDial.Redacted())
	observeDial(upstream, proto, time.Since(start))
	return c, nil
}

func parseTransportHints(q url.Values) (tryH2, h2Only, tryH3, h3Only, connectOnly bool) {
	tryH2 = q.Get("h2") == "1" || q.Get("http2") == "1" || q.Get("h2c") == "1" || q.Get("rfc8441") == "1"
	h2Only = q.Get("h2") == "only" || q.Get("http2") == "only" || q.Get("h2only") == "1" || q.Get("rfc8441") == "only"
	tryH3 = q.Get("h3") == "1" || q.Get("http3") == "1" || q.Get("quic") == "1" || q.Get("rfc9220") == "1"
	h3Only = q.Get("h3") == "only" || q.Get("http3") == "only" || q.Get("h3only") == "1" || q.Get("quic") == "only" || q.Get("rfc9220") == "only"
	connectOnly = q.Get("connect") == "only" || q.Get("extended_connect") == "only" || q.Get("extended-connect") == "only" || q.Get("connect_protocol") == "only"
	if h3Only {
		tryH3 = true
	}
	if h2Only {
		tryH2 = true
	}
	if h2Only || h3Only {
		connectOnly = true
	}
	return
}

func stripHealthcheckQueryParams(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	clone := *u
	q := clone.Query()
	q.Del("hc_path")
	q.Del("health_path")
	q.Del("test_path")
	clone.RawQuery = q.Encode()
	return &clone
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
	elapsed := time.Since(start)
	wsDebugf("probe: transport established url=%q elapsed=%s, closing intentionally", rawurl, elapsed)
	_ = c.Close(WSStatusNormalClosure, "probe")
	wsDebugf("probe: close sent url=%q", rawurl)
	return elapsed, nil
}
