//go:build !unit

package internal

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/quic"
)

const (
	h3HandshakeTimeout             = 12 * time.Second
	h3FrameData                    = 0x0
	h3FrameHeaders                 = 0x1
	h3FrameSettings                = 0x4
	h3StreamControl                = 0x0
	h3SettingEnableConnectProtocol = 0x08
)

type h3wsStream struct {
	s  *quic.Stream
	qc *quic.Conn
	ep *quic.Endpoint
}

func (s *h3wsStream) Read(p []byte) (int, error)  { return s.s.Read(p) }
func (s *h3wsStream) Write(p []byte) (int, error) { return s.s.Write(p) }
func (s *h3wsStream) Close() error {
	s.s.CloseRead()
	s.s.CloseWrite()
	_ = s.qc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = s.ep.Close(ctx)
	return nil
}

func dialRFC9220(ctx context.Context, u *url.URL) (WSConn, error) {
	if u.Scheme != "wss" && u.Scheme != "https" {
		return nil, fmt.Errorf("rfc9220 requires wss/https, got %q", u.Scheme)
	}
	h3ctx, h3cancel := context.WithTimeout(ctx, h3HandshakeTimeout)
	defer h3cancel()

	host := u.Hostname()
	if host == "" {
		host, _ = splitHostPortDefault(u.Host, "443")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	authority := net.JoinHostPort(host, port)
	if u.Host != "" {
		if _, p, err := net.SplitHostPort(u.Host); err == nil && p != "" {
			authority = u.Host
		}
	}
	dialAddr := net.JoinHostPort(host, port)
	wsDebugf("h3: prepare dial host=%q port=%q authority=%q dial_addr=%q url=%q", host, port, authority, dialAddr, u.Redacted())

	tlsConf := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host, NextProtos: []string{"h3"}}
	qcConf := &quic.Config{TLSConfig: tlsConf}
	ep, err := quic.Listen("udp", ":0", qcConf)
	if err != nil {
		wsDebugf("h3: quic listen failed err=%v", err)
		return nil, err
	}
	wsDebugf("h3: quic endpoint ready, dialing addr=%q", dialAddr)
	qconn, err := ep.Dial(h3ctx, "udp", dialAddr, qcConf)
	if err != nil {
		wsDebugf("h3: quic dial failed addr=%q err=%v", dialAddr, err)
		_ = ep.Close(context.Background())
		return nil, err
	}
	wsDebugf("h3: quic dial established addr=%q", dialAddr)

	if err := h3SendClientSettings(h3ctx, qconn); err != nil {
		wsDebugf("h3: send client settings failed err=%v", err)
		_ = qconn.Close()
		_ = ep.Close(context.Background())
		return nil, err
	}
	wsDebugf("h3: client settings sent")

	st, err := qconn.NewStream(h3ctx)
	if err != nil {
		wsDebugf("h3: open request stream failed err=%v", err)
		_ = qconn.Close()
		_ = ep.Close(context.Background())
		return nil, err
	}
	wsDebugf("h3: request stream opened")

	key, accept, err := h3WebSocketKeyAccept()
	if err != nil {
		return nil, err
	}
	headers := h3EncodeHeaders([][2]string{{":method", "CONNECT"}, {":scheme", "https"}, {":authority", authority}, {":path", cleanedRequestURI(u)}, {":protocol", "websocket"}, {"sec-websocket-version", "13"}, {"sec-websocket-key", key}})
	if origin := u.Query().Get("origin"); origin != "" {
		headers = h3EncodeHeaders([][2]string{{":method", "CONNECT"}, {":scheme", "https"}, {":authority", authority}, {":path", cleanedRequestURI(u)}, {":protocol", "websocket"}, {"sec-websocket-version", "13"}, {"sec-websocket-key", key}, {"origin", origin}})
	}
	wsDebugf("h3: writing HEADERS frame type")
	if err := h3WriteWithContext(h3ctx, st, appendVarint(nil, h3FrameHeaders)); err != nil {
		wsDebugf("h3: write frame type failed err=%v", err)
		return nil, err
	}
	wsDebugf("h3: HEADERS frame type written")
	wsDebugf("h3: writing HEADERS length=%d", len(headers))
	if err := h3WriteWithContext(h3ctx, st, appendVarint(nil, uint64(len(headers)))); err != nil {
		wsDebugf("h3: write headers length failed err=%v", err)
		return nil, err
	}
	wsDebugf("h3: writing HEADERS payload")
	if err := h3WriteWithContext(h3ctx, st, headers); err != nil {
		wsDebugf("h3: write headers payload failed err=%v", err)
		return nil, err
	}
	wsDebugf("h3: request headers sent, waiting response")

	respCh := make(chan map[string]string, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := h3ReadResponseHeaders(st)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var resp map[string]string
	select {
	case <-h3ctx.Done():
		_ = qconn.Close()
		_ = ep.Close(context.Background())
		if errors.Is(h3ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("rfc9220 handshake timeout waiting response headers")
		}
		return nil, h3ctx.Err()
	case err := <-errCh:
		wsDebugf("h3: read response headers failed err=%v", err)
		return nil, err
	case resp = <-respCh:
	}
	wsDebugf("h3: response status=%q headers=%s", resp[":status"], h3FormatHeaders(resp))
	if resp[":status"] != "200" {
		return nil, fmt.Errorf("rfc9220 connect failed: status=%s headers=%s", resp[":status"], h3FormatHeaders(resp))
	}
	if got := resp["sec-websocket-accept"]; got != "" && got != accept {
		wsDebugf("h3: bad sec-websocket-accept got=%q", got)
		return nil, fmt.Errorf("rfc9220 bad sec-websocket-accept")
	}
	wsDebugf("h3: websocket CONNECT established")
	return newFramedWSConn(&h3wsStream{s: st, qc: qconn, ep: ep}), nil
}

func h3SendClientSettings(ctx context.Context, c *quic.Conn) error {
	st, err := c.NewSendOnlyStream(ctx)
	if err != nil {
		return err
	}
	payload := appendVarint(nil, h3SettingEnableConnectProtocol)
	payload = appendVarint(payload, 1)
	if err := h3WriteWithContext(ctx, st, appendVarint(nil, h3StreamControl)); err != nil {
		return err
	}
	if err := h3WriteWithContext(ctx, st, appendVarint(nil, h3FrameSettings)); err != nil {
		return err
	}
	if err := h3WriteWithContext(ctx, st, appendVarint(nil, uint64(len(payload)))); err != nil {
		return err
	}
	if err := h3WriteWithContext(ctx, st, payload); err != nil {
		return err
	}
	st.CloseWrite()
	return nil
}

func h3WriteWithContext(ctx context.Context, st *quic.Stream, b []byte) error {
	errCh := make(chan error, 1)
	go func() {
		_, err := st.Write(b)
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		st.CloseWrite()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("h3 write timeout")
		}
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func h3ReadResponseHeaders(r io.Reader) (map[string]string, error) {
	for {
		ft, err := readVarint(r)
		if err != nil {
			return nil, err
		}
		n, err := readVarint(r)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		if ft == h3FrameHeaders {
			return h3DecodeHeaders(buf)
		}
	}
}

func h3FormatHeaders(h map[string]string) string {
	if len(h) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := h[k]
		if len(v) > 128 {
			v = v[:128] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func splitHostPortDefault(hostport, defPort string) (string, string) {
	h, p, err := net.SplitHostPort(hostport)
	if err == nil {
		return strings.Trim(h, "[]"), p
	}
	return hostport, defPort
}

func appendVarint(b []byte, v uint64) []byte {
	switch {
	case v <= 63:
		return append(b, byte(v))
	case v <= 16383:
		return append(b, byte((v>>8)|0x40), byte(v))
	case v <= 1073741823:
		return append(b, byte((v>>24)|0x80), byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(b, byte((v>>56)|0xC0), byte(v>>48), byte(v>>40), byte(v>>32), byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}

func readVarint(r io.Reader) (uint64, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return 0, err
	}
	prefix := first[0] >> 6
	n := 1 << prefix
	buf := make([]byte, n)
	buf[0] = first[0]
	if n > 1 {
		if _, err := io.ReadFull(r, buf[1:]); err != nil {
			return 0, err
		}
	}
	buf[0] &= 0x3f
	var v uint64
	for _, b := range buf {
		v = (v << 8) | uint64(b)
	}
	return v, nil
}

func h3WebSocketKeyAccept() (string, string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	key := base64.StdEncoding.EncodeToString(raw)
	return key, computeAccept(key), nil
}
