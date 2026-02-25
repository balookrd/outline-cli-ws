package internal

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
)

type dummyAddr struct{}

func (dummyAddr) Network() string { return "udp" }
func (dummyAddr) String() string  { return "0.0.0.0:0" }

func itoa(i int) string { return strconv.Itoa(i) }

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
