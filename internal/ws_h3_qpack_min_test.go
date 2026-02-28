package internal

import "testing"

func TestH3QPACKEncodeDecode(t *testing.T) {
	block := h3EncodeHeaders([][2]string{{":status", "200"}, {"sec-websocket-accept", "abc"}})
	h, err := h3DecodeHeaders(block)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h[":status"] != "200" {
		t.Fatalf("status: %q", h[":status"])
	}
	if h["sec-websocket-accept"] != "abc" {
		t.Fatalf("accept: %q", h["sec-websocket-accept"])
	}
}
