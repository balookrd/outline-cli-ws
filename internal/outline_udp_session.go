package internal

import (
	"context"
	"net"
	"strconv"
	"sync"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
)

type UDPPayload struct {
	B       []byte
	release func()
}

func (p UDPPayload) Release() {
	if p.release != nil {
		p.release()
	}
}

type addrKey struct {
	atyp byte     // 0x01 v4, 0x04 v6, 0x03 domain
	ip   [16]byte // for v4 use first 4
	port uint16
	// domain key (rare path)
	domain string
}

type OutlineUDPSession struct {
	ctx    context.Context
	cancel context.CancelFunc

	wsc WSConn
	enc net.PacketConn

	mu   sync.Mutex
	subs map[addrKey]chan UDPPayload

	pool sync.Pool // stores *[]byte
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
		_ = wsc.Close(WSStatusNormalClosure, "close")
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
		subs:   make(map[addrKey]chan UDPPayload),
	}
	s.pool.New = func() any {
		b := make([]byte, 0, 2048)
		return &b
	}
	go s.readLoop()
	return s, nil
}

func (s *OutlineUDPSession) Close() {
	s.cancel()
	_ = s.enc.Close()
	_ = s.wsc.Close(WSStatusNormalClosure, "close")

	s.mu.Lock()
	for _, ch := range s.subs {
		close(ch)
	}
	s.subs = map[addrKey]chan UDPPayload{}
	s.mu.Unlock()
}

func (s *OutlineUDPSession) Subscribe(from string) <-chan UDPPayload {
	k, ok := addrKeyFromHostPort(from)
	if !ok {
		ch := make(chan UDPPayload)
		close(ch)
		return ch
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.subs[k]
	if ch == nil {
		ch = make(chan UDPPayload, 128)
		s.subs[k] = ch
	}
	return ch
}

func (s *OutlineUDPSession) Unsubscribe(from string) {
	k, ok := addrKeyFromHostPort(from)
	if !ok {
		return
	}

	s.mu.Lock()
	ch := s.subs[k]
	if ch != nil {
		delete(s.subs, k)
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
	plainBuf := make([]byte, 65535)

	for {
		n, _, err := s.enc.ReadFrom(plainBuf)
		if err != nil {
			return
		}
		plain := plainBuf[:n]

		k, off, ok := parseAddrKeyFromPlain(plain)
		if !ok || off > len(plain) {
			continue
		}
		payloadLen := len(plain) - off
		if payloadLen <= 0 {
			continue
		}

		// buffer from pool
		pbPtr := s.pool.Get().(*[]byte)
		pb := *pbPtr
		if cap(pb) < payloadLen {
			pb = make([]byte, payloadLen)
		}
		pb = pb[:payloadLen]
		copy(pb, plain[off:]) // единственный неизбежный copy

		msg := UDPPayload{
			B: pb,
			release: func() {
				*pbPtr = pb[:0]
				s.pool.Put(pbPtr)
			},
		}

		s.mu.Lock()
		ch := s.subs[k]
		s.mu.Unlock()

		if ch != nil {
			select {
			case ch <- msg:
			default:
				msg.Release()
			}
		} else {
			msg.Release()
		}
	}
}

func parseAddrKeyFromPlain(plain []byte) (k addrKey, off int, ok bool) {
	if len(plain) < 1 {
		return k, 0, false
	}
	k.atyp = plain[0]
	off = 1

	switch k.atyp {
	case 0x01: // IPv4
		if len(plain) < off+4+2 {
			return k, 0, false
		}
		copy(k.ip[:4], plain[off:off+4])
		off += 4
	case 0x04: // IPv6
		if len(plain) < off+16+2 {
			return k, 0, false
		}
		copy(k.ip[:], plain[off:off+16])
		off += 16
	case 0x03: // domain (alloc unavoidable)
		if len(plain) < off+1 {
			return k, 0, false
		}
		l := int(plain[off])
		off++
		if len(plain) < off+l+2 {
			return k, 0, false
		}
		k.domain = string(plain[off : off+l])
		off += l
	default:
		return k, 0, false
	}

	// port
	k.port = uint16(plain[off])<<8 | uint16(plain[off+1])
	off += 2
	return k, off, true
}

func addrKeyFromHostPort(hp string) (addrKey, bool) {
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		return addrKey{}, false
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return addrKey{}, false
	}
	port := uint16(p)

	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		var k addrKey
		k.atyp = 0x01
		copy(k.ip[:4], ip4)
		k.port = port
		return k, true
	}
	if ip6 := ip.To16(); ip6 != nil {
		var k addrKey
		k.atyp = 0x04
		copy(k.ip[:], ip6)
		k.port = port
		return k, true
	}

	// domain
	var k addrKey
	k.atyp = 0x03
	k.domain = host
	k.port = port
	return k, true
}
