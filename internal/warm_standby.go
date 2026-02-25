package internal

import (
	"context"
	"time"

	"nhooyr.io/websocket"
)

// AcquireTCPWS: отдаёт прогретый WS (если есть) или делает Dial.
// Если взяли прогретый — слот освобождается и будет догрет снова.
func (lb *LoadBalancer) AcquireTCPWS(ctx context.Context, up *upstreamState) (*websocket.Conn, error) {
	// 1) попробуем взять прогретый
	up.standbyMu.Lock()
	c := up.standbyTCP
	up.standbyTCP = nil
	up.standbyMu.Unlock()

	if c != nil {
		return c, nil
	}

	// 2) иначе — обычный dial
	return DialWSStream(ctx, up.cfg.TCPWSS)
}

// EnsureStandbyTCP: гарантирует, что у апстрима есть прогретый TCP WS (если он healthy и не в cooldown).
func (lb *LoadBalancer) EnsureStandbyTCP(ctx context.Context, up *upstreamState) {
	up.mu.Lock()
	ok := up.tcp.healthy && time.Now().After(up.tcpCooldownUntil)
	up.mu.Unlock()
	if !ok {
		// если апстрим не ок — сбросим прогретый конн, если был
		up.standbyMu.Lock()
		if up.standbyTCP != nil {
			_ = up.standbyTCP.Close(websocket.StatusNormalClosure, "standby-reset")
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

	c, err := DialWSStream(cctx, up.cfg.TCPWSS)
	if err != nil {
		// не делаем жёсткий failover только из-за standby — но можно чуть штрафовать
		return
	}

	up.standbyMu.Lock()
	// если пока мы dial'или другой уже прогрел — закроем лишний
	if up.standbyTCP != nil {
		_ = c.Close(websocket.StatusNormalClosure, "duplicate-standby")
	} else {
		up.standbyTCP = c
	}
	up.standbyMu.Unlock()
}
