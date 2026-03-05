package internal

import (
	"net/url"
	"testing"
)

func TestParseTransportHints(t *testing.T) {
	q := url.Values{}
	q.Set("h2", "1")
	q.Set("http3", "1")

	tryH2, h2Only, tryH3, h3Only, connectOnly := parseTransportHints(q)
	if !tryH2 || h2Only || !tryH3 || h3Only || connectOnly {
		t.Fatalf("unexpected hint parse: tryH2=%v h2Only=%v tryH3=%v h3Only=%v connectOnly=%v", tryH2, h2Only, tryH3, h3Only, connectOnly)
	}
}

func TestParseTransportHintsOnlyModes(t *testing.T) {
	q := url.Values{}
	q.Set("h2", "only")
	q.Set("quic", "only")

	tryH2, h2Only, tryH3, h3Only, connectOnly := parseTransportHints(q)
	if !tryH2 || !h2Only || !tryH3 || !h3Only || !connectOnly {
		t.Fatalf("unexpected only-hint parse: tryH2=%v h2Only=%v tryH3=%v h3Only=%v connectOnly=%v", tryH2, h2Only, tryH3, h3Only, connectOnly)
	}
}

func TestParseTransportHintsH3OnlyWithoutTryFlag(t *testing.T) {
	q := url.Values{}
	q.Set("h3", "only")

	_, _, tryH3, h3Only, connectOnly := parseTransportHints(q)
	if !tryH3 || !h3Only || !connectOnly {
		t.Fatalf("unexpected h3-only parse: tryH3=%v h3Only=%v connectOnly=%v", tryH3, h3Only, connectOnly)
	}
}

func TestParseTransportHintsConnectOnly(t *testing.T) {
	q := url.Values{}
	q.Set("connect", "only")

	_, _, _, _, connectOnly := parseTransportHints(q)
	if !connectOnly {
		t.Fatalf("expected connectOnly hint")
	}
}

func TestParseTransportHintsRFCModes(t *testing.T) {
	q := url.Values{}
	q.Set("rfc8441", "1")
	q.Set("rfc9220", "1")

	tryH2, h2Only, tryH3, h3Only, connectOnly := parseTransportHints(q)
	if !tryH2 || h2Only || !tryH3 || h3Only || connectOnly {
		t.Fatalf("unexpected rfc hint parse: tryH2=%v h2Only=%v tryH3=%v h3Only=%v connectOnly=%v", tryH2, h2Only, tryH3, h3Only, connectOnly)
	}
}

func TestParseTransportHintsRFCOnlyModes(t *testing.T) {
	q := url.Values{}
	q.Set("rfc8441", "only")
	q.Set("rfc9220", "only")

	tryH2, h2Only, tryH3, h3Only, connectOnly := parseTransportHints(q)
	if !tryH2 || !h2Only || !tryH3 || !h3Only || !connectOnly {
		t.Fatalf("unexpected rfc only parse: tryH2=%v h2Only=%v tryH3=%v h3Only=%v connectOnly=%v", tryH2, h2Only, tryH3, h3Only, connectOnly)
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
