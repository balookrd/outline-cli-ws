package internal

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestH3ReadResponseHeadersFromQUICFrames(t *testing.T) {
	settingsPayload := []byte{0x01, 0x00}
	headersPayload := h3EncodeHeaders([][2]string{{":status", "200"}, {"sec-websocket-accept", "test-accept"}})

	wire := make([]byte, 0, 64)
	wire = appendVarint(wire, h3FrameSettings)
	wire = appendVarint(wire, uint64(len(settingsPayload)))
	wire = append(wire, settingsPayload...)
	wire = appendVarint(wire, h3FrameHeaders)
	wire = appendVarint(wire, uint64(len(headersPayload)))
	wire = append(wire, headersPayload...)

	headers, err := h3ReadResponseHeaders(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("h3ReadResponseHeaders: %v", err)
	}
	if got := headers[":status"]; got != "200" {
		t.Fatalf("unexpected :status header: got %q", got)
	}
	if got := headers["sec-websocket-accept"]; got != "test-accept" {
		t.Fatalf("unexpected sec-websocket-accept header: got %q", got)
	}
}

func TestQUICVarintRoundTripForHTTP3Frames(t *testing.T) {
	values := []uint64{0, 63, 64, 15293, 16383, 16384, 1<<30 - 1}
	for _, v := range values {
		enc := appendVarint(nil, v)
		got, err := readVarint(bytes.NewReader(enc))
		if err != nil {
			t.Fatalf("readVarint(%d): %v", v, err)
		}
		if got != v {
			t.Fatalf("varint roundtrip mismatch: got %d want %d", got, v)
		}
	}
}

func TestRFC9220ClientToWSAndBack(t *testing.T) {
	// net.Pipe emulates a bidirectional QUIC stream that RFC9220 upgrades into WS data.
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	clientWS := newFramedWSConn(clientSide)
	serverWS := newFramedWSConn(serverSide)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		typ, payload, err := serverWS.Read(ctx)
		if err != nil {
			serverErr <- err
			return
		}
		if typ != WSMessageBinary {
			serverErr <- fmt.Errorf("unexpected ws type: %v", typ)
			return
		}
		if string(payload) != "client->ws" {
			serverErr <- fmt.Errorf("unexpected payload: %q", payload)
			return
		}
		serverErr <- serverWS.Write(ctx, WSMessageBinary, []byte("ws->client"))
	}()

	if err := clientWS.Write(ctx, WSMessageBinary, []byte("client->ws")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	typ, payload, err := clientWS.Read(ctx)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if typ != WSMessageBinary {
		t.Fatalf("unexpected ws type: %v", typ)
	}
	if string(payload) != "ws->client" {
		t.Fatalf("unexpected response payload: %q", payload)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server flow failed: %v", err)
	}
}
