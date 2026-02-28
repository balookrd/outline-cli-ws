package internal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestLoadConfigAppliesDefaultsForNonPositiveValues(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	const yamlCfg = `
listen:
  socks5: ""
tun:
  mtu: -1
  udp_max_flows: -10
  udp_idle_timeout: -1s
  udp_gc_interval: -1s
  udp_flow_idle_timeout: -1s
  udp_max_dst_per_port: -5
healthcheck:
  interval: -1s
  timeout: -1s
  fail_threshold: -1
  success_threshold: -1
  min_interval: -1s
  max_interval: -1s
  jitter: -1s
  backoff_factor: -1
  rtt_scale: -1
selection:
  sticky_ttl: -1s
  cooldown: -1s
  min_switch: -1s
  warm_standby_n: -1
  warm_standby_interval: -1s
probe:
  timeout: -1s
`
	if err := os.WriteFile(cfgPath, []byte(yamlCfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	c, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if c.Listen.SOCKS5 != "127.0.0.1:1080" {
		t.Fatalf("SOCKS5 default not applied: %q", c.Listen.SOCKS5)
	}
	if c.Tun.MTU != 1500 || c.Tun.UDPMaxFlows != 4096 || c.Tun.UDPMaxDstPerPort != 512 {
		t.Fatalf("tun int defaults not applied: %+v", c.Tun)
	}
	if c.Tun.UDPIdleTimeout != 60*time.Second || c.Tun.UDPGCInterval != 10*time.Second || c.Tun.UDPFlowIdleTimeout != 30*time.Second {
		t.Fatalf("tun duration defaults not applied: %+v", c.Tun)
	}
	if c.Healthcheck.Interval != 5*time.Second || c.Healthcheck.Timeout != 3*time.Second ||
		c.Healthcheck.MinInterval != 1*time.Second || c.Healthcheck.MaxInterval != 30*time.Second || c.Healthcheck.Jitter != 200*time.Millisecond {
		t.Fatalf("health duration defaults not applied: %+v", c.Healthcheck)
	}
	if c.Healthcheck.FailThreshold != 2 || c.Healthcheck.SuccessThreshold != 1 {
		t.Fatalf("health threshold defaults not applied: %+v", c.Healthcheck)
	}
	if c.Healthcheck.BackoffFactor != 1.6 || c.Healthcheck.RTTScale != 0.25 {
		t.Fatalf("health float defaults not applied: %+v", c.Healthcheck)
	}
	if c.Selection.StickyTTL != 60*time.Second || c.Selection.Cooldown != 20*time.Second || c.Selection.MinSwitch != 20*time.Millisecond {
		t.Fatalf("selection duration defaults not applied: %+v", c.Selection)
	}
	if c.Selection.WarmStandbyN != 2 || c.Selection.WarmStandbyInterval != 2*time.Second {
		t.Fatalf("selection warm standby defaults not applied: %+v", c.Selection)
	}
	if c.Probe.Timeout != 2*time.Second {
		t.Fatalf("probe timeout default not applied: %+v", c.Probe)
	}
}
