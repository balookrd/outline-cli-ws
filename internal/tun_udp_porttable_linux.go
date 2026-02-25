//go:build linux

package internal

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type udpPortKey struct {
	netProto uint8
	srcIP    string
	srcPort  uint16
}

type udpPortSession struct {
	key      udpPortKey
	up       *UpstreamState
	sess     *OutlineUDPSession
	lastSeen time.Time

	mu    sync.Mutex
	flows map[string]time.Time // dst -> lastSeen
}

type udpPortTable struct {
	mu    sync.Mutex
	lb    *LoadBalancer
	cfg   TunConfig
	ports map[udpPortKey]*udpPortSession
}

func newUDPPortTable(lb *LoadBalancer, cfg TunConfig) *udpPortTable {
	return &udpPortTable{
		lb:    lb,
		cfg:   cfg,
		ports: make(map[udpPortKey]*udpPortSession),
	}
}

func (t *udpPortTable) getOrCreate(ctx context.Context, key udpPortKey) (*udpPortSession, error) {
	now := time.Now()

	t.mu.Lock()
	if ps := t.ports[key]; ps != nil {
		ps.lastSeen = now
		t.mu.Unlock()
		return ps, nil
	}

	limit := t.cfg.UDPMaxFlows
	if limit <= 0 {
		limit = 4096
	}
	if len(t.ports) >= limit {
		t.mu.Unlock()
		return nil, fmt.Errorf("udp port session limit reached: %d", limit)
	}
	t.mu.Unlock()

	up, err := t.lb.PickUDP()
	if err != nil {
		return nil, err
	}
	sess, err := NewOutlineUDPSession(ctx, t.lb, up)
	if err != nil {
		t.lb.ReportUDPFailure(up, err)
		return nil, err
	}

	ps := &udpPortSession{
		key:      key,
		up:       up,
		sess:     sess,
		lastSeen: now,
		flows:    make(map[string]time.Time),
	}

	t.mu.Lock()
	t.ports[key] = ps
	t.mu.Unlock()

	return ps, nil
}

func (t *udpPortTable) gcOnce() {
	now := time.Now()

	portIdle := t.cfg.UDPIdleTimeout
	if portIdle <= 0 {
		portIdle = 60 * time.Second
	}
	flowIdle := t.cfg.UDPFlowIdleTimeout
	if flowIdle <= 0 {
		flowIdle = 30 * time.Second
	}

	var toClose []*udpPortSession

	t.mu.Lock()
	for k, ps := range t.ports {
		if ps == nil {
			delete(t.ports, k)
			continue
		}

		// 1) prune flows inside session
		ps.mu.Lock()
		for dst, last := range ps.flows {
			if now.Sub(last) > flowIdle {
				delete(ps.flows, dst)
				ps.sess.Unsubscribe(dst)
			}
		}
		ps.mu.Unlock()

		// 2) prune port session itself
		if now.Sub(ps.lastSeen) > portIdle {
			delete(t.ports, k)
			toClose = append(toClose, ps)
		}
	}
	t.mu.Unlock()

	for _, ps := range toClose {
		ps.sess.Close()
	}
}
