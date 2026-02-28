//go:build !unit

package internal

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/quic"
)

var errRFC9220NotSupported = fmt.Errorf("rfc9220 not supported")

const (
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
	host, port := splitHostPortDefault(u.Host, "443")
	authority := net.JoinHostPort(host, port)
	if strings.Contains(u.Host, ":") && strings.HasPrefix(u.Host, "[") {
		authority = u.Host
	}

	tlsConf := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host, NextProtos: []string{"h3"}}
	qcConf := &quic.Config{TLSConfig: tlsConf}
	ep, err := quic.Listen("udp", ":0", qcConf)
	if err != nil {
		return nil, err
	}
	qconn, err := ep.Dial(ctx, "udp", authority, qcConf)
	if err != nil {
		_ = ep.Close(context.Background())
		return nil, err
	}

	if err := h3SendClientSettings(ctx, qconn); err != nil {
		_ = qconn.Close()
		_ = ep.Close(context.Background())
		return nil, err
	}

	st, err := qconn.NewStream(ctx)
	if err != nil {
		_ = qconn.Close()
		_ = ep.Close(context.Background())
		return nil, err
	}

	key, accept, err := h3WebSocketKeyAccept()
	if err != nil {
		return nil, err
	}
	headers := h3EncodeHeaders([][2]string{{":method", "CONNECT"}, {":scheme", "https"}, {":authority", authority}, {":path", cleanedRequestURI(u)}, {":protocol", "websocket"}, {"sec-websocket-version", "13"}, {"sec-websocket-key", key}})
	if origin := u.Query().Get("origin"); origin != "" {
		headers = h3EncodeHeaders([][2]string{{":method", "CONNECT"}, {":scheme", "https"}, {":authority", authority}, {":path", cleanedRequestURI(u)}, {":protocol", "websocket"}, {"sec-websocket-version", "13"}, {"sec-websocket-key", key}, {"origin", origin}})
	}
	if _, err := st.Write(appendVarint(nil, h3FrameHeaders)); err != nil {
		return nil, err
	}
	if _, err := st.Write(appendVarint(nil, uint64(len(headers)))); err != nil {
		return nil, err
	}
	if _, err := st.Write(headers); err != nil {
		return nil, err
	}

	resp, err := h3ReadResponseHeaders(st)
	if err != nil {
		return nil, err
	}
	if resp[":status"] != "200" {
		return nil, fmt.Errorf("rfc9220 connect failed: status=%s", resp[":status"])
	}
	if got := resp["sec-websocket-accept"]; got != "" && got != accept {
		return nil, fmt.Errorf("rfc9220 bad sec-websocket-accept")
	}
	return newFramedWSConn(&h3wsStream{s: st, qc: qconn, ep: ep}), nil
}

func h3SendClientSettings(ctx context.Context, c *quic.Conn) error {
	st, err := c.NewSendOnlyStream(ctx)
	if err != nil {
		return err
	}
	payload := appendVarint(nil, h3SettingEnableConnectProtocol)
	payload = appendVarint(payload, 1)
	if _, err := st.Write(appendVarint(nil, h3StreamControl)); err != nil {
		return err
	}
	if _, err := st.Write(appendVarint(nil, h3FrameSettings)); err != nil {
		return err
	}
	if _, err := st.Write(appendVarint(nil, uint64(len(payload)))); err != nil {
		return err
	}
	if _, err := st.Write(payload); err != nil {
		return err
	}
	st.CloseWrite()
	return nil
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
