//go:build !unit

package internal

import (
	"os"
	"path/filepath"
	"testing"
)

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
