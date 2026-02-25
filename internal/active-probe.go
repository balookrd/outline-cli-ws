package internal

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"nhooyr.io/websocket"
)

// ---- TCP Quality Probe: HTTP HEAD ----

func ProbeTCPQuality(ctx context.Context, up UpstreamConfig, target string) (time.Duration, error) {
	start := time.Now()

	ciph, err := core.PickCipher(up.Cipher, nil, up.Secret)
	if err != nil {
		return 0, err
	}

	wsc, err := DialWSStream(ctx, up.TCPWSS)
	if err != nil {
		return 0, err
	}
	defer wsc.Close(websocket.StatusNormalClosure, "tcp-probe")

	wsconn := NewWSStreamConn(ctx, wsc)
	ssconn := ciph.StreamConn(wsconn)
	defer ssconn.Close()

	tgt := socks.ParseAddr(target)
	if tgt == nil {
		return 0, socks.ErrAddressNotSupported
	}
	if _, err := ssconn.Write(tgt); err != nil {
		return 0, err
	}

	host := target
	if h, _, e := net.SplitHostPort(target); e == nil {
		host = h
	}
	req := "HEAD / HTTP/1.1\r\nHost: " + host + "\r\nConnection: close\r\n\r\n"
	if _, err := ssconn.Write([]byte(req)); err != nil {
		return 0, err
	}

	// read a bit; should start with "HTTP/"
	buf := make([]byte, 16)
	n, err := io.ReadAtLeast(ssconn, buf, 5)
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(string(buf[:n]), "HTTP/") {
		return 0, errors.New("tcp probe: unexpected response")
	}

	return time.Since(start), nil
}

// ---- UDP Quality Probe: DNS query ----

func ProbeUDPQuality(ctx context.Context, up UpstreamConfig, dnsServer string, name string) (time.Duration, error) {
	start := time.Now()

	ciph, err := core.PickCipher(up.Cipher, nil, up.Secret)
	if err != nil {
		return 0, err
	}

	wsc, err := DialWSStream(ctx, up.UDPWSS)
	if err != nil {
		return 0, err
	}
	defer wsc.Close(websocket.StatusNormalClosure, "udp-probe")

	// Underlying WS packet transport
	wsPC := NewWSPacketConn(ctx, wsc)
	encPC := ciph.PacketConn(wsPC)
	defer encPC.Close()

	// Build DNS query (A)
	txid := uint16(time.Now().UnixNano()) // not crypto, fine for probe
	q := buildDNSQuery(txid, name)

	// SS UDP plaintext = [socks addr][dns query]
	dst := socks.ParseAddr(dnsServer)
	if dst == nil {
		return 0, socks.ErrAddressNotSupported
	}
	plain := append(dst, q...)

	if _, err := encPC.WriteTo(plain, dummyAddr{}); err != nil {
		return 0, err
	}

	// read response (plain) with same txid
	buf := make([]byte, 1500)
	for {
		n, _, err := encPC.ReadFrom(buf)
		if err != nil {
			return 0, err
		}
		p := buf[:n]

		// parse socks addr header length, then DNS
		_, _, off, err := parseSocksAddrFromPlain(p)
		if err != nil || off >= len(p) {
			continue
		}
		dns := p[off:]
		if len(dns) < 12 {
			continue
		}
		rxid := binary.BigEndian.Uint16(dns[0:2])
		flags := binary.BigEndian.Uint16(dns[2:4])
		qr := (flags >> 15) & 1
		if rxid == txid && qr == 1 {
			return time.Since(start), nil
		}
		// иначе это не наш ответ — продолжим (маловероятно)
	}
}

func buildDNSQuery(txid uint16, name string) []byte {
	// DNS header: ID, flags(0x0100), QD=1
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], txid)
	binary.BigEndian.PutUint16(b[2:4], 0x0100)
	binary.BigEndian.PutUint16(b[4:6], 1)

	// QNAME
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")
	for _, lab := range labels {
		if len(lab) == 0 || len(lab) > 63 {
			continue
		}
		b = append(b, byte(len(lab)))
		b = append(b, []byte(lab)...)
	}
	b = append(b, 0x00)

	// QTYPE=A(1), QCLASS=IN(1)
	b = append(b, 0x00, 0x01, 0x00, 0x01)
	return b
}
