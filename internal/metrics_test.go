package internal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestTunMetricsExposed(t *testing.T) {
	metricsMu.Lock()
	metrics = telemetry{}
	metricsMu.Unlock()

	EnablePrometheusMetrics()
	observeTunFrame("in", 128)
	observeTunFrame("out", 256)
	observeTunDrop("unknown_l3")
	observeTunError("read")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	metricsHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("metricsHandler status=%d want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"outlinews_tun_packets_total{dir=\"in\"} 1",
		"outlinews_tun_packets_total{dir=\"out\"} 1",
		"outlinews_tun_bytes_total{dir=\"in\"} 128",
		"outlinews_tun_bytes_total{dir=\"out\"} 256",
		"outlinews_tun_drops_total{reason=\"unknown_l3\"} 1",
		"outlinews_tun_errors_total{op=\"read\"} 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q\nbody:\n%s", want, body)
		}
	}
}
