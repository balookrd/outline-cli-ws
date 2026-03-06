//go:build !unit

package internal

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestReadFrame_ServerMaskedIsProtocolError(t *testing.T) {
	frame, err := buildFrame(WSMessageBinary, []byte("x"), true)
	if err != nil {
		t.Fatalf("buildFrame: %v", err)
	}
	_, _, _, err = readFrame(bufio.NewReader(bytes.NewReader(frame)), false)
	if err == nil || !strings.Contains(err.Error(), "must not be masked") {
		t.Fatalf("expected masked server frame protocol error, got: %v", err)
	}
}

func TestFramedWSConn_UnexpectedContinuationIsProtocolError(t *testing.T) {
	frame, err := buildFrame(WSMessageContinuation, []byte("x"), false)
	if err != nil {
		t.Fatalf("buildFrame: %v", err)
	}
	conn := newFramedWSConn(&rwStub{r: bytes.NewReader(frame), w: io.Discard})
	_, _, err = conn.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected continuation") {
		t.Fatalf("expected unexpected continuation protocol error, got: %v", err)
	}
}

func TestFramedWSConn_ReservedOpcodeIsProtocolError(t *testing.T) {
	// Reserved non-control opcode 0x3.
	wire := []byte{0x83, 0x01, 0x00}
	conn := newFramedWSConn(&rwStub{r: bytes.NewReader(wire), w: io.Discard})
	_, _, err := conn.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reserved opcode") {
		t.Fatalf("expected reserved opcode protocol error, got: %v", err)
	}
}

func TestFramedWSConn_NoWritesAfterCloseSent(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()

	ws := newFramedWSConn(c1)
	defer c1.Close()

	closeErr := make(chan error, 1)
	go func() { closeErr <- ws.Close(WSStatusNormalClosure, "bye") }()

	_ = c2.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	buf := make([]byte, 512)
	n, err := c2.Read(buf)
	if cerr := <-closeErr; cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	if err := ws.Write(context.Background(), WSMessageBinary, []byte("late")); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after close, got: %v", err)
	}
	if err != nil {
		t.Fatalf("peer read close frame: %v", err)
	}
	if n == 0 {
		t.Fatalf("expected close frame bytes")
	}

	_ = c2.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, err = c2.Read(buf)
	if err == nil {
		t.Fatalf("expected no extra writes after close")
	}
}

type rwStub struct {
	r io.Reader
	w io.Writer
}

func (s *rwStub) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *rwStub) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *rwStub) Close() error                { return nil }
