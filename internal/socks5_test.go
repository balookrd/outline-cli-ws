package internal

import (
	"net"
	"testing"
)

func TestSocks5Handshake_NoAuthAccepted(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- socks5Handshake(server)
	}()

	// VER=5, NMETHODS=2, METHODS={0x02,0x00}
	_, _ = client.Write([]byte{0x05, 0x02, 0x02, 0x00})

	reply := make([]byte, 2)
	if _, err := client.Read(reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("reply=%#v want [0x05 0x00]", reply)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

func TestSocks5Handshake_NoAcceptableMethod(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- socks5Handshake(server)
	}()

	// VER=5, NMETHODS=1, METHODS={0x02} (no "no-auth")
	_, _ = client.Write([]byte{0x05, 0x01, 0x02})

	reply := make([]byte, 2)
	if _, err := client.Read(reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0xFF {
		t.Fatalf("reply=%#v want [0x05 0xFF]", reply)
	}
	if err := <-errCh; err == nil {
		t.Fatalf("expected error")
	}
}
