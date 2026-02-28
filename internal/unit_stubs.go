//go:build unit

package internal

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrNotImplemented = errors.New("not implemented in unit build")

// LoadConfig is disabled in unit build (YAML parser requires external deps).
func LoadConfig(path string) (*Config, error) { return nil, ErrNotImplemented }

// ProbeTCPQuality/ProbeUDPQuality are disabled in unit build (need external deps).
func ProbeTCPQuality(ctx context.Context, up UpstreamConfig, target string, fwmark uint32) (time.Duration, error) {
	return 0, ErrNotImplemented
}
func ProbeUDPQuality(ctx context.Context, up UpstreamConfig, target string, dnsName string, dnsType string, fwmark uint32) (time.Duration, error) {
	return 0, ErrNotImplemented
}

// RunTunNative is disabled in unit build (requires TUN + gVisor).
func RunTunNative(ctx context.Context, cfg TunConfig, lb *LoadBalancer) error {
	return ErrNotImplemented
}

// --- Shadowsocks stream adapter is disabled in unit build.
func newSSTCPConn(ctx context.Context, wsc WSConn, up UpstreamConfig, dst string) (net.Conn, error) {
	return nil, ErrNotImplemented
}

// --- UDP association is disabled in unit build.
type UDPAssociation struct{ addr net.Addr }

func (a *UDPAssociation) Close() error { return nil }
func (a *UDPAssociation) LocalAddr() net.Addr {
	if a.addr == nil {
		a.addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	}
	return a.addr
}
func NewUDPAssociation(ctx context.Context, up UpstreamConfig, fwmark uint32) (*UDPAssociation, error) {
	return &UDPAssociation{}, nil
}

// --- Websocket dialers are disabled in unit build.
func dialCoderWebSocket(ctx context.Context, rawurl string, tr *http.Transport) (WSConn, error) {
	return nil, ErrNotImplemented
}

func dialRFC8441RawH2(ctx context.Context, u *url.URL, tr *http.Transport) (WSConn, error) {
	return nil, ErrNotImplemented
}

func dialRFC9220(ctx context.Context, u *url.URL) (WSConn, error) {
	return nil, ErrNotImplemented
}

// rfc8441Debug is used for verbose logging in ws_h2.go.
var rfc8441Debug = false

// normalizeHostPort normalizes IPv6 host:port by adding brackets if missing.
// It is used by config_test.go; the production implementation lives in config.go.
func normalizeHostPort(s string) string {
	if s == "" {
		return s
	}
	// Already valid host:port (IPv4, hostname, or bracketed IPv6).
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}

	// Try to treat as "IPv6WithoutBrackets:port" by bracketing everything
	// before the last ':' and verifying with net.SplitHostPort.
	last := strings.LastIndexByte(s, ':')
	if last > 0 && last < len(s)-1 {
		host := s[:last]
		// If host ends with ':', the input likely contains '::' right before the last segment,
		// and the trailing segment is probably part of the IPv6 address (not a port).
		if strings.HasSuffix(host, ":") {
			return s
		}
		port := s[last+1:]
		candidate := "[" + host + "]:" + port
		if _, _, err := net.SplitHostPort(candidate); err == nil {
			return candidate
		}
	}

	// Pure IP literal without port.
	if _, err := netip.ParseAddr(s); err == nil {
		return s
	}
	return s
}
