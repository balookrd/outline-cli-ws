package internal

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
)

func itoa(i int) string { return strconv.Itoa(i) }

// parseSocksAddrAt parses a SOCKS address that starts at b[off] (ATYP byte).
// Returns host, port, and the offset of the first byte AFTER DST.PORT.
func parseSocksAddrAt(b []byte, off int) (host, port string, newOff int, err error) {
	if len(b) < off+1 {
		return "", "", 0, errors.New("short")
	}
	atyp := b[off]
	off++
	switch atyp {
	case 0x01: // IPv4
		if len(b) < off+4+2 {
			return "", "", 0, errors.New("short ipv4")
		}
		host = net.IP(b[off : off+4]).String()
		off += 4
	case 0x03: // domain
		if len(b) < off+1 {
			return "", "", 0, errors.New("short domain len")
		}
		l := int(b[off])
		off++
		if len(b) < off+l+2 {
			return "", "", 0, errors.New("short domain")
		}
		host = string(b[off : off+l])
		off += l
	case 0x04: // IPv6
		if len(b) < off+16+2 {
			return "", "", 0, errors.New("short ipv6")
		}
		host = net.IP(b[off : off+16]).String()
		off += 16
	default:
		return "", "", 0, errors.New("bad atyp")
	}
	p := binary.BigEndian.Uint16(b[off : off+2])
	off += 2
	return host, itoa(int(p)), off, nil
}

// parseSocksAddrFromPlain parses Shadowsocks UDP plaintext: [ATYP][ADDR][PORT][DATA]
// and returns the addr and the offset of DATA.
func parseSocksAddrFromPlain(plain []byte) (host, port string, off int, err error) {
	return parseSocksAddrAt(plain, 0)
}
