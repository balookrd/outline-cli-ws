package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"outline-cli-ws/internal/config"
)

type Dialer interface {
	DialContext(ctx context.Context) (net.Conn, error)
}

type TCPDialer struct {
	server string
	port   int
}

func NewTCPDialer(server string, port int) *TCPDialer {
	return &TCPDialer{
		server: server,
		port:   port,
	}
}

func (d *TCPDialer) DialContext(ctx context.Context) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", d.server, d.port)
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return dialer.DialContext(ctx, "tcp", addr)
}

func CreateDialer(config *config.ServerConfig) (Dialer, error) {
	if config.WebSocket {
		return NewWebSocketDialer(
			config.Server,
			config.Port,
			config.WSPath,
			config.UseTLS,
		), nil
	}

	return NewTCPDialer(config.Server, config.Port), nil
}
