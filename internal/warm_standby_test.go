package internal

import (
	"context"
	"testing"
	"time"
)

func TestWSAliveCheck_PingPongOK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	m := &mockWSConn{}
	m.enqueueRead(WSMessagePong, nil, nil) // empty pong is allowed

	if ok := wsAliveCheck(ctx, m); !ok {
		t.Fatalf("expected ok")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) != 1 || m.writes[0].typ != WSMessagePing {
		t.Fatalf("expected one ping write, got %#v", m.writes)
	}
}

func TestWSAliveCheck_RespondsToPing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	m := &mockWSConn{}
	m.enqueueRead(WSMessagePing, []byte("peer"), nil)
	m.enqueueRead(WSMessagePong, nil, nil)

	if ok := wsAliveCheck(ctx, m); !ok {
		t.Fatalf("expected ok")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) < 2 {
		t.Fatalf("expected >=2 writes, got %d", len(m.writes))
	}
	if m.writes[1].typ != WSMessagePong || string(m.writes[1].data) != "peer" {
		t.Fatalf("expected pong response to peer ping, got %#v", m.writes[1])
	}
}

func TestAcquireTCPWS_UsesStandbyWhenAlive(t *testing.T) {
	lb := NewLoadBalancer([]UpstreamConfig{{TCPWSS: "wss://example"}}, HealthcheckConfig{Timeout: time.Second}, SelectionConfig{}, ProbeConfig{}, 0)
	up := lb.pool[0]
	m := &mockWSConn{}
	m.enqueueRead(WSMessagePong, nil, nil)

	up.standbyMu.Lock()
	up.standbyTCP = m
	up.standbyMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := lb.AcquireTCPWS(ctx, up)
	if err != nil {
		t.Fatalf("AcquireTCPWS: %v", err)
	}
	if c != m {
		t.Fatalf("expected standby conn")
	}

	up.standbyMu.Lock()
	defer up.standbyMu.Unlock()
	if up.standbyTCP != nil {
		t.Fatalf("expected standby slot cleared after acquire")
	}
}
