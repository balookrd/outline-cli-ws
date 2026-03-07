//go:build !unit

package internal

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const (
	rawH2MaxDataFrameChunk = 16 * 1024
	rawH2WindowUpdateBatch = 64 * 1024
)

var errRFC8441HandshakeFailed = errors.New("rfc8441 handshake failed")

// dialRFC8441RawH2 speaks RFC 8441 (Extended CONNECT) directly over HTTP/2.
//
// Why: some Go toolchains don't expose a public net/http API to set the
// ":protocol" pseudo-header, so net/http cannot send RFC 8441 requests.
//
// Notes:
//   - TLS only (wss). h2c is not supported here.
//   - One HTTP/2 connection per WS connection.
func dialRFC8441RawH2(ctx context.Context, u *url.URL, tr *http.Transport) (WSConn, error) {
	if u.Scheme != "wss" {
		return nil, fmt.Errorf("rfc8441 raw h2 requires wss, got %q", u.Scheme)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}

	// Dial TCP using the same dialer (fwmark/proxy settings already applied).
	dialCtx := tr.DialContext
	if dialCtx == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dialCtx = dialer.DialContext
	}

	wsDebugf("h2raw: dial tcp host=%q sni=%q path=%q", host, u.Hostname(), u.Path)
	tcpConn, err := dialCtx(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}

	// TLS handshake with ALPN h2.
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if tr.TLSClientConfig != nil {
		tlsConf = tr.TLSClientConfig.Clone()
	}
	if tlsConf.ServerName == "" {
		// Use hostname without port.
		if h, _, e := net.SplitHostPort(host); e == nil {
			tlsConf.ServerName = h
		} else {
			tlsConf.ServerName = host
		}
	}
	// Ensure ALPN includes h2.
	if len(tlsConf.NextProtos) == 0 {
		tlsConf.NextProtos = []string{"h2", "http/1.1"}
	} else {
		hasH2 := false
		for _, p := range tlsConf.NextProtos {
			if p == "h2" {
				hasH2 = true
				break
			}
		}
		if !hasH2 {
			tlsConf.NextProtos = append([]string{"h2"}, tlsConf.NextProtos...)
		}
	}

	tlsConn := tls.Client(tcpConn, tlsConf)
	wsDebugf("h2raw: tls handshake start servername=%q", tlsConf.ServerName)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	wsDebugf("h2raw: tls handshake done negotiated_alpn=%q", tlsConn.ConnectionState().NegotiatedProtocol)
	if tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("rfc8441 requires h2 ALPN, negotiated %q", tlsConn.ConnectionState().NegotiatedProtocol)
	}

	cc := newRawH2Conn(tlsConn)
	wsDebugf("h2raw: init connection")
	if err := cc.init(ctx); err != nil {
		_ = cc.Close()
		return nil, err
	}

	wsDebugf("h2raw: open websocket stream")
	ws, err := cc.openWebSocketStream(ctx, u)
	if err != nil {
		_ = cc.Close()
		return nil, err
	}
	return ws, nil
}

// ---- raw HTTP/2 connection + single stream ----

type rawH2Conn struct {
	c   net.Conn
	bw  *bufio.Writer
	fr  *http2.Framer
	rmu sync.Mutex
	wmu sync.Mutex

	// flow control
	connWindow uint32
	strWindow  uint32

	closed chan struct{}
}

func newRawH2Conn(c net.Conn) *rawH2Conn {
	br := bufio.NewReaderSize(c, 32*1024)
	bw := bufio.NewWriterSize(c, 32*1024)
	fr := http2.NewFramer(bw, br)
	// We decode response headers ourselves (see readResponseHeaders).
	// Keep ReadMetaHeaders nil so Framer returns raw *HeadersFrame/*ContinuationFrame.
	fr.ReadMetaHeaders = nil
	return &rawH2Conn{
		c:          c,
		bw:         bw,
		fr:         fr,
		connWindow: 65535,
		strWindow:  65535,
		closed:     make(chan struct{}),
	}
}

func (c *rawH2Conn) init(ctx context.Context) error {
	// Client preface (must be first bytes on the connection)
	c.wmu.Lock()
	_, err := io.WriteString(c.bw, http2.ClientPreface)
	if err == nil {
		err = c.bw.Flush()
	}
	c.wmu.Unlock()
	if err != nil {
		return err
	}

	// SETTINGS
	// RFC 8441 requires SETTINGS_ENABLE_CONNECT_PROTOCOL=1 to be negotiated
	// before using Extended CONNECT with the ":protocol" pseudo-header.
	// Some servers won't accept ":protocol" unless the client also advertises
	// this setting.
	const settingEnableConnectProtocol http2.SettingID = 0x8
	if err := c.writeFrame(func() error {
		return c.fr.WriteSettings(http2.Setting{ID: settingEnableConnectProtocol, Val: 1})
	}); err != nil {
		return err
	}

	// Read server SETTINGS, ACK it.
	// We also check whether the server advertises SETTINGS_ENABLE_CONNECT_PROTOCOL=1.
	// If it doesn't, many servers will RST_STREAM with PROTOCOL_ERROR once they
	// see ":protocol".
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := c.readFrame()
		if err != nil {
			return err
		}
		sf, ok := f.(*http2.SettingsFrame)
		if !ok {
			continue
		}
		if sf.IsAck() {
			continue
		}
		serverEnable := uint32(0)
		found := false
		if err := sf.ForeachSetting(func(s http2.Setting) error {
			if s.ID == settingEnableConnectProtocol {
				serverEnable = s.Val
				found = true
			}
			return nil
		}); err != nil {
			return err // или: return fmt.Errorf("foreach setting: %w", err)
		}
		wsDebugf("h2raw: server SETTINGS_ENABLE_CONNECT_PROTOCOL present=%v val=%d", found, serverEnable)
		if !found || serverEnable != 1 {
			return fmt.Errorf("rfc8441 not supported by server: SETTINGS_ENABLE_CONNECT_PROTOCOL=%d (present=%v)", serverEnable, found)
		}
		// ACK settings
		return c.writeFrame(func() error { return c.fr.WriteSettingsAck() })
	}
}

func (c *rawH2Conn) openWebSocketStream(ctx context.Context, u *url.URL) (WSConn, error) {
	// RFC6455 key/accept
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	accept := computeAccept(key)

	// Do not forward client-side control query params (h2/h2only/http2/h2c).
	// Many servers route by path+query and will reject unknown query values.
	path := cleanedRequestURI(u)
	// Use authority from URL (includes port when non-default).
	authority := u.Host

	// HPACK encode request headers
	var hb strings.Builder
	enc := hpack.NewEncoder(&hb)
	_ = enc.WriteField(hpack.HeaderField{Name: ":method", Value: "CONNECT"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":scheme", Value: "https"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":authority", Value: authority})
	_ = enc.WriteField(hpack.HeaderField{Name: ":path", Value: path})
	_ = enc.WriteField(hpack.HeaderField{Name: ":protocol", Value: "websocket"})
	_ = enc.WriteField(hpack.HeaderField{Name: "sec-websocket-version", Value: "13"})
	_ = enc.WriteField(hpack.HeaderField{Name: "sec-websocket-key", Value: key})
	if origin := u.Query().Get("origin"); origin != "" {
		_ = enc.WriteField(hpack.HeaderField{Name: "origin", Value: origin})
	}

	// Send HEADERS on stream 1.
	wsDebugf("h2raw: send CONNECT :authority=%q :path=%q", authority, path)
	if err := c.writeFrame(func() error {
		return c.fr.WriteHeaders(http2.HeadersFrameParam{
			StreamID:      1,
			BlockFragment: []byte(hb.String()),
			EndHeaders:    true,
			EndStream:     false,
		})
	}); err != nil {
		return nil, err
	}

	// Read response HEADERS for stream 1.
	status, hdrs, err := c.readResponseHeaders(ctx, 1)
	if err != nil {
		return nil, err
	}
	wsDebugf("h2raw: response status=%q", status)
	if status != "200" {
		return nil, fmt.Errorf("%w: unexpected status %s", errRFC8441HandshakeFailed, status)
	}
	if got := hdrs["sec-websocket-accept"]; got != "" && got != accept {
		return nil, fmt.Errorf("%w: bad sec-websocket-accept", errRFC8441HandshakeFailed)
	}

	// Stream data pump.
	pr, pw := io.Pipe()
	ws := &rawH2Stream{
		parent: c,
		r:      pr,
		w:      pw,
	}
	go ws.readLoop(ctx)
	return newFramedWSConn(ws), nil
}

func cleanedRequestURI(u *url.URL) string {
	// Keep path as-is, but remove our internal control parameters.
	q := u.Query()
	q.Del("h2")
	q.Del("http2")
	q.Del("h2only")
	q.Del("h2c")
	q.Del("h3")
	q.Del("http3")
	q.Del("h3only")
	q.Del("quic")
	q.Del("test_path")
	q.Del("health_path")
	q.Del("hc_path")

	// Rebuild a copy so we don't mutate caller URL.
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	qs := q.Encode()
	if qs == "" {
		return path
	}
	return path + "?" + qs
}

func computeAccept(key string) string {
	// RFC6455 magic GUID
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (c *rawH2Conn) readResponseHeaders(ctx context.Context, streamID uint32) (status string, hdrs map[string]string, err error) {
	hdrs = map[string]string{}
	var block []byte
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		f, err := c.readFrame()
		if err != nil {
			return "", nil, err
		}
		switch ff := f.(type) {
		case *http2.SettingsFrame:
			// SETTINGS can arrive at any time; ACK them to avoid stalling strict peers.
			if !ff.IsAck() {
				_ = c.writeFrame(func() error { return c.fr.WriteSettingsAck() })
			}
			continue
		case *http2.HeadersFrame:
			if ff.StreamID != streamID {
				continue
			}
			block = append(block, ff.HeaderBlockFragment()...)
			if ff.HeadersEnded() {
				goto decode
			}
		case *http2.MetaHeadersFrame:
			// Shouldn't happen with ReadMetaHeaders=nil, but handle defensively.
			if ff.StreamID != streamID {
				continue
			}
			for _, hf := range ff.Fields {
				name := strings.ToLower(hf.Name)
				if name == ":status" {
					status = hf.Value
					continue
				}
				hdrs[name] = hf.Value
			}
			return status, hdrs, nil
		case *http2.ContinuationFrame:
			if ff.StreamID != streamID {
				continue
			}
			block = append(block, ff.HeaderBlockFragment()...)
			if ff.HeadersEnded() {
				goto decode
			}
		case *http2.GoAwayFrame:
			return "", nil, fmt.Errorf("%w: received GOAWAY", errRFC8441HandshakeFailed)
		case *http2.RSTStreamFrame:
			if ff.StreamID != streamID {
				continue
			}
			return "", nil, fmt.Errorf("%w: received RST_STREAM (code=%v)", errRFC8441HandshakeFailed, ff.ErrCode)
		}
	}

decode:
	dec := hpack.NewDecoder(4096, func(f hpack.HeaderField) {
		name := strings.ToLower(f.Name)
		if name == ":status" {
			status = f.Value
			return
		}
		hdrs[name] = f.Value
	})
	_, derr := dec.Write(block)
	if derr != nil {
		return "", nil, derr
	}
	return status, hdrs, nil
}

func (c *rawH2Conn) readFrame() (http2.Frame, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	return c.fr.ReadFrame()
}

func (c *rawH2Conn) writeFrame(fn func() error) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	return c.bw.Flush()
}

func (c *rawH2Conn) Close() error {
	select {
	case <-c.closed:
		return nil
	default:
		close(c.closed)
		return c.c.Close()
	}
}

// rawH2Stream adapts a single HTTP/2 stream to io.ReadWriteCloser for WS framing.
type rawH2Stream struct {
	parent *rawH2Conn
	r      *io.PipeReader
	w      *io.PipeWriter // writes into reader? (fed by readLoop)
}

func (s *rawH2Stream) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *rawH2Stream) Write(p []byte) (int, error) {
	// Send DATA on stream 1.
	s.parent.wmu.Lock()
	defer s.parent.wmu.Unlock()

	off := 0
	for off < len(p) {
		end := off + rawH2MaxDataFrameChunk
		if end > len(p) {
			end = len(p)
		}
		chunk := p[off:end]
		if err := s.parent.fr.WriteData(1, false, chunk); err != nil {
			return off, err
		}
		off = end
	}
	if err := s.parent.bw.Flush(); err != nil {
		return off, err
	}
	return len(p), nil
}

func (s *rawH2Stream) Close() error {
	// Best-effort stream close.
	_ = s.parent.writeFrame(func() error { return s.parent.fr.WriteRSTStream(1, http2.ErrCodeCancel) })
	_ = s.parent.Close()
	return s.w.Close()
}

func (s *rawH2Stream) readLoop(ctx context.Context) {
	defer s.w.Close()
	var pendingWindowUpdate uint32
	flushWindowUpdate := func(force bool) {
		if pendingWindowUpdate == 0 {
			return
		}
		if !force && pendingWindowUpdate < rawH2WindowUpdateBatch {
			return
		}
		v := pendingWindowUpdate
		pendingWindowUpdate = 0
		_ = s.parent.writeFrame(func() error {
			_ = s.parent.fr.WriteWindowUpdate(0, v)
			return s.parent.fr.WriteWindowUpdate(1, v)
		})
	}
	defer flushWindowUpdate(true)

	for {
		select {
		case <-s.parent.closed:
			return
		default:
		}
		if err := ctx.Err(); err != nil {
			return
		}
		f, err := s.parent.readFrame()
		if err != nil {
			_ = s.w.CloseWithError(err)
			return
		}
		switch ff := f.(type) {
		case *http2.DataFrame:
			if ff.StreamID != 1 {
				continue
			}
			data := ff.Data()
			if len(data) > 0 {
				_, _ = s.w.Write(data)
				pendingWindowUpdate += uint32(len(data))
				flushWindowUpdate(false)
			}
			if ff.StreamEnded() {
				flushWindowUpdate(true)
				return
			}
		case *http2.RSTStreamFrame:
			if ff.StreamID == 1 {
				return
			}
		case *http2.GoAwayFrame:
			return
		}
	}
}
