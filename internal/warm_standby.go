package internal

import (
	"bytes"
	"context"
	"fmt"
	"time"
)

// wsAliveCheck verifies that an idle standby websocket is still usable.
// Some servers close idle WebSocket CONNECT streams silently; if we hand such
// a stale conn to a new TCP tunnel, the first bytes (e.g. TLS ClientHello)
// may be dropped/reset, causing intermittent SSL_ERROR_SYSCALL.
//
// We send a Ping and expect a Pong (echo payload or empty).
func wsAliveCheck(ctx context.Context, c WSConn) bool {
	payload := []byte(fmt.Sprintf("ws-keepalive-%d", time.Now().UnixNano()))
	if err := c.Write(ctx, WSMessagePing, payload); err != nil {
		return false
	}
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return false
		}
		switch typ {
		case WSMessagePong:
			return len(data) == 0 || bytes.Equal(data, payload)
		case WSMessagePing:
			// Respond to keep the peer happy.
			_ = c.Write(ctx, WSMessagePong, data)
		case WSMessageClose:
			return false
		default:
			// Ignore unexpected frames.
		}
	}
}

// AcquireTCPWS отдаёт прогретый WS (если есть) или делает Dial.
// Если взяли прогретый — слот освобождается и будет догрет снова.
func (lb *LoadBalancer) AcquireTCPWS(ctx context.Context, up *UpstreamState) (WSConn, error) {
	// 1) попробуем взять прогретый
	up.standbyMu.Lock()
	c := up.standbyTCP
	up.standbyTCP = nil
	up.standbyMu.Unlock()

	if c != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		ok := wsAliveCheck(checkCtx, c)
		cancel()
		if ok {
			return c, nil
		}
		_ = c.Close(WSStatusNormalClosure, "stale-standby")
	}

	// 2) иначе — обычный dial
	return lb.DialWSStreamLimited(ctx, up.cfg.TCPWSS)
}

// EnsureStandbyTCP гарантирует, что у апстрима есть прогретый TCP WS (если он healthy и не в cooldown).
func (lb *LoadBalancer) EnsureStandbyTCP(ctx context.Context, up *UpstreamState) {
	up.mu.Lock()
	ok := up.tcp.healthy && time.Now().After(up.tcpCooldownUntil)
	up.mu.Unlock()
	if !ok {
		// если апстрим не ок — сбросим прогретый конн, если был
		up.standbyMu.Lock()
		if up.standbyTCP != nil {
			_ = up.standbyTCP.Close(WSStatusNormalClosure, "standby-reset")
			up.standbyTCP = nil
		}
		up.standbyMu.Unlock()
		return
	}

	// уже есть прогретый?
	up.standbyMu.Lock()
	exists := up.standbyTCP != nil
	up.standbyMu.Unlock()
	if exists {
		return
	}

	// догреваем
	cctx, cancel := context.WithTimeout(ctx, lb.hc.Timeout)
	defer cancel()

	c, err := DialWSStream(cctx, up.cfg.TCPWSS, lb.fwmark)
	if err != nil {
		// не делаем жёсткий failover только из-за standby — но можно чуть штрафовать
		return
	}

	up.standbyMu.Lock()
	// если пока мы dial'или другой уже прогрел — закроем лишний
	if up.standbyTCP != nil {
		_ = c.Close(WSStatusNormalClosure, "duplicate-standby")
	} else {
		up.standbyTCP = c
	}
	up.standbyMu.Unlock()
}
