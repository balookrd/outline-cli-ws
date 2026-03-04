package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeHostPort(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1.1.1.1:53", "1.1.1.1:53"},
		{"example.com:53", "example.com:53"},
		{"[2606:4700:4700::1111]:53", "[2606:4700:4700::1111]:53"},
		// IPv6 literal without brackets should be normalized.
		{"2606:4700:4700::1111:53", "[2606:4700:4700::1111]:53"},
		// No port: cannot normalize.
		{"2606:4700:4700::1111", "2606:4700:4700::1111"},
		// Empty port: cannot normalize.
		{"2606:4700:4700::1111:", "2606:4700:4700::1111:"},
	}

	for _, tc := range cases {
		got := normalizeHostPort(tc.in)
		if got != tc.want {
			t.Fatalf("normalizeHostPort(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoadConfig_AllowsDebugFlags(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configYAML := `websocket:
  debug: true
tun:
  debug: true
upstreams:
  - name: edge-1
    tcp_wss: wss://example.com/tcp
    udp_wss: wss://example.com/udp
    cipher: chacha20-ietf-poly1305
    secret: test-secret
`

	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if !cfg.WebSocket.Debug {
		t.Fatalf("expected websocket.debug=true")
	}
	if !cfg.Tun.Debug {
		t.Fatalf("expected tun.debug=true")
	}
}
