package internal

import (
	"context"
	"net"
	"sync"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
)

type UDPAssociation struct {
	ctx    context.Context
	cancel context.CancelFunc

	uc  net.PacketConn // local UDP relay for SOCKS5 client
	wsc WSConn

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
		_ = uc.Close()
		cancel()
		return nil, err
	}

	ciph, err := core.PickCipher(up.Cipher, nil, up.Secret)
	if err != nil {
		_ = wsc.Close(WSStatusNormalClosure, "close")
		_ = uc.Close()
		cancel()
		return nil, err
	}

	// Underlying packet transport: WS binary message <-> datagram bytes
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
	_ = a.uc.Close()
	_ = a.enc.Close()
	_ = a.wsc.Close(WSStatusNormalClosure, "")
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

		// Parse DST.ADDR/DST.PORT starting at ATYP (pkt[3]).
		dstHost, dstPort, off, err := parseSocksAddrAt(pkt, 3)
		if err != nil {
			continue
		}

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

		// SOCKS5 UDP response: RSV(2)=0, FRAG=0, then [socks addr], then DATA
		resp := make([]byte, 0, 3+len(plain))
		resp = append(resp, 0x00, 0x00, 0x00)
		resp = append(resp, plain[:off]...)
		resp = append(resp, plain[off:]...)

		a.mu.Lock()
		peer := a.peerUDP
		a.mu.Unlock()
		if peer == nil {
			continue
		}
		_, _ = a.uc.WriteTo(resp, peer)
	}
}
