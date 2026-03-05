//go:build !unit

package internal

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/quic"
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

func TestH3ConnectHeaders_RFC9220Shape(t *testing.T) {
	u, err := url.Parse("wss://example.com/maiRfy1HEEkssRrSfffYu8/udp?h3=only&origin=https%3A%2F%2Fclient.example")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	headers, err := h3DecodeHeaders(h3ConnectHeaders(u, u.Host))
	if err != nil {
		t.Fatalf("decode headers: %v", err)
	}

	if got := headers[":method"]; got != "CONNECT" {
		t.Fatalf(":method=%q want CONNECT", got)
	}
	if got := headers[":protocol"]; got != "websocket" {
		t.Fatalf(":protocol=%q want websocket", got)
	}
	if got := headers[":path"]; got != "/maiRfy1HEEkssRrSfffYu8/udp?origin=https%3A%2F%2Fclient.example" {
		t.Fatalf(":path=%q", got)
	}
	if got := headers["sec-websocket-key"]; got == "" {
		t.Fatalf("expected sec-websocket-key in RFC9220 CONNECT headers")
	}
	if got := headers["sec-websocket-version"]; got != "13" {
		t.Fatalf("sec-websocket-version=%q want 13", got)
	}
}

func TestH3ParseSettingsPayload_EnableConnectProtocol(t *testing.T) {
	payload := appendVarint(nil, h3SettingEnableConnectProtocol)
	payload = appendVarint(payload, 1)
	payload = appendVarint(payload, 0x6)
	payload = appendVarint(payload, 4096)

	enable, formatted := h3ParseSettingsPayload(payload)
	if !enable {
		t.Fatalf("expected ENABLE_CONNECT_PROTOCOL to be detected")
	}
	if formatted == "" || formatted == "{}" {
		t.Fatalf("unexpected formatted settings: %q", formatted)
	}
}

func TestH3PeerSupportHint_WhenMissingEnableConnectProtocol(t *testing.T) {
	obs := &h3PeerObservations{}
	obs.setSettings(false)
	hint := h3PeerSupportHint(fmt.Errorf("wrap: %w", quic.StreamErrorCode(h3ErrorMessage)), obs)
	if hint == "" {
		t.Fatalf("expected support hint for H3_MESSAGE_ERROR with missing ENABLE_CONNECT_PROTOCOL")
	}
	if !strings.Contains(hint, "remove h3=only") {
		t.Fatalf("expected remediation in hint, got %q", hint)
	}
}

func TestH3PeerSupportHint_WaitsForLateSettingsObservation(t *testing.T) {
	obs := newH3PeerObservations()
	go func() {
		time.Sleep(20 * time.Millisecond)
		obs.setSettings(false)
	}()

	hint := h3PeerSupportHint(fmt.Errorf("wrap: %w", quic.StreamErrorCode(h3ErrorMessage)), obs)
	if hint == "" {
		t.Fatalf("expected support hint after delayed SETTINGS observation")
	}
	if !strings.Contains(hint, "remove h3=only") {
		t.Fatalf("expected remediation in delayed hint, got %q", hint)
	}
}
