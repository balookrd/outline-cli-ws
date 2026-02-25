package internal

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen struct {
		SOCKS5 string `yaml:"socks5"`
	} `yaml:"listen"`
	Healthcheck HealthcheckConfig `yaml:"healthcheck"`
	Selection   SelectionConfig   `yaml:"selection"`
	Upstreams   []UpstreamConfig  `yaml:"upstreams"`
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
	for i := range c.Upstreams {
		if c.Upstreams[i].Weight <= 0 {
			c.Upstreams[i].Weight = 1
		}
	}
	return &c, nil
}
