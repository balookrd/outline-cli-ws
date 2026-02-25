package internal

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"time"

	"nhooyr.io/websocket"
)

type Socks5Server struct {
	LB *LoadBalancer
}

func (s *Socks5Server) HandleConn(ctx context.Context, c net.Conn) {
	defer c.Close()

	// handshake
	if err := socks5Handshake(c); err != nil {
		log.Printf("socks handshake: %v", err)
		return
	}
	_ = c.SetDeadline(time.Time{})

	// request
	cmd, dst, err := socks5ReadRequest(c)
	if err != nil {
		log.Printf("socks req: %v", err)
		return
	}

	switch cmd {
	case 0x01: // CONNECT
		s.handleConnect(ctx, c, dst)
	case 0x03: // UDP ASSOCIATE
		s.handleUDPAssociate(ctx, c)
	default:
		_ = socks5Reply(c, 0x07, "0.0.0.0:0") // Command not supported
	}
}

func (s *Socks5Server) handleConnect(ctx context.Context, c net.Conn, dst string) {
	up, err := s.LB.PickTCP()
	if err != nil {
		s.LB.ReportTCPFailure(up, err)
		_ = socks5Reply(c, 0x04, "0.0.0.0:0") // Host unreachable
		return
	}

	// Open WS stream to upstream TCP endpoint
	wsc, err := s.LB.AcquireTCPWS(ctx, up)
	if err != nil {
		s.LB.ReportTCPFailure(up, err)
		_ = socks5Reply(c, 0x04, "0.0.0.0:0")
		return
	}
	defer wsc.Close(websocket.StatusNormalClosure, "close")

	// Reply success (bound addr can be 0.0.0.0:0 for our proxy)
	if err := socks5Reply(c, 0x00, "0.0.0.0:0"); err != nil {
		return
	}

	// Tunnel: local TCP <-> Shadowsocks-over-WS
	err = ProxyTCPOverOutlineWS(ctx, c, wsc, up.cfg, dst)
	if err != nil && !errors.Is(err, io.EOF) {
		s.LB.ReportTCPFailure(up, err)
		log.Printf("tcp tunnel err: %v", err)
	}
}

func (s *Socks5Server) handleUDPAssociate(ctx context.Context, c net.Conn) {
	up, err := s.LB.PickUDP()
	if err != nil {
		s.LB.ReportUDPFailure(up, err)
		_ = socks5Reply(c, 0x04, "0.0.0.0:0")
		return
	}

	assoc, err := NewUDPAssociation(ctx, up.cfg, s.LB.fwmark)
	if err != nil {
		s.LB.ReportUDPFailure(up, err)
		_ = socks5Reply(c, 0x04, "0.0.0.0:0")
		return
	}
	defer assoc.Close()

	// tell client where to send UDP packets
	relayAddr := assoc.LocalAddr().String()
	if err := socks5Reply(c, 0x00, relayAddr); err != nil {
		return
	}

	// keep TCP control connection open until client closes it
	_, _ = io.Copy(io.Discard, c)
}

// ---- minimal SOCKS5 helpers ----

func socks5Handshake(c net.Conn) error {
	h := make([]byte, 2)
	if _, err := io.ReadFull(c, h); err != nil {
		return err
	}
	if h[0] != 0x05 {
		return errors.New("not socks5")
	}
	nMethods := int(h[1])
	m := make([]byte, nMethods)
	if _, err := io.ReadFull(c, m); err != nil {
		return err
	}
	// no-auth
	_, err := c.Write([]byte{0x05, 0x00})
	return err
}

func socks5ReadRequest(c net.Conn) (cmd byte, dst string, err error) {
	h := make([]byte, 4)
	if _, err = io.ReadFull(c, h); err != nil {
		return
	}
	if h[0] != 0x05 {
		err = errors.New("bad ver")
		return
	}
	cmd = h[1]
	atyp := h[3]

	host, port, err := readAddrPort(c, atyp)
	if err != nil {
		return 0, "", err
	}
	return cmd, net.JoinHostPort(host, port), nil
}

func socks5Reply(c net.Conn, rep byte, bind string) error {
	host, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		host = "0.0.0.0"
		portStr = "0"
	}

	port := uint16(0)
	if p, e := net.LookupPort("tcp", portStr); e == nil {
		port = uint16(p)
	}

	ip := net.ParseIP(host)

	var atyp byte
	var addr []byte

	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			atyp = 0x01
			addr = ip4
		} else {
			atyp = 0x04
			addr = ip.To16()
		}
	} else {
		atyp = 0x03
		addr = append([]byte{byte(len(host))}, []byte(host)...)
	}

	b := []byte{0x05, rep, 0x00, atyp}
	b = append(b, addr...)

	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, port)
	b = append(b, pb...)

	_, err = c.Write(b)
	return err
}

func readAddrPort(r io.Reader, atyp byte) (host, port string, err error) {
	switch atyp {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err = io.ReadFull(r, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 0x03: // Domain
		l := make([]byte, 1)
		if _, err = io.ReadFull(r, l); err != nil {
			return
		}
		b := make([]byte, int(l[0]))
		if _, err = io.ReadFull(r, b); err != nil {
			return
		}
		host = string(b)
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err = io.ReadFull(r, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return "", "", errors.New("bad atyp")
	}
	pb := make([]byte, 2)
	if _, err = io.ReadFull(r, pb); err != nil {
		return
	}
	port = itoa(int(binary.BigEndian.Uint16(pb)))
	return
}

func itoa(i int) string {
	// tiny itoa, avoids strconv for brevity
	if i == 0 {
		return "0"
	}
	var b [6]byte
	n := 0
	for i > 0 {
		b[len(b)-1-n] = byte('0' + (i % 10))
		i /= 10
		n++
	}
	return string(b[len(b)-n:])
}
