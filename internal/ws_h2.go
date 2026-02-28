package internal

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"time"
)

var errRFC8441NotSupported = errors.New("rfc8441 not supported by transport")

// dialRFC8441 attempts WebSocket over HTTP/2 using RFC 8441 (Extended CONNECT).
//
// Important:
//   - This relies on the Go HTTP stack supporting Extended CONNECT and passing
//     the ":protocol" pseudo-header through to HTTP/2. In some Go versions this
//     is behind a GODEBUG flag (commonly documented as GODEBUG=http2xconnect=1).
//   - If unsupported, this returns errRFC8441NotSupported.
func dialRFC8441(ctx context.Context, u *url.URL, tr *http.Transport) (WSConn, error) {
	// RFC 8441 uses "http"/"https" schemes, mapped from ws/wss.
	target := *u
	switch u.Scheme {
	case "wss":
		target.Scheme = "https"
	case "ws":
		target.Scheme = "http"
	default:
		return nil, fmt.Errorf("unsupported scheme for rfc8441: %s", u.Scheme)
	}

	// Ensure we negotiate HTTP/2 where possible.
	tr2 := tr.Clone()
	tr2.ForceAttemptHTTP2 = true
	if tr2.TLSClientConfig == nil {
		tr2.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	// We need full-duplex: request body is our write side; response body is read side.
	pr, pw := io.Pipe()

	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, target.String(), pr)
	if err != nil {
		_ = pw.Close()
		return nil, err
	}

	// RFC 8441: ":protocol = websocket" pseudo-header.
	//
	// Prefer using the stdlib Extended CONNECT support when available.
	// If the toolchain doesn't support it, fall back to a raw HTTP/2
	// implementation that can still speak RFC 8441.
	if !setRequestProtocol(req, "websocket") {
		_ = pr.Close()
		_ = pw.Close()
		return dialRFC8441RawH2(ctx, u, tr2)
	}
	req.Header.Set("sec-websocket-version", "13")
	if origin := u.Query().Get("origin"); origin != "" {
		req.Header.Set("origin", origin)
	}

	cli := &http.Client{
		Timeout:   0, // stream
		Transport: tr2,
	}

	resp, err := cli.Do(req)
	if err != nil {
		_ = pw.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		_ = pw.Close()
		return nil, fmt.Errorf("rfc8441 connect failed: %s", resp.Status)
	}

	stream := &h2Stream{
		r: resp.Body,
		w: pw,
		c: func() error {
			_ = resp.Body.Close()
			return pw.Close()
		},
	}

	return newFramedWSConn(stream), nil
}

// setRequestProtocol tries to set req.Protocol = protocol.
// Returns false if the running Go toolchain doesn't support it.
func setRequestProtocol(req *http.Request, protocol string) bool {
	rv := reflect.ValueOf(req).Elem()
	f := rv.FieldByName("Protocol")
	if !f.IsValid() || !f.CanSet() || f.Kind() != reflect.String {
		return false
	}
	f.SetString(protocol)
	return true
}

// h2Stream is a minimal full-duplex stream built from CONNECT's req/resp bodies.
type h2Stream struct {
	r io.ReadCloser
	w *io.PipeWriter
	c func() error
}

func (s *h2Stream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *h2Stream) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *h2Stream) Close() error                { return s.c() }

// framedWSConn implements WSConn using RFC6455 framing over an io.ReadWriteCloser.
type framedWSConn struct {
	br *bufio.Reader
	s  io.ReadWriteCloser
	mu sync.Mutex // serialize writes; multiple goroutines may write (data + auto pong/close)
}

func newFramedWSConn(s io.ReadWriteCloser) *framedWSConn {
	return &framedWSConn{
		br: bufio.NewReaderSize(s, 32*1024),
		s:  s,
	}
}

func (c *framedWSConn) writeRaw(frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.s.Write(frame)
	return err
}

func (c *framedWSConn) Read(ctx context.Context) (WSMessageType, []byte, error) {
	// Note: we cannot reliably cancel a blocked read on generic io.Reader without
	// deadlines, so ctx is best-effort.
	for {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}

		typ, payload, fin, err := readFrame(c.br)
		if err != nil {
			return 0, nil, err
		}

		switch typ {
		case WSMessagePing:
			// auto-respond with pong
			_ = c.Write(ctx, WSMessagePong, payload)
			continue
		case WSMessagePong:
			continue
		case WSMessageClose:
			// Echo close (best-effort) and stop.
			// If peer sent a close code/reason, preserve it.
			if rfc8441Debug {
				code := WSStatusCode(0)
				reason := ""
				if len(payload) >= 2 {
					code = WSStatusCode(binary.BigEndian.Uint16(payload[:2]))
					reason = string(payload[2:])
				}
				log.Printf("[WS] recv close code=%d reason=%q", code, reason)
			}
			// Send back the same payload.
			if frame, err := buildFrame(WSMessageClose, payload, true /* mask */); err == nil {
				_ = c.writeRaw(frame)
			}
			_ = c.s.Close()
			return 0, nil, io.EOF
		case WSMessageContinuation:
			// Continuation without an active message is a protocol error.
			if rfc8441Debug {
				log.Printf("[WS] protocol error: unexpected continuation")
			}
			return 0, nil, fmt.Errorf("websocket protocol error: unexpected continuation frame")
		default:
			if fin {
				return typ, payload, nil
			}
			// Fragmented message: accumulate continuation frames until FIN.
			buf := append([]byte(nil), payload...)
			for {
				if err := ctx.Err(); err != nil {
					return 0, nil, err
				}
				op2, p2, fin2, err := readFrame(c.br)
				if err != nil {
					return 0, nil, err
				}
				switch op2 {
				case WSMessagePing:
					_ = c.Write(ctx, WSMessagePong, p2)
					continue
				case WSMessagePong:
					continue
				case WSMessageClose:
					_ = c.Close(WSStatusNormalClosure, "")
					return 0, nil, io.EOF
				case WSMessageContinuation:
					buf = append(buf, p2...)
					if fin2 {
						return typ, buf, nil
					}
				default:
					// Interleaved data frames during fragmentation are invalid.
					if rfc8441Debug {
						log.Printf("[WS] protocol error: expected continuation, got opcode=%d", op2)
					}
					return 0, nil, fmt.Errorf("websocket protocol error: expected continuation, got opcode=%d", op2)
				}
			}
		}
	}
}

func (c *framedWSConn) Write(ctx context.Context, typ WSMessageType, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame, err := buildFrame(typ, data, true /* mask client frames */)
	if err != nil {
		return err
	}
	return c.writeRaw(frame)
}

func (c *framedWSConn) Close(code WSStatusCode, reason string) error {
	// Close frame: 2-byte code + reason.
	var payload []byte
	if code != 0 {
		payload = make([]byte, 2+len(reason))
		binary.BigEndian.PutUint16(payload[:2], uint16(code))
		copy(payload[2:], []byte(reason))
	}
	_ = c.Write(context.Background(), WSMessageClose, payload)
	// Give the peer a moment to read the close (best-effort).
	_ = time.AfterFunc(150*time.Millisecond, func() { _ = c.s.Close() })
	return nil
}

// ---- framing helpers ----

func readFrame(r *bufio.Reader) (typ WSMessageType, payload []byte, fin bool, err error) {
	b0, err := r.ReadByte()
	if err != nil {
		return 0, nil, false, err
	}
	b1, err := r.ReadByte()
	if err != nil {
		return 0, nil, false, err
	}

	fin = (b0 & 0x80) != 0
	op := WSMessageType(b0 & 0x0F)

	masked := (b1 & 0x80) != 0
	ln := int(b1 & 0x7F)

	var plen uint64
	switch ln {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, false, err
		}
		plen = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, false, err
		}
		plen = binary.BigEndian.Uint64(b[:])
	default:
		plen = uint64(ln)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, false, err
		}
	}

	if plen > (64 << 20) { // 64 MiB safety cap
		return 0, nil, false, fmt.Errorf("ws frame too large: %d", plen)
	}

	payload = make([]byte, plen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, false, err
	}

	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return op, payload, fin, nil
}

func buildFrame(typ WSMessageType, payload []byte, mask bool) ([]byte, error) {
	// FIN + opcode
	b0 := byte(0x80) | byte(typ&0x0F)

	// length
	plen := len(payload)
	var hdr []byte

	switch {
	case plen < 126:
		hdr = make([]byte, 2)
		hdr[0] = b0
		hdr[1] = byte(plen)
	case plen <= 0xFFFF:
		hdr = make([]byte, 4)
		hdr[0] = b0
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(plen))
	default:
		hdr = make([]byte, 10)
		hdr[0] = b0
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(plen))
	}

	var maskKey [4]byte
	outPayload := payload
	if mask {
		hdr[1] |= 0x80
		if _, err := rand.Read(maskKey[:]); err != nil {
			return nil, err
		}
		outPayload = make([]byte, len(payload))
		copy(outPayload, payload)
		for i := range outPayload {
			outPayload[i] ^= maskKey[i%4]
		}
	}

	out := make([]byte, 0, len(hdr)+4+len(outPayload))
	out = append(out, hdr...)
	if mask {
		out = append(out, maskKey[:]...)
	}
	out = append(out, outPayload...)
	return out, nil
}
