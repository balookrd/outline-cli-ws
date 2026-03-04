//go:build !unit

package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/quic"
)

const (
	h3FrameData                    = 0x0
	h3FrameHeaders                 = 0x1
	h3FrameSettings                = 0x4
	h3StreamControl                = 0x0
	h3StreamQpackEncoder           = 0x2
	h3StreamQpackDecoder           = 0x3
	h3SettingEnableConnectProtocol = 0x08
	h3ErrorNoError                 = 0x100
	h3ErrorGeneralProtocol         = 0x101
	h3ErrorInternal                = 0x102
	h3ErrorStreamCreation          = 0x103
	h3ErrorClosedCriticalStream    = 0x104
	h3ErrorFrameUnexpected         = 0x105
	h3ErrorFrame                   = 0x106
	h3ErrorExcessiveLoad           = 0x107
	h3ErrorID                      = 0x108
	h3ErrorSettings                = 0x109
	h3ErrorMissingSettings         = 0x10a
	h3ErrorRequestRejected         = 0x10b
	h3ErrorRequestCancelled        = 0x10c
	h3ErrorRequestIncomplete       = 0x10d
	h3ErrorMessage                 = 0x10e
	h3ErrorConnect                 = 0x10f
	h3ErrorVersionFallback         = 0x110
)

type h3ClientStreamProfile uint8

const (
	h3ClientStreamsControlAndQPACK h3ClientStreamProfile = iota
	h3ClientStreamsControlOnly
)

type h3wsStream struct {
	s               *quic.Stream
	qc              *quic.Conn
	ep              *quic.Endpoint
	stopPeerDrainer context.CancelFunc
}

type h3PeerObservations struct {
	mu                    sync.RWMutex
	sawSettings           bool
	enableConnectProtocol bool
	settingsSeen          chan struct{}
}

func newH3PeerObservations() *h3PeerObservations {
	return &h3PeerObservations{settingsSeen: make(chan struct{})}
}

func (o *h3PeerObservations) setSettings(enableConnectProtocol bool) {
	o.mu.Lock()
	alreadySaw := o.sawSettings
	o.sawSettings = true
	o.enableConnectProtocol = enableConnectProtocol
	ch := o.settingsSeen
	o.mu.Unlock()
	if !alreadySaw && ch != nil {
		close(ch)
	}
}

func (o *h3PeerObservations) snapshot() (sawSettings bool, enableConnectProtocol bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sawSettings, o.enableConnectProtocol
}

func (o *h3PeerObservations) waitSettings(timeout time.Duration) {
	if o == nil || timeout <= 0 {
		return
	}
	o.mu.RLock()
	if o.sawSettings {
		o.mu.RUnlock()
		return
	}
	ch := o.settingsSeen
	o.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(timeout):
	}
}

func (s *h3wsStream) Read(p []byte) (int, error)  { return s.s.Read(p) }
func (s *h3wsStream) Write(p []byte) (int, error) { return s.s.Write(p) }
func (s *h3wsStream) Close() error {
	if s.stopPeerDrainer != nil {
		s.stopPeerDrainer()
	}
	s.s.CloseRead()
	s.s.CloseWrite()
	_ = s.qc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = s.ep.Close(ctx)
	return nil
}

func dialRFC9220(ctx context.Context, u *url.URL) (WSConn, error) {
	profiles := []h3ClientStreamProfile{
		h3ClientStreamsControlAndQPACK,
		h3ClientStreamsControlOnly,
	}
	var lastErr error
	for i, profile := range profiles {
		c, err := dialRFC9220Profile(ctx, u, profile)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "expected first frame to be a HEADERS frame") {
			break
		}
		if i+1 < len(profiles) {
			wsDebugf("h3: retrying rfc9220 dial with alternate client stream profile=%d after peer frame-order error", profiles[i+1])
		}
	}
	return nil, lastErr
}

func dialRFC9220Profile(ctx context.Context, u *url.URL, profile h3ClientStreamProfile) (WSConn, error) {
	if u.Scheme != "wss" && u.Scheme != "https" {
		return nil, fmt.Errorf("rfc9220 requires wss/https, got %q", u.Scheme)
	}
	h3BaseCtx := ctx
	effectiveH3Timeout := h3HandshakeTimeout
	if ddl, ok := ctx.Deadline(); ok {
		if rem := time.Until(ddl); rem > 0 && rem < effectiveH3Timeout {
			// Some call sites use short per-attempt deadlines (~3s) that are too
			// aggressive for RFC9220/QUIC handshake and can cause false failures.
			// Keep context values but ignore the deadline; preserve explicit cancel.
			h3BaseCtx = context.WithoutCancel(ctx)
		}
	}
	h3ctx, h3cancel := context.WithTimeout(h3BaseCtx, effectiveH3Timeout)
	defer h3cancel()
	if h3BaseCtx != ctx {
		go func(parent context.Context) {
			<-parent.Done()
			if !errors.Is(parent.Err(), context.DeadlineExceeded) {
				h3cancel()
			}
		}(ctx)
	}

	host := u.Hostname()
	if host == "" {
		host, _ = splitHostPortDefault(u.Host, "443")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	authority := u.Host
	if authority == "" {
		authority = host
	}
	dialAddr := net.JoinHostPort(host, port)
	wsDebugf("h3: prepare dial host=%q port=%q authority=%q dial_addr=%q timeout=%s url=%q", host, port, authority, dialAddr, effectiveH3Timeout, u.Redacted())

	tlsConf := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host, NextProtos: []string{"h3"}}
	qcConf := &quic.Config{TLSConfig: tlsConf}
	if wsDebugEnabled.Load() {
		qcConf.QLogLogger = slog.New(&h3QlogDebugHandler{})
		wsDebugf("h3: qlog packet tracing enabled (first %d sent/recv packets)", h3QlogFirstPackets)
	}
	ep, err := quic.Listen("udp", ":0", qcConf)
	if err != nil {
		wsDebugf("h3: quic listen failed err=%v", err)
		return nil, err
	}
	wsDebugf("h3: quic endpoint ready, dialing addr=%q sni=%q alpn=%v", dialAddr, tlsConf.ServerName, tlsConf.NextProtos)
	qconn, err := ep.Dial(h3ctx, "udp", dialAddr, qcConf)
	if err != nil {
		wsDebugf("h3: quic dial failed addr=%q err=%s", dialAddr, h3DescribeErr(err))
		_ = ep.Close(context.Background())
		return nil, err
	}
	wsDebugf("h3: quic dial established addr=%q", dialAddr)
	obs := newH3PeerObservations()
	peerDrainCancel := startH3PeerStreamDrainer(qconn, obs)
	h3Established := false
	defer func() {
		if h3Established {
			return
		}
		peerDrainCancel()
	}()

	if err := h3OpenClientUniStreams(h3ctx, qconn, profile); err != nil {
		wsDebugf("h3: open client uni streams failed err=%v", err)
		return nil, err
	}
	wsDebugf("h3: client streams initialized profile=%d (%s)", profile, h3ProfileName(profile))

	st, err := qconn.NewStream(h3ctx)
	if err != nil {
		wsDebugf("h3: open request stream failed err=%v", err)
		return nil, err
	}
	wsDebugf("h3: request stream opened")

	headers := h3ConnectHeaders(u, authority)
	requestFrame := appendVarint(nil, h3FrameHeaders)
	requestFrame = appendVarint(requestFrame, uint64(len(headers)))
	requestFrame = append(requestFrame, headers...)
	wsDebugf("h3: request CONNECT headers=%s", h3FormatHeaders(h3ConnectHeaderMap(u, authority)))
	wsDebugf("h3: writing HEADERS frame total_len=%d (field_section_len=%d)", len(requestFrame), len(headers))
	if err := h3WriteWithContext(h3ctx, st, requestFrame); err != nil {
		wsDebugf("h3: write HEADERS frame failed err=%v", err)
		return nil, err
	}
	// x/net/quic may buffer stream data until scheduler tick; force flushing the
	// CONNECT request headers so the server can respond promptly.
	_ = st.Flush()
	wsDebugf("h3: request headers sent, flushed, waiting response")
	// IMPORTANT: do NOT close the client request stream here.
	//
	// RFC 9220 upgrades this very stream into a bidirectional WebSocket data
	// channel after successful CONNECT response headers. Closing write-side at
	// handshake time makes the resulting tunnel half-closed and breaks any
	// client->upstream traffic (seen with QUIC proxy backends).
	handshakeStarted := time.Now()

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
		elapsed := time.Since(handshakeStarted)
		wsDebugf("h3: timeout/cancel while waiting response after %s (authority=%q path=%q err=%v)", elapsed, authority, cleanedRequestURI(u), h3ctx.Err())
		_ = qconn.Close()
		_ = ep.Close(context.Background())
		if errors.Is(h3ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("rfc9220 handshake timeout waiting response headers after %s", elapsed)
		}
		return nil, h3ctx.Err()
	case err := <-errCh:
		if hint := h3PeerSupportHint(err, obs); hint != "" {
			wsDebugf("h3: read response headers failed hint=%s", hint)
			err = fmt.Errorf("%w: %s", err, hint)
		}
		wsDebugf("h3: read response headers failed err=%s", h3DescribeErr(err))
		return nil, err
	case resp = <-respCh:
	}
	wsDebugf("h3: response status=%q headers=%s", resp[":status"], h3FormatHeaders(resp))
	if resp[":status"] != "200" {
		return nil, fmt.Errorf("rfc9220 connect failed: status=%s headers=%s", resp[":status"], h3FormatHeaders(resp))
	}
	if got := resp["sec-websocket-accept"]; got != "" {
		wsDebugf("h3: server returned optional sec-websocket-accept=%q", got)
	}
	wsDebugf("h3: websocket CONNECT established")
	h3Established = true
	return newFramedWSConn(&h3wsStream{s: st, qc: qconn, ep: ep, stopPeerDrainer: peerDrainCancel}), nil
}

func startH3PeerStreamDrainer(c *quic.Conn, obs *h3PeerObservations) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			st, err := c.AcceptStream(ctx)
			if err != nil {
				return
			}
			go func(s *quic.Stream) {
				// peer-initiated uni streams будут read-only
				defer s.CloseRead()

				if s.IsReadOnly() {
					// H3 uni stream начинается с varint stream type (0=control,2,3=qpack)
					typ, err := readVarint(s)
					if err == nil {
						wsDebugf("h3: peer stream accepted type=%d", typ)
						if typ == h3StreamControl {
							h3LogPeerControlStream(s, obs)
							return
						}
					}
				} else {
					wsDebugf("h3: peer bidi stream accepted")
				}

				_, _ = io.Copy(io.Discard, s)
			}(st)
		}
	}()
	return cancel
}

func h3OpenClientUniStreams(ctx context.Context, c *quic.Conn, profile h3ClientStreamProfile) error {
	// 1) Control stream + SETTINGS_ENABLE_CONNECT_PROTOCOL.
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
	_ = st.Flush()

	if profile == h3ClientStreamsControlOnly {
		return nil
	}

	// 2) Some strict H3 stacks expect client QPACK uni streams to be present
	// before processing request streams. Open both streams and send only the
	// stream-type varint (no encoder instructions).
	qenc, err := c.NewSendOnlyStream(ctx)
	if err != nil {
		return err
	}
	if err := h3WriteWithContext(ctx, qenc, appendVarint(nil, h3StreamQpackEncoder)); err != nil {
		return err
	}
	_ = qenc.Flush()

	qdec, err := c.NewSendOnlyStream(ctx)
	if err != nil {
		return err
	}
	if err := h3WriteWithContext(ctx, qdec, appendVarint(nil, h3StreamQpackDecoder)); err != nil {
		return err
	}
	_ = qdec.Flush()
	return nil
}

func h3WriteWithContext(ctx context.Context, st *quic.Stream, b []byte) error {
	remaining := b
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("h3 write timeout")
			}
			return err
		}
		n, err := st.Write(remaining)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[n:]
	}
	return nil
}

func h3ConnectHeaders(u *url.URL, authority string) []byte {
	fields := h3ConnectHeaderFields(u, authority)
	return h3EncodeHeaders(fields)
}

func h3ConnectHeaderMap(u *url.URL, authority string) map[string]string {
	out := map[string]string{}
	for _, f := range h3ConnectHeaderFields(u, authority) {
		out[f[0]] = f[1]
	}
	return out
}

func h3ConnectHeaderFields(u *url.URL, authority string) [][2]string {
	fields := [][2]string{{":method", "CONNECT"}, {":scheme", "https"}, {":authority", authority}, {":path", cleanedRequestURI(u)}, {":protocol", "websocket"}, {"sec-websocket-version", "13"}}
	if origin := u.Query().Get("origin"); origin != "" {
		fields = append(fields, [2]string{"origin", origin})
	}
	return fields
}

func h3ReadResponseHeaders(r io.Reader) (map[string]string, error) {
	const maxLoggedNonHeadersFrames = 8
	nonHeaders := 0
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
		nonHeaders++
		if nonHeaders <= maxLoggedNonHeadersFrames {
			wsDebugf("h3: pre-response frame #%d type=%d len=%d (waiting for HEADERS)", nonHeaders, ft, n)
		}
	}
}

func h3ProfileName(p h3ClientStreamProfile) string {
	switch p {
	case h3ClientStreamsControlAndQPACK:
		return "control+qpack"
	case h3ClientStreamsControlOnly:
		return "control-only"
	default:
		return "unknown"
	}
}

func h3DescribeErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	parts := []string{fmt.Sprintf("%T: %v", err, err)}
	for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
		parts = append(parts, fmt.Sprintf("%T: %v", u, u))
		if sc, ok := u.(quic.StreamErrorCode); ok {
			parts = append(parts, fmt.Sprintf("quic.StreamErrorCode=0x%x (%s)", uint64(sc), h3ErrorName(uint64(sc))))
		}
	}
	return strings.Join(parts, " | cause: ")
}

func h3LogPeerControlStream(r io.Reader, obs *h3PeerObservations) {
	const maxControlFramesToLog = 8
	for i := 0; i < maxControlFramesToLog; i++ {
		ft, err := readVarint(r)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				wsDebugf("h3: peer control stream read end err=%s", h3DescribeErr(err))
			}
			return
		}
		n, err := readVarint(r)
		if err != nil {
			wsDebugf("h3: peer control stream read frame length failed err=%s", h3DescribeErr(err))
			return
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			wsDebugf("h3: peer control stream read frame payload failed type=%d len=%d err=%s", ft, n, h3DescribeErr(err))
			return
		}
		if ft == h3FrameSettings {
			enableConnectProtocol, formatted := h3ParseSettingsPayload(payload)
			if obs != nil {
				obs.setSettings(enableConnectProtocol)
			}
			wsDebugf("h3: peer SETTINGS %s", formatted)
			continue
		}
		wsDebugf("h3: peer control frame type=%d len=%d", ft, n)
	}
}

func h3FormatSettingsPayload(payload []byte) string {
	_, formatted := h3ParseSettingsPayload(payload)
	return formatted
}

func h3ParseSettingsPayload(payload []byte) (enableConnectProtocol bool, formatted string) {
	if len(payload) == 0 {
		return false, "{}"
	}
	out := map[string]string{}
	r := strings.NewReader(string(payload))
	idx := 0
	for r.Len() > 0 && idx < 32 {
		id, err := readVarint(r)
		if err != nil {
			break
		}
		val, err := readVarint(r)
		if err != nil {
			break
		}
		name := fmt.Sprintf("0x%x", id)
		if id == h3SettingEnableConnectProtocol {
			name = "ENABLE_CONNECT_PROTOCOL(0x8)"
			enableConnectProtocol = (val == 1)
		}
		out[name] = fmt.Sprintf("%d", val)
		idx++
	}
	if len(out) == 0 {
		return false, fmt.Sprintf("{raw_len=%d decode=failed}", len(payload))
	}
	return enableConnectProtocol, h3FormatHeaders(out)
}

func h3PeerSupportHint(err error, obs *h3PeerObservations) string {
	if obs == nil {
		return ""
	}
	isMessageErr := false
	for u := err; u != nil; u = errors.Unwrap(u) {
		if sc, ok := u.(quic.StreamErrorCode); ok && uint64(sc) == h3ErrorMessage {
			isMessageErr = true
			break
		}
	}
	if !isMessageErr {
		return ""
	}
	// Control stream SETTINGS can arrive slightly after request stream reset.
	// Give the drainer a short chance to observe peer capabilities before
	// deciding whether to emit the unsupported-RFC9220 hint.
	obs.waitSettings(150 * time.Millisecond)
	saw, enable := obs.snapshot()
	if !saw || enable {
		return ""
	}
	return "peer SETTINGS missing ENABLE_CONNECT_PROTOCOL=1; RFC9220 Extended CONNECT likely unsupported on this H3 endpoint/proxy"
}

func h3ErrorName(code uint64) string {
	switch code {
	case h3ErrorNoError:
		return "H3_NO_ERROR"
	case h3ErrorGeneralProtocol:
		return "H3_GENERAL_PROTOCOL_ERROR"
	case h3ErrorInternal:
		return "H3_INTERNAL_ERROR"
	case h3ErrorStreamCreation:
		return "H3_STREAM_CREATION_ERROR"
	case h3ErrorClosedCriticalStream:
		return "H3_CLOSED_CRITICAL_STREAM"
	case h3ErrorFrameUnexpected:
		return "H3_FRAME_UNEXPECTED"
	case h3ErrorFrame:
		return "H3_FRAME_ERROR"
	case h3ErrorExcessiveLoad:
		return "H3_EXCESSIVE_LOAD"
	case h3ErrorID:
		return "H3_ID_ERROR"
	case h3ErrorSettings:
		return "H3_SETTINGS_ERROR"
	case h3ErrorMissingSettings:
		return "H3_MISSING_SETTINGS"
	case h3ErrorRequestRejected:
		return "H3_REQUEST_REJECTED"
	case h3ErrorRequestCancelled:
		return "H3_REQUEST_CANCELLED"
	case h3ErrorRequestIncomplete:
		return "H3_REQUEST_INCOMPLETE"
	case h3ErrorMessage:
		return "H3_MESSAGE_ERROR"
	case h3ErrorConnect:
		return "H3_CONNECT_ERROR"
	case h3ErrorVersionFallback:
		return "H3_VERSION_FALLBACK"
	default:
		return "unknown"
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
