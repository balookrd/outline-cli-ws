package config

import (
	"fmt"
)

type ServerConfig struct {
	ID         string `json:"id" yaml:"id"`
	Name       string `json:"name" yaml:"name"`
	Server     string `json:"server" yaml:"server"`
	Port       int    `json:"port" yaml:"port"`
	Method     string `json:"method" yaml:"method"`
	Password   string `json:"password" yaml:"password"`
	WebSocket  bool   `json:"websocket" yaml:"websocket"`
	WSPath     string `json:"ws_path" yaml:"ws_path"`
	UseTLS     bool   `json:"use_tls" yaml:"use_tls"`
	UDP        bool   `json:"udp" yaml:"udp"`
	UDPPath    string `json:"udp_path" yaml:"udp_path"`
	IsActive   bool   `json:"is_active"`
	ConfigPath string `json:"config_path"`
}

type GlobalConfig struct {
	Servers   []*ServerConfig `json:"servers"`
	ActiveID  string          `json:"active_id"`
	LocalAddr string          `json:"local_addr"`
	LocalPort int             `json:"local_port"`
	DNS       string          `json:"dns"`
	ConfigDir string          `json:"-"`
}

func (c *ServerConfig) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server address is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Port)
	}
	if c.Password == "" {
		return fmt.Errorf("password is required")
	}
	if c.Method == "" {
		return fmt.Errorf("encryption method is required")
	}
	if c.WebSocket && c.WSPath == "" {
		return fmt.Errorf("websocket path is required for websocket connections")
	}
	return nil
}

func (c *ServerConfig) GetKeyString() string {
	if c.WebSocket {
		protocol := "ws"
		if c.UseTLS {
			protocol = "wss"
		}
		return fmt.Sprintf("%s://%s:%d%s (%s)",
			protocol, c.Server, c.Port, c.WSPath, c.Method)
	}
	return fmt.Sprintf("ss://%s:%s@%s:%d",
		c.Method, c.Password, c.Server, c.Port)
}
