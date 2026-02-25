package internal

import (
	"context"
	"net"
	"sync"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"nhooyr.io/websocket"
)

type OutlineUDPSession struct {
	ctx    context.Context
	cancel context.CancelFunc

	wsc *websocket.Conn
	enc net.PacketConn

	mu   sync.Mutex
	subs map[string]chan []byte // "ip:port"
}

func NewOutlineUDPSession(parent context.Context, lb *LoadBalancer, up *UpstreamState) (*OutlineUDPSession, error) {
	ctx, cancel := context.WithCancel(parent)

	wsc, err := lb.DialWSStreamLimited(ctx, up.cfg.UDPWSS)
	if err != nil {
		cancel()
		return nil, err
	}

	ciph, err := core.PickCipher(up.cfg.Cipher, nil, up.cfg.Secret)
	if err != nil {
		_ = wsc.Close(websocket.StatusNormalClosure, "close")
		cancel()
		return nil, err
	}

	wsPC := NewWSPacketConn(ctx, wsc)
	encPC := ciph.PacketConn(wsPC)

	s := &OutlineUDPSession{
		ctx:    ctx,
		cancel: cancel,
		wsc:    wsc,
		enc:    encPC,
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

func (s *OutlineUDPSession) Subscribe(from string) <-chan []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.subs[from]
	if ch == nil {
		ch = make(chan []byte, 128)
		s.subs[from] = ch
	}
	return ch
}

func (s *OutlineUDPSession) Unsubscribe(from string) {
	s.mu.Lock()
	ch := s.subs[from]
	if ch != nil {
		delete(s.subs, from)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *OutlineUDPSession) Send(dst string, payload []byte) error {
	ssAddr := socks.ParseAddr(dst)
	if ssAddr == nil {
		return socks.ErrAddressNotSupported
	}
	plain := make([]byte, 0, len(ssAddr)+len(payload))
	plain = append(plain, ssAddr...)
	plain = append(plain, payload...)
	_, err := s.enc.WriteTo(plain, dummyAddr{})
	return err
}

func (s *OutlineUDPSession) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, _, err := s.enc.ReadFrom(buf)
		if err != nil {
			return
		}
		plain := buf[:n]

		host, port, off, err := parseSocksAddrFromPlain(plain)
		if err != nil || off > len(plain) {
			continue
		}

		from := net.JoinHostPort(host, port)
		payload := append([]byte(nil), plain[off:]...)

		s.mu.Lock()
		ch := s.subs[from]
		s.mu.Unlock()

		if ch != nil {
			select {
			case ch <- payload:
			default:
			}
		}
	}
}
