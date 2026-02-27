package internal

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

type coderConn struct {
	c *websocket.Conn
}

func (c *coderConn) Read(ctx context.Context) (WSMessageType, []byte, error) {
	mt, data, err := c.c.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	switch mt {
	case websocket.MessageText:
		return WSMessageText, data, nil
	case websocket.MessageBinary:
		return WSMessageBinary, data, nil
	default:
		// Map anything else to binary-ish; callers filter anyway.
		return WSMessageBinary, data, nil
	}
}

func (c *coderConn) Write(ctx context.Context, typ WSMessageType, data []byte) error {
	var mt websocket.MessageType
	switch typ {
	case WSMessageText:
		mt = websocket.MessageText
	default:
		mt = websocket.MessageBinary
	}
	return c.c.Write(ctx, mt, data)
}

func (c *coderConn) Close(code WSStatusCode, reason string) error {
	// coder/websocket Close expects a websocket.StatusCode.
	return c.c.Close(websocket.StatusCode(int(code)), reason)
}

func dialCoderWebSocket(ctx context.Context, rawurl string, tr *http.Transport) (WSConn, error) {
	opts := &websocket.DialOptions{
		HTTPClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: tr,
		},
	}
	conn, _, err := websocket.Dial(ctx, rawurl, opts)
	if err != nil {
		return nil, err
	}
	return &coderConn{c: conn}, nil
}
