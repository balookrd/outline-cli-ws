package internal

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestParseSocksAddrAt_IPv4(t *testing.T) {
	b := []byte{0x01, 1, 2, 3, 4, 0, 53}
	h, p, off, err := parseSocksAddrAt(b, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != "1.2.3.4" || p != "53" {
		t.Fatalf("got %q:%q", h, p)
	}
	if off != len(b) {
		t.Fatalf("off=%d want %d", off, len(b))
	}
}

func TestParseSocksAddrAt_Domain(t *testing.T) {
	d := "example.com"
	b := append([]byte{0x03, byte(len(d))}, []byte(d)...)
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, 443)
	b = append(b, pb...)

	h, p, off, err := parseSocksAddrAt(b, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != d || p != "443" {
		t.Fatalf("got %q:%q", h, p)
	}
	if off != len(b) {
		t.Fatalf("off=%d want %d", off, len(b))
	}
}

func TestParseSocksAddrAt_IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1").To16()
	if ip == nil {
		t.Fatal("bad test ip")
	}
	b := append([]byte{0x04}, ip...)
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, 8080)
	b = append(b, pb...)

	h, p, off, err := parseSocksAddrAt(b, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != "2001:db8::1" || p != "8080" {
		t.Fatalf("got %q:%q", h, p)
	}
	if off != len(b) {
		t.Fatalf("off=%d want %d", off, len(b))
	}
}

func TestParseSocksAddrAt_Errors(t *testing.T) {
	if _, _, _, err := parseSocksAddrAt([]byte{}, 0); err == nil {
		t.Fatal("expected error")
	}
	if _, _, _, err := parseSocksAddrAt([]byte{0x09, 0, 0}, 0); err == nil {
		t.Fatal("expected error")
	}
}
