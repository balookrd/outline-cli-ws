package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WebSocketConn struct {
	conn       *websocket.Conn
	reader     io.Reader
	localAddr  net.Addr
	remoteAddr net.Addr
	mu         sync.Mutex
}

type WebSocketDialer struct {
	dialer *websocket.Dialer
	url    string
	host   string
	path   string
	useTLS bool
}

func NewWebSocketDialer(server string, port int, path string, useTLS bool) *WebSocketDialer {
	scheme := "ws"
	if useTLS {
		scheme = "wss"
	}

	tlsConfig := &tls.Config{
		ServerName: server,
		MinVersion: tls.VersionTLS12,
	}

	// Настройка заголовков для имитации браузера
	headers := http.Header{}
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	headers.Set("Origin", fmt.Sprintf("%s://%s", scheme, server))

	return &WebSocketDialer{
		dialer: &websocket.Dialer{
			TLSClientConfig:   tlsConfig,
			HandshakeTimeout:  45 * time.Second,
			EnableCompression: true,
			Subprotocols:      []string{"binary"},
		},
		url:    fmt.Sprintf("%s://%s:%d%s", scheme, server, port, path),
		host:   fmt.Sprintf("%s:%d", server, port),
		path:   path,
		useTLS: useTLS,
	}
}

func (d *WebSocketDialer) DialContext(ctx context.Context) (net.Conn, error) {
	wsConn, resp, err := d.dialer.DialContext(ctx, d.url, nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("websocket dial failed (status %d): %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("websocket dial failed: %w", err)
	}

	// Настройка параметров соединения
	wsConn.SetReadLimit(0) // No limit
	wsConn.EnableWriteCompression(true)

	localAddr := &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 0,
	}

	remoteAddr := &net.TCPAddr{
		IP:   net.ParseIP(d.host),
		Port: 0,
	}

	return &WebSocketConn{
		conn:       wsConn,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
	}, nil
}

func (c *WebSocketConn) Read(b []byte) (n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		if c.reader == nil {
			var messageType int
			messageType, c.reader, err = c.conn.NextReader()
			if err != nil {
				return 0, err
			}

			// Только бинарные сообщения
			if messageType != websocket.BinaryMessage {
				continue
			}
		}

		n, err = c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			continue
		}
		if err != nil {
			return 0, err
		}
		return n, nil
	}
}

func (c *WebSocketConn) Write(b []byte) (n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	err = c.conn.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *WebSocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func (c *WebSocketConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *WebSocketConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *WebSocketConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.SetReadDeadline(t); err != nil {
		return err
	}
	return c.conn.SetWriteDeadline(t)
}

func (c *WebSocketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.conn.SetReadDeadline(t)
}

func (c *WebSocketConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.conn.SetWriteDeadline(t)
}
