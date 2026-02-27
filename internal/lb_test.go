package internal

import (
	"testing"
	"time"
)

func markHealthy(up *UpstreamState, isTCP bool, rtt time.Duration) {
	up.mu.Lock()
	if isTCP {
		up.tcp.healthy = true
		up.tcp.rttEWMA = rtt
		up.tcpCooldownUntil = time.Time{}
	} else {
		up.udp.healthy = true
		up.udp.rttEWMA = rtt
		up.udpCooldownUntil = time.Time{}
	}
	up.mu.Unlock()
}

func TestPickTCP_Sticky(t *testing.T) {
	sel := SelectionConfig{StickyTTL: 200 * time.Millisecond, MinSwitch: 0}
	lb := NewLoadBalancer([]UpstreamConfig{{TCPWSS: "a"}, {TCPWSS: "b"}}, HealthcheckConfig{}, sel, ProbeConfig{}, 0)

	u0 := lb.pool[0]
	u1 := lb.pool[1]
	markHealthy(u0, true, 50*time.Millisecond)
	markHealthy(u1, true, 10*time.Millisecond)

	got, err := lb.PickTCP()
	if err != nil {
		t.Fatalf("PickTCP: %v", err)
	}
	if got != u1 {
		t.Fatalf("expected u1, got %v", got.cfg.TCPWSS)
	}

	lb.mu.Lock()
	lb.current = u0
	lb.stickyUntil = time.Now().Add(150 * time.Millisecond)
	lb.mu.Unlock()

	got2, err := lb.PickTCP()
	if err != nil {
		t.Fatalf("PickTCP: %v", err)
	}
	if got2 != u0 {
		t.Fatalf("expected sticky u0, got %v", got2.cfg.TCPWSS)
	}
}

func TestPickTCP_HysteresisMinSwitch(t *testing.T) {
	sel := SelectionConfig{StickyTTL: 0, MinSwitch: 15 * time.Millisecond}
	lb := NewLoadBalancer([]UpstreamConfig{{TCPWSS: "a"}, {TCPWSS: "b"}}, HealthcheckConfig{}, sel, ProbeConfig{}, 0)
	u0 := lb.pool[0]
	u1 := lb.pool[1]
	markHealthy(u0, true, 50*time.Millisecond)
	markHealthy(u1, true, 40*time.Millisecond)

	lb.mu.Lock()
	lb.current = u0
	lb.stickyUntil = time.Time{}
	lb.mu.Unlock()

	got, err := lb.PickTCP()
	if err != nil {
		t.Fatalf("PickTCP: %v", err)
	}
	if got != u0 {
		t.Fatalf("expected stay on u0 due to MinSwitch hysteresis")
	}

	markHealthy(u1, true, 10*time.Millisecond)
	got2, err := lb.PickTCP()
	if err != nil {
		t.Fatalf("PickTCP: %v", err)
	}
	if got2 != u1 {
		t.Fatalf("expected switch to u1")
	}
}

func TestPickUDP_NoSticky(t *testing.T) {
	lb := NewLoadBalancer([]UpstreamConfig{{UDPWSS: "a"}, {UDPWSS: "b"}}, HealthcheckConfig{}, SelectionConfig{}, ProbeConfig{}, 0)
	u0 := lb.pool[0]
	u1 := lb.pool[1]
	markHealthy(u0, false, 30*time.Millisecond)
	markHealthy(u1, false, 5*time.Millisecond)

	got, err := lb.PickUDP()
	if err != nil {
		t.Fatalf("PickUDP: %v", err)
	}
	if got != u1 {
		t.Fatalf("expected best u1")
	}
}
