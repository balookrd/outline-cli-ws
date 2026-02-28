package internal

import (
	"errors"
	"net/url"
	"testing"
)

func TestFailureReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("i/o timeout"), "timeout"},
		{errors.New("x509: certificate signed by unknown authority"), "tls"},
		{errors.New("lookup host: no such host"), "dns"},
		{errors.New("connection refused"), "refused"},
		{errors.New("boom"), "other"},
		{nil, "unknown"},
	}

	for _, tc := range cases {
		if got := failureReason(tc.err); got != tc.want {
			t.Fatalf("failureReason(%v)=%q want %q", tc.err, got, tc.want)
		}
	}
}

func TestUpstreamFromURL(t *testing.T) {
	u, err := url.Parse("wss://example.com/udp/path?h2=1")
	if err != nil {
		t.Fatal(err)
	}
	name, proto := upstreamFromURL(u)
	if name != "example.com" || proto != "udp" {
		t.Fatalf("got name=%q proto=%q", name, proto)
	}

	u2, err := url.Parse("wss://edge.local/tcp")
	if err != nil {
		t.Fatal(err)
	}
	name, proto = upstreamFromURL(u2)
	if name != "edge.local" || proto != "tcp" {
		t.Fatalf("got name=%q proto=%q", name, proto)
	}
}

func TestToPromLabels(t *testing.T) {
	got := toPromLabels("upstream=edge-1,proto=tcp,reason=timeout")
	want := "upstream=\"edge-1\",proto=\"tcp\",reason=\"timeout\""
	if got != want {
		t.Fatalf("toPromLabels=%q want %q", got, want)
	}
}
