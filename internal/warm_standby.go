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
	return lb.acquireTCPWS(ctx, up, 0)
}

// AcquireTCPWSForFlow is identical to AcquireTCPWS but adds flow-scoped debug logs.
func (lb *LoadBalancer) AcquireTCPWSForFlow(ctx context.Context, up *UpstreamState, flowID uint64) (WSConn, error) {
	return lb.acquireTCPWS(ctx, up, flowID)
}

func (lb *LoadBalancer) acquireTCPWS(ctx context.Context, up *UpstreamState, flowID uint64) (WSConn, error) {
	logf := func(format string, args ...any) {
		if flowID > 0 {
			wsDebugf("flow=%d "+format, append([]any{flowID}, args...)...)
			return
		}
		wsDebugf(format, args...)
	}

	// 1) попробуем взять прогретый
	up.standbyMu.Lock()
	c := up.standbyTCP
	up.standbyTCP = nil
	up.standbyMu.Unlock()

	if c != nil {
		logf("acquire tcp ws: got standby candidate upstream=%q", up.cfg.Name)
		checkCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		aliveStarted := time.Now()
		ok := wsAliveCheck(checkCtx, c)
		cancel()
		logf("acquire tcp ws: standby alive-check upstream=%q ok=%v elapsed=%s", up.cfg.Name, ok, time.Since(aliveStarted))
		if ok {
			return c, nil
		}
		_ = c.Close(WSStatusNormalClosure, "stale-standby")
		logf("acquire tcp ws: standby rejected upstream=%q reason=stale", up.cfg.Name)
	}

	// 2) иначе — обычный dial
	dialStarted := time.Now()
	logf("acquire tcp ws: dialing fresh upstream=%q", up.cfg.Name)
	conn, err := lb.DialWSStreamLimited(ctx, up.cfg.TCPWSS)
	if err != nil {
		logf("acquire tcp ws: fresh dial failed upstream=%q elapsed=%s err=%v", up.cfg.Name, time.Since(dialStarted), err)
		return nil, err
	}
	logf("acquire tcp ws: fresh dial done upstream=%q elapsed=%s", up.cfg.Name, time.Since(dialStarted))
	return conn, nil
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
	cctx, cancel := context.WithTimeout(ctx, wsDialTimeoutForURL(lb.hc.Timeout, up.cfg.TCPWSS))
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
