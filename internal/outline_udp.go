package internal

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"nhooyr.io/websocket"
)

type UDPAssociation struct {
	ctx    context.Context
	cancel context.CancelFunc

	uc  net.PacketConn // local UDP relay for SOCKS5 client
	wsc *websocket.Conn

	enc net.PacketConn // Shadowsocks-encrypted PacketConn over WS packet transport

	mu      sync.Mutex
	peerUDP *net.UDPAddr // learned from first client packet
}

func NewUDPAssociation(parent context.Context, up UpstreamConfig, fwmark uint32) (*UDPAssociation, error) {
	ctx, cancel := context.WithCancel(parent)

	uc, err := net.ListenPacket("udp", ":0")
	if err != nil {
		cancel()
		return nil, err
	}

	wsc, err := DialWSStream(ctx, up.UDPWSS, fwmark)
	if err != nil {
		uc.Close()
		cancel()
		return nil, err
	}

	ciph, err := core.PickCipher(up.Cipher, nil, up.Secret)
	if err != nil {
		_ = wsc.Close(websocket.StatusNormalClosure, "close")
		uc.Close()
		cancel()
		return nil, err
	}

	// Underlying packet transport: WS binary message <-> UDP datagram bytes
	wsPC := NewWSPacketConn(ctx, wsc)

	// Encrypted PacketConn: WriteTo expects plaintext (addr+payload), ReadFrom returns plaintext
	encPC := ciph.PacketConn(wsPC)

	a := &UDPAssociation{
		ctx:    ctx,
		cancel: cancel,
		uc:     uc,
		wsc:    wsc,
		enc:    encPC,
	}

	go a.readFromClientLoop()
	go a.readFromUpstreamLoop()

	return a, nil
}

func (a *UDPAssociation) LocalAddr() net.Addr { return a.uc.LocalAddr() }

func (a *UDPAssociation) Close() {
	a.cancel()
	_ = a.wsc.Close(websocket.StatusNormalClosure, "close")
	_ = a.uc.Close()
	_ = a.enc.Close()
}

// SOCKS5 UDP request/response:
// +----+------+------+----------+----------+----------+
// |RSV | FRAG | ATYP | DST.ADDR | DST.PORT |   DATA   |
// +----+------+------+----------+----------+----------+
// | 2  |  1   |  1   | Variable |    2     | Variable |
// +----+------+------+----------+----------+----------+

func (a *UDPAssociation) readFromClientLoop() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := a.uc.ReadFrom(buf)
		if err != nil {
			return
		}
		if n < 4+2+2 { // minimal-ish
			continue
		}

		if ua, ok := addr.(*net.UDPAddr); ok {
			a.mu.Lock()
			if a.peerUDP == nil {
				a.peerUDP = ua
			}
			a.mu.Unlock()
		}

		pkt := buf[:n]

		// RSV
		if pkt[0] != 0 || pkt[1] != 0 {
			continue
		}
		// FRAG unsupported
		if pkt[2] != 0 {
			continue
		}

		atyp := pkt[3]
		off := 4

		dstHost, dstPort, off2, err := parseSocksAddr(pkt, off, atyp)
		if err != nil {
			continue
		}
		off = off2

		data := pkt[off:]

		// SS UDP plaintext = [socks addr][data]
		ssAddr := socks.ParseAddr(net.JoinHostPort(dstHost, dstPort))
		if ssAddr == nil {
			continue
		}
		plain := append(ssAddr, data...)

		// Encrypt+send as one WS binary message (via wsPC underneath)
		_, err = a.enc.WriteTo(plain, dummyAddr{})
		if err != nil {
			return
		}
	}
}

func (a *UDPAssociation) readFromUpstreamLoop() {
	buf := make([]byte, 65535)
	for {
		// Read decrypted SS UDP plaintext = [socks addr][data]
		n, _, err := a.enc.ReadFrom(buf)
		if err != nil {
			return
		}
		plain := buf[:n]

		// parse addr header length (so we can rebuild SOCKS5 UDP response)
		_, _, off, err := parseSocksAddrFromPlain(plain)
		if err != nil {
			continue
		}

		payload := plain[off:]

		// SOCKS5 UDP response: RSV(2)=0, FRAG=0, then [socks addr], then DATA
		resp := make([]byte, 0, 3+len(plain))
		resp = append(resp, 0x00, 0x00, 0x00)
		resp = append(resp, plain[:off]...)
		resp = append(resp, payload...)

		a.mu.Lock()
		peer := a.peerUDP
		a.mu.Unlock()
		if peer == nil {
			continue
		}
		_, _ = a.uc.WriteTo(resp, peer)
	}
}

func parseSocksAddr(pkt []byte, off int, atyp byte) (host, port string, newOff int, err error) {
	switch atyp {
	case 0x01:
		if len(pkt) < off+4+2 {
			return "", "", 0, errors.New("short ipv4")
		}
		host = net.IP(pkt[off : off+4]).String()
		off += 4
	case 0x03:
		if len(pkt) < off+1 {
			return "", "", 0, errors.New("short domain len")
		}
		l := int(pkt[off])
		off++
		if len(pkt) < off+l+2 {
			return "", "", 0, errors.New("short domain")
		}
		host = string(pkt[off : off+l])
		off += l
	case 0x04:
		if len(pkt) < off+16+2 {
			return "", "", 0, errors.New("short ipv6")
		}
		host = net.IP(pkt[off : off+16]).String()
		off += 16
	default:
		return "", "", 0, errors.New("bad atyp")
	}
	p := binary.BigEndian.Uint16(pkt[off : off+2])
	off += 2
	return host, itoa(int(p)), off, nil
}

func parseSocksAddrFromPlain(plain []byte) (host, port string, off int, err error) {
	if len(plain) < 1 {
		return "", "", 0, errors.New("short")
	}
	atyp := plain[0]
	off = 1
	switch atyp {
	case 0x01:
		if len(plain) < off+4+2 {
			return "", "", 0, errors.New("short ipv4")
		}
		host = net.IP(plain[off : off+4]).String()
		off += 4
	case 0x03:
		if len(plain) < off+1 {
			return "", "", 0, errors.New("short domain len")
		}
		l := int(plain[off])
		off++
		if len(plain) < off+l+2 {
			return "", "", 0, errors.New("short domain")
		}
		host = string(plain[off : off+l])
		off += l
	case 0x04:
		if len(plain) < off+16+2 {
			return "", "", 0, errors.New("short ipv6")
		}
		host = net.IP(plain[off : off+16]).String()
		off += 16
	default:
		return "", "", 0, errors.New("bad atyp")
	}
	p := binary.BigEndian.Uint16(plain[off : off+2])
	off += 2
	return host, itoa(int(p)), off, nil
}

// ---- WebSocket packet transport as net.PacketConn ----
// One WS binary message = one UDP datagram bytes.

type WSPacketConn struct {
	ctx context.Context
	c   *websocket.Conn
}

func NewWSPacketConn(ctx context.Context, c *websocket.Conn) *WSPacketConn {
	return &WSPacketConn{ctx: ctx, c: c}
}

func (w *WSPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		typ, data, err := w.c.Read(w.ctx)
		if err != nil {
			return 0, nil, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		n := copy(p, data)
		return n, dummyAddr{}, nil
	}
}

func (w *WSPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if err := w.c.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *WSPacketConn) Close() error                       { return nil }
func (w *WSPacketConn) LocalAddr() net.Addr                { return dummyAddr{} }
func (w *WSPacketConn) SetDeadline(t time.Time) error      { return nil }
func (w *WSPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (w *WSPacketConn) SetWriteDeadline(t time.Time) error { return nil }

// dummy addr
type dummyAddr struct{}

func (dummyAddr) Network() string { return "udp" }
func (dummyAddr) String() string  { return "0.0.0.0:0" }
