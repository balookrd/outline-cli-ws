package internal

import (
	"net/url"
	"testing"
)

func TestParseTransportHints(t *testing.T) {
	q := url.Values{}
	q.Set("h2", "1")
	q.Set("http3", "1")

	tryH2, h2Only, tryH3, h3Only := parseTransportHints(q)
	if !tryH2 || h2Only || !tryH3 || h3Only {
		t.Fatalf("unexpected hint parse: tryH2=%v h2Only=%v tryH3=%v h3Only=%v", tryH2, h2Only, tryH3, h3Only)
	}
}

func TestParseTransportHintsOnlyModes(t *testing.T) {
	q := url.Values{}
	q.Set("h2", "only")
	q.Set("quic", "only")

	tryH2, h2Only, tryH3, h3Only := parseTransportHints(q)
	if tryH2 || !h2Only || tryH3 || !h3Only {
		t.Fatalf("unexpected only-hint parse: tryH2=%v h2Only=%v tryH3=%v h3Only=%v", tryH2, h2Only, tryH3, h3Only)
	}
}

func TestIsWebSocketLikeScheme(t *testing.T) {
	for _, tc := range []struct {
		scheme string
		ok     bool
	}{
		{scheme: "ws", ok: true},
		{scheme: "wss", ok: true},
		{scheme: "http", ok: true},
		{scheme: "https", ok: true},
		{scheme: "tcp", ok: false},
	} {
		if got := isWebSocketLikeScheme(tc.scheme); got != tc.ok {
			t.Fatalf("scheme %q: got %v want %v", tc.scheme, got, tc.ok)
		}
	}
}
