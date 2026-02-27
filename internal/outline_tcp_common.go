package internal

import (
	"context"
	"net"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
)

// newSSTCPConn creates a Shadowsocks stream over a WS stream and writes the target header.
// The returned conn is the encrypted stream ready for io.Copy.
//
// Ownership: caller must Close() the returned conn.
func newSSTCPConn(ctx context.Context, wsc WSConn, up UpstreamConfig, dst string) (net.Conn, error) {
	wsconn := NewWSStreamConn(ctx, wsc)

	ciph, err := core.PickCipher(up.Cipher, nil, up.Secret)
	if err != nil {
		return nil, err
	}

	ssconn := ciph.StreamConn(wsconn)

	tgt := socks.ParseAddr(dst)
	if tgt == nil {
		_ = ssconn.Close()
		return nil, socks.ErrAddressNotSupported
	}
	if _, err := ssconn.Write(tgt); err != nil {
		_ = ssconn.Close()
		return nil, err
	}

	return ssconn, nil
}
