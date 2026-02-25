package internal

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"syscall"
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

func DialWSStream(ctx context.Context, rawurl string, fwmark uint32) (*websocket.Conn, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}

	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var ctrlErr error
			if err := c.Control(func(fd uintptr) {
				ctrlErr = setSocketMark(fd, fwmark)
			}); err != nil {
				return err
			}
			return ctrlErr
		},
	}

	tr := &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: d.DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	opts := &websocket.DialOptions{
		HTTPClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: tr,
		},
	}

	c, _, err := websocket.Dial(ctx, u.String(), opts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ProbeWSS Probe just verifies WebSocket handshake succeeds.
func ProbeWSS(ctx context.Context, rawurl string, fwmark uint32) (time.Duration, error) {
	start := time.Now()
	c, err := DialWSStream(ctx, rawurl, fwmark)
	if err != nil {
		return 0, err
	}
	_ = c.Close(websocket.StatusNormalClosure, "probe")
	return time.Since(start), nil
}
