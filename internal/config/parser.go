package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type OutlineWSConfig struct {
	Transport struct {
		Type string `yaml:"$type"`
		TCP  struct {
			Type     string `yaml:"$type"`
			Endpoint struct {
				Type string `yaml:"$type"`
				URL  string `yaml:"url"`
			} `yaml:"endpoint"`
			Cipher string `yaml:"cipher"`
			Secret string `yaml:"secret"`
		} `yaml:"tcp"`
		UDP *struct {
			Type string `yaml:"$type"`
			Path string `yaml:"path"`
		} `yaml:"udp,omitempty"`
	} `yaml:"transport"`
}

func ParseKey(key string, name string) (*ServerConfig, error) {
	// Проверяем, является ли ключ YAML (WebSocket)
	if strings.Contains(key, "$type:") || strings.Contains(key, "transport:") {
		return parseWebSocketKey(key, name)
	}

	// Проверяем, является ли ключ стандартным ss://
	if strings.HasPrefix(key, "ss://") {
		return parseShadowsocksKey(key, name)
	}

	// Проверяем, является ли ключ файлом
	if _, err := os.Stat(key); err == nil {
		return parseKeyFile(key, name)
	}

	return nil, fmt.Errorf("unsupported key format")
}

func parseWebSocketKey(key string, name string) (*ServerConfig, error) {
	var wsConfig OutlineWSConfig
	err := yaml.Unmarshal([]byte(key), &wsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if wsConfig.Transport.Type != "tcp" {
		return nil, fmt.Errorf("unsupported transport type: %s", wsConfig.Transport.Type)
	}

	// Парсинг URL WebSocket
	wsURL := wsConfig.Transport.TCP.Endpoint.URL
	parsedURL, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("invalid websocket URL: %w", err)
	}

	config := &ServerConfig{
		Name:      name,
		Method:    wsConfig.Transport.TCP.Cipher,
		Password:  wsConfig.Transport.TCP.Secret,
		WebSocket: true,
		WSPath:    parsedURL.Path,
		UseTLS:    parsedURL.Scheme == "wss",
		UDP:       false,
	}

	// Парсинг host и port
	host := parsedURL.Hostname()
	port := 80
	if parsedURL.Scheme == "wss" {
		port = 443
	}
	if parsedURL.Port() != "" {
		fmt.Sscanf(parsedURL.Port(), "%d", &port)
	}

	config.Server = host
	config.Port = port

	// Проверка на UDP поддержку
	if wsConfig.Transport.UDP != nil {
		config.UDP = true
		config.UDPPath = wsConfig.Transport.UDP.Path
	}

	return config, nil
}

func parseShadowsocksKey(key string, name string) (*ServerConfig, error) {
	// Формат: ss://method:password@server:port
	key = strings.TrimPrefix(key, "ss://")

	// Проверяем на наличие Base64 encoding
	if strings.Contains(key, "@") {
		// Обычный формат
		parts := strings.Split(key, "@")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid ss:// key format")
		}

		methodPass := parts[0]
		hostPort := parts[1]

		// Парсинг method:password
		mpParts := strings.Split(methodPass, ":")
		if len(mpParts) != 2 {
			return nil, fmt.Errorf("invalid method:password format")
		}

		// Парсинг host:port
		hpParts := strings.Split(hostPort, ":")
		if len(hpParts) != 2 {
			return nil, fmt.Errorf("invalid host:port format")
		}

		port := 0
		fmt.Sscanf(hpParts[1], "%d", &port)

		return &ServerConfig{
			Name:      name,
			Server:    hpParts[0],
			Port:      port,
			Method:    mpParts[0],
			Password:  mpParts[1],
			WebSocket: false,
		}, nil
	} else {
		// Base64 encoded формат
		decoded, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64: %w", err)
		}

		parts := strings.Split(string(decoded), "@")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid decoded key format")
		}

		methodPass := strings.Split(parts[0], ":")
		if len(methodPass) != 2 {
			return nil, fmt.Errorf("invalid method:password in decoded key")
		}

		hostPort := strings.Split(parts[1], ":")
		if len(hostPort) != 2 {
			return nil, fmt.Errorf("invalid host:port in decoded key")
		}

		port := 0
		fmt.Sscanf(hostPort[1], "%d", &port)

		return &ServerConfig{
			Name:      name,
			Server:    hostPort[0],
			Port:      port,
			Method:    methodPass[0],
			Password:  methodPass[1],
			WebSocket: false,
		}, nil
	}
}

func parseKeyFile(filepath string, name string) (*ServerConfig, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	return ParseKey(string(data), name)
}

func LoadGlobalConfig(configDir string) (*GlobalConfig, error) {
	config := &GlobalConfig{
		Servers:   []*ServerConfig{},
		LocalAddr: "127.0.0.1",
		LocalPort: 1080,
		DNS:       "8.8.8.8",
		ConfigDir: configDir,
	}

	configFile := filepath.Join(configDir, "config.json")
	if _, err := os.Stat(configFile); err == nil {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, err
		}

		err = json.Unmarshal(data, config)
		if err != nil {
			return nil, err
		}
	}

	return config, nil
}

func (c *GlobalConfig) Save() error {
	if err := os.MkdirAll(c.ConfigDir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	configFile := filepath.Join(c.ConfigDir, "config.json")
	return os.WriteFile(configFile, data, 0644)
}
