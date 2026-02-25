package internal

import (
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen struct {
		SOCKS5 string `yaml:"socks5"`
	} `yaml:"listen"`
	Tun         TunConfig         `yaml:"tun"`
	Healthcheck HealthcheckConfig `yaml:"healthcheck"`
	Selection   SelectionConfig   `yaml:"selection"`
	Upstreams   []UpstreamConfig  `yaml:"upstreams"`
	Probe       ProbeConfig       `yaml:"probe"`
	Fwmark      uint32            `yaml:"fwmark"` // 0 = disabled
}

type TunConfig struct {
	Enable bool   `yaml:"enable"`
	Device string `yaml:"device"`
	MTU    int    `yaml:"mtu"`
	// Native UDP flow table tuning
	UDPMaxFlows    int           `yaml:"udp_max_flows"`    // e.g. 4096
	UDPIdleTimeout time.Duration `yaml:"udp_idle_timeout"` // e.g. 60s
	UDPGCInterval  time.Duration `yaml:"udp_gc_interval"`  // e.g. 10s
}

type HealthcheckConfig struct {
	Interval         time.Duration `yaml:"interval"` // базовый (как раньше)
	Timeout          time.Duration `yaml:"timeout"`
	FailThreshold    int           `yaml:"fail_threshold"`
	SuccessThreshold int           `yaml:"success_threshold"`

	MinInterval   time.Duration `yaml:"min_interval"`   // минимум (для DOWN/подозрительных)
	MaxInterval   time.Duration `yaml:"max_interval"`   // максимум (для стабильных UP)
	Jitter        time.Duration `yaml:"jitter"`         // +- случайный сдвиг
	BackoffFactor float64       `yaml:"backoff_factor"` // рост интервала на фейлах (например 1.6)
	RTTScale      float64       `yaml:"rtt_scale"`      // добавка от RTT (например 0.25)
}

type SelectionConfig struct {
	StickyTTL time.Duration `yaml:"sticky_ttl"`
	Cooldown  time.Duration `yaml:"cooldown"`
	MinSwitch time.Duration `yaml:"min_switch"`

	WarmStandbyN        int           `yaml:"warm_standby_n"`        // сколько апстримов держать прогретыми (1-2)
	WarmStandbyInterval time.Duration `yaml:"warm_standby_interval"` // как часто проверять/догревать
}

type UpstreamConfig struct {
	Name   string `yaml:"name"`
	Weight int    `yaml:"weight"`

	TCPWSS string `yaml:"tcp_wss"`
	UDPWSS string `yaml:"udp_wss"`

	Cipher string `yaml:"cipher"`
	Secret string `yaml:"secret"`
}

type ProbeConfig struct {
	EnableTCP bool `yaml:"enable_tcp"`
	EnableUDP bool `yaml:"enable_udp"`

	Timeout time.Duration `yaml:"timeout"`

	TCPTarget string `yaml:"tcp_target"` // e.g. "example.com:80"
	UDPTarget string `yaml:"udp_target"` // e.g. "1.1.1.1:53"
	DNSName   string `yaml:"dns_name"`   // e.g. "example.com"
	DNSType   string `yaml:"dns_type"`   // "A" или "AAAA"
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Listen.SOCKS5 == "" {
		c.Listen.SOCKS5 = "127.0.0.1:1080"
	}
	if c.Tun.MTU == 0 {
		c.Tun.MTU = 1500
	}
	if c.Tun.UDPMaxFlows == 0 {
		c.Tun.UDPMaxFlows = 4096
	}
	if c.Tun.UDPIdleTimeout == 0 {
		c.Tun.UDPIdleTimeout = 60 * time.Second
	}
	if c.Tun.UDPGCInterval == 0 {
		c.Tun.UDPGCInterval = 10 * time.Second
	}
	if c.Healthcheck.Interval == 0 {
		c.Healthcheck.Interval = 5 * time.Second
	}
	if c.Healthcheck.Timeout == 0 {
		c.Healthcheck.Timeout = 3 * time.Second
	}
	if c.Healthcheck.FailThreshold == 0 {
		c.Healthcheck.FailThreshold = 2
	}
	if c.Healthcheck.SuccessThreshold == 0 {
		c.Healthcheck.SuccessThreshold = 1
	}
	if c.Healthcheck.MinInterval == 0 {
		c.Healthcheck.MinInterval = 1 * time.Second
	}
	if c.Healthcheck.MaxInterval == 0 {
		c.Healthcheck.MaxInterval = 30 * time.Second
	}
	if c.Healthcheck.Jitter == 0 {
		c.Healthcheck.Jitter = 200 * time.Millisecond
	}
	if c.Healthcheck.BackoffFactor == 0 {
		c.Healthcheck.BackoffFactor = 1.6
	}
	if c.Healthcheck.RTTScale == 0 {
		c.Healthcheck.RTTScale = 0.25
	}
	if c.Selection.StickyTTL == 0 {
		c.Selection.StickyTTL = 60 * time.Second
	}
	if c.Selection.Cooldown == 0 {
		c.Selection.Cooldown = 20 * time.Second
	}
	if c.Selection.MinSwitch == 0 {
		c.Selection.MinSwitch = 20 * time.Millisecond
	}
	if c.Selection.WarmStandbyN == 0 {
		c.Selection.WarmStandbyN = 2
	}
	if c.Selection.WarmStandbyInterval == 0 {
		c.Selection.WarmStandbyInterval = 2 * time.Second
	}
	if c.Probe.Timeout == 0 {
		c.Probe.Timeout = 2 * time.Second
	}
	if c.Probe.TCPTarget == "" {
		c.Probe.TCPTarget = "example.com:80"
	}
	if c.Probe.UDPTarget == "" {
		c.Probe.UDPTarget = "1.1.1.1:53"
	}
	if strings.Contains(c.Probe.UDPTarget, "::") {
		c.Probe.UDPTarget = "[2606:4700:4700::1111]:53"
	}
	if c.Probe.DNSName == "" {
		c.Probe.DNSName = "example.com"
	}
	if c.Probe.DNSType == "" {
		c.Probe.DNSType = "A"
	}
	// по умолчанию включим UDP (самый полезный) и TCP
	// если не хочешь — поставь false в yaml
	if !c.Probe.EnableTCP && !c.Probe.EnableUDP {
		c.Probe.EnableTCP = true
		c.Probe.EnableUDP = true
	}
	for i := range c.Upstreams {
		if c.Upstreams[i].Weight <= 0 {
			c.Upstreams[i].Weight = 1
		}
	}
	return &c, nil
}
