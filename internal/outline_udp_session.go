package internal

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"nhooyr.io/websocket"
)

type OutlineUDPSession struct {
	ctx    context.Context
	cancel context.CancelFunc

	wsc  *websocket.Conn
	enc  net.PacketConn // decrypted plaintext in/out (addr+payload)
	mu   sync.Mutex
	subs map[string]chan []byte // key: remote "ip:port"
}

func NewOutlineUDPSession(parent context.Context, lb *LoadBalancer, up *UpstreamState) (*OutlineUDPSession, error) {
	ctx, cancel := context.WithCancel(parent)

	wsc, err := DialWSStream(ctx, up.cfg.UDPWSS, lb.fwmark)
	if err != nil {
		cancel()
		return nil, err
	}

	ciph, err := core.PickCipher(up.cfg.Cipher, nil, up.cfg.Secret)
	if err != nil {
		_ = wsc.Close(websocket.StatusNormalClosure, "cipher-error")
		cancel()
		return nil, err
	}

	wsPC := NewWSPacketConn(ctx, wsc)
	enc := ciph.PacketConn(wsPC)

	s := &OutlineUDPSession{
		ctx:    ctx,
		cancel: cancel,
		wsc:    wsc,
		enc:    enc,
		subs:   make(map[string]chan []byte),
	}

	go s.readLoop()

	return s, nil
}

func (s *OutlineUDPSession) Close() {
	s.cancel()
	_ = s.enc.Close()
	_ = s.wsc.Close(websocket.StatusNormalClosure, "close")
	s.mu.Lock()
	for _, ch := range s.subs {
		close(ch)
	}
	s.subs = map[string]chan []byte{}
	s.mu.Unlock()
}

// Subscribe returns a channel for packets coming from given remote (ip:port).
func (s *OutlineUDPSession) Subscribe(remote string) <-chan []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.subs[remote]
	if ch == nil {
		ch = make(chan []byte, 64)
		s.subs[remote] = ch
	}
	return ch
}

func (s *OutlineUDPSession) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, _, err := s.enc.ReadFrom(buf)
		if err != nil {
			return
		}
		plain := buf[:n]

		// parse socks addr header
		host, port, off, err := parseSocksAddrFromPlain(plain)
		if err != nil || off >= len(plain) {
			continue
		}
		from := net.JoinHostPort(host, port)
		payload := append([]byte(nil), plain[off:]...) // copy

		s.mu.Lock()
		ch := s.subs[from]
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- payload:
			default:
				// drop if slow consumer
			}
		}
	}
}

// Send sends one datagram to dst through Outline UDP-over-WS.
// dst must be "host:port" (IPv6 must be bracketed).
func (s *OutlineUDPSession) Send(dst string, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	addr := socks.ParseAddr(dst)
	if addr == nil {
		return socks.ErrAddressNotSupported
	}
	plain := make([]byte, 0, len(addr)+len(payload))
	plain = append(plain, addr...)
	plain = append(plain, payload...)

	_, err := s.enc.WriteTo(plain, dummyAddr{})
	return err
}

var ErrUDPSessionClosed = errors.New("udp session closed")
