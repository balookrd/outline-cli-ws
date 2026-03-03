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

func TestH3QPACKLiteralNameRefUsesStaticBit(t *testing.T) {
	block := h3EncodeHeaders([][2]string{{":path", "/maiRfy1HEEkssRrSfffYu8/tcp"}})
	if len(block) < 3 {
		t.Fatalf("block too short: %d", len(block))
	}
	firstLine := block[2]
	if firstLine&0b1111_0000 != 0b0101_0000 {
		t.Fatalf("expected literal-with-name-reference static form, got first line byte 0x%02x", firstLine)
	}
	h, err := h3DecodeHeaders(block)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h[":path"] != "/maiRfy1HEEkssRrSfffYu8/tcp" {
		t.Fatalf("path: %q", h[":path"])
	}
}
