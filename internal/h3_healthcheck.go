//go:build !unit

package internal

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/quic"
)

const h3HealthcheckPeerSettingsWait = 1500 * time.Millisecond

// ProbeH3ExtendedConnect validates RFC9220 path in three stages:
// 1) QUIC handshake;
// 2) peer H3 control/SETTINGS;
// 3) extended CONNECT on a dedicated test path (if hc_path is provided).
//
// It intentionally avoids websocket CLOSE frame emission on the upstream data
// path by not wrapping the stream into WS framing and by closing the QUIC
// connection directly after response validation.
func ProbeH3ExtendedConnect(ctx context.Context, rawurl string) (time.Duration, error) {
	start := time.Now()
	u, err := url.Parse(rawurl)
	if err != nil {
		return 0, err
	}
	if u.Scheme != "wss" && u.Scheme != "https" {
		return 0, fmt.Errorf("h3 healthcheck requires wss/https, got %q", u.Scheme)
	}

	hcURL := h3HealthcheckURL(u)
	host := hcURL.Hostname()
	if host == "" {
		host, _ = splitHostPortDefault(hcURL.Host, "443")
	}
	port := hcURL.Port()
	if port == "" {
		port = "443"
	}
	authority := hcURL.Host
	if authority == "" {
		authority = host
	}

	tlsConf := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host, NextProtos: []string{"h3"}}
	qcConf := &quic.Config{TLSConfig: tlsConf}
	ep, err := quic.Listen("udp", ":0", qcConf)
	if err != nil {
		return 0, fmt.Errorf("h3 healthcheck: quic endpoint init failed: %w", err)
	}
	defer ep.Close(context.Background())

	dialAddr := net.JoinHostPort(host, port)
	qconn, err := ep.Dial(ctx, "udp", dialAddr, qcConf)
	if err != nil {
		return 0, fmt.Errorf("h3 healthcheck: quic handshake failed: %w", err)
	}
	defer qconn.Close()

	obs := newH3PeerObservations()
	peerDrainCancel := startH3PeerStreamDrainer(qconn, obs)
	defer peerDrainCancel()

	if err := h3OpenClientUniStreams(ctx, qconn, h3ClientStreamsControlAndQPACK); err != nil {
		return 0, fmt.Errorf("h3 healthcheck: open client control streams failed: %w", err)
	}

	wait := h3HealthcheckPeerSettingsWait
	if ddl, ok := ctx.Deadline(); ok {
		left := time.Until(ddl)
		if left > 0 && left < wait {
			wait = left
		}
	}
	obs.waitSettings(wait)
	sawSettings, enableConnect := obs.snapshot()
	if !sawSettings {
		return 0, fmt.Errorf("h3 healthcheck: peer H3 control stream SETTINGS not observed")
	}
	if !enableConnect {
		return 0, fmt.Errorf("h3 healthcheck: peer SETTINGS_ENABLE_CONNECT_PROTOCOL is disabled")
	}

	st, err := qconn.NewStream(ctx)
	if err != nil {
		return 0, fmt.Errorf("h3 healthcheck: open request stream failed: %w", err)
	}

	headers := h3ConnectHeaders(hcURL, authority)
	requestFrame := appendVarint(nil, h3FrameHeaders)
	requestFrame = appendVarint(requestFrame, uint64(len(headers)))
	requestFrame = append(requestFrame, headers...)
	if err := h3WriteWithContext(ctx, st, requestFrame); err != nil {
		return 0, fmt.Errorf("h3 healthcheck: send CONNECT HEADERS failed: %w", err)
	}
	_ = st.Flush()

	resp, err := h3ReadResponseHeaders(st)
	if err != nil {
		return 0, fmt.Errorf("h3 healthcheck: read CONNECT response failed: %w", err)
	}
	if status := resp[":status"]; status != "200" {
		return 0, fmt.Errorf("h3 healthcheck: extended CONNECT test-path failed status=%s", status)
	}
	_ = st.Close()

	return time.Since(start), nil
}

func h3HealthcheckURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	clone := *u
	q := clone.Query()

	hcPath := strings.TrimSpace(q.Get("hc_path"))
	if hcPath == "" {
		hcPath = strings.TrimSpace(q.Get("health_path"))
	}
	if hcPath == "" {
		hcPath = strings.TrimSpace(q.Get("test_path"))
	}
	q.Del("hc_path")
	q.Del("health_path")
	q.Del("test_path")
	clone.RawQuery = q.Encode()

	if hcPath != "" {
		if !strings.HasPrefix(hcPath, "/") {
			hcPath = "/" + hcPath
		}
		clone.Path = hcPath
		clone.RawPath = ""
	}
	return &clone
}
