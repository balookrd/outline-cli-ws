//go:build unit

package internal

import "time"

// Minimal config/types for unit-test build (no external deps).

type UpstreamConfig struct {
	Name   string
	Weight float64

	TCPWSS string
	UDPWSS string
	Cipher string
	Secret string
}

type HealthcheckConfig struct {
	Interval         time.Duration
	Timeout          time.Duration
	Jitter           time.Duration
	MinInterval      time.Duration
	MaxInterval      time.Duration
	BackoffFactor    float64
	FailThreshold    int
	SuccessThreshold int
	RTTScale         float64
}

type SelectionConfig struct {
	StickyTTL                    time.Duration
	MinSwitch                    time.Duration
	Cooldown                     time.Duration
	WarmStandbyN                 int
	WarmStandbyInterval          time.Duration
	StandbyKeepalive             bool
	StandbyKeepaliveInterval     time.Duration
	StandbyKeepaliveProbeTimeout time.Duration
}

type ProbeConfig struct {
	EnableTCP bool
	EnableUDP bool
	Timeout   time.Duration
	TCPTarget string
	UDPTarget string
	DNSName   string
	DNSType   string
}

type TunConfig struct {
	Device string

	UDPMaxFlows        int
	UDPIdleTimeout     time.Duration
	UDPFlowIdleTimeout time.Duration
}

type WebSocketConfig struct {
	Debug bool
}

type Config struct {
	Upstreams     []UpstreamConfig
	Healthcheck   HealthcheckConfig
	Selection     SelectionConfig
	Probe         ProbeConfig
	DisableProbes bool
	Fwmark        uint32
	Tun           TunConfig
	WebSocket     WebSocketConfig
	Socks5Listen  string
}
