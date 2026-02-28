package outlinews

// Package outlinews provides a small public surface for reusing this repository as a library.
// The implementation lives in internal/ and may change without notice.

import (
	"context"

	"outline-cli-ws/internal"
)

// --- Config ---

type Config = internal.Config

type UpstreamConfig = internal.UpstreamConfig

type HealthcheckConfig = internal.HealthcheckConfig

type SelectionConfig = internal.SelectionConfig

type ProbeConfig = internal.ProbeConfig

type TunConfig = internal.TunConfig

// LoadConfig loads YAML configuration file.
// Note: internal.LoadConfig returns a pointer.
func LoadConfig(path string) (*Config, error) { return internal.LoadConfig(path) }

// --- Core runtime ---

type LoadBalancer = internal.LoadBalancer

// NewLoadBalancer creates a new load balancer.
// fwmark is a Linux socket fwmark value (0 disables).
func NewLoadBalancer(upstreams []UpstreamConfig, hc HealthcheckConfig, sel SelectionConfig, probe ProbeConfig, fwmark uint32) *LoadBalancer {
	return internal.NewLoadBalancer(upstreams, hc, sel, probe, fwmark)
}

// --- SOCKS5 server ---

type Socks5Server = internal.Socks5Server

// --- TUN ---

func RunTunNative(ctx context.Context, cfg TunConfig, lb *LoadBalancer) error {
	return internal.RunTunNative(ctx, cfg, lb)
}

// EnablePrometheusMetrics registers and enables default Prometheus metrics.
func EnablePrometheusMetrics() {
	internal.EnablePrometheusMetrics()
}

// StartMetricsServer serves /metrics on the provided address until context cancellation.
func StartMetricsServer(ctx context.Context, addr string) error {
	return internal.StartMetricsServer(ctx, addr)
}
