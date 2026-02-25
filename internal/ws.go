package internal

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"time"

	"nhooyr.io/websocket"
)

type WSNetConn struct {
	ctx    context.Context
	c      *websocket.Conn
	r      net.Conn // nil (we implement Read/Write via ws messages)
	closed chan struct{}
	buf    []byte
}

func DialWSStream(ctx context.Context, rawurl string) (*websocket.Conn, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	opts := &websocket.DialOptions{
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			},
		},
	}
	// ws:// or wss:// supported
	c, _, err := websocket.Dial(ctx, u.String(), opts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Probe just verifies WebSocket handshake succeeds.
func ProbeWSS(ctx context.Context, rawurl string) (time.Duration, error) {
	start := time.Now()
	c, err := DialWSStream(ctx, rawurl)
	if err != nil {
		return 0, err
	}
	_ = c.Close(websocket.StatusNormalClosure, "probe")
	return time.Since(start), nil
}
