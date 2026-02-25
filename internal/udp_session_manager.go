package internal

import (
	"context"
	"sync"
	"time"
)

type udpSessEntry struct {
	sess     *OutlineUDPSession
	lastUsed time.Time
}

type UDPSessionManager struct {
	mu  sync.Mutex
	m   map[*UpstreamState]*udpSessEntry
	ttl time.Duration
	lb  *LoadBalancer
}

func NewUDPSessionManager(lb *LoadBalancer, ttl time.Duration) *UDPSessionManager {
	return &UDPSessionManager{
		m:   make(map[*UpstreamState]*udpSessEntry),
		ttl: ttl,
		lb:  lb,
	}
}

func (sm *UDPSessionManager) Get(ctx context.Context, up *UpstreamState) (*OutlineUDPSession, error) {
	now := time.Now()

	sm.mu.Lock()
	e := sm.m[up]
	if e != nil && e.sess != nil {
		e.lastUsed = now
		s := e.sess
		sm.mu.Unlock()
		return s, nil
	}
	sm.mu.Unlock()

	// create new outside lock
	s, err := NewOutlineUDPSession(ctx, sm.lb, up)
	if err != nil {
		return nil, err
	}

	sm.mu.Lock()
	sm.m[up] = &udpSessEntry{sess: s, lastUsed: now}
	sm.mu.Unlock()

	return s, nil
}

func (sm *UDPSessionManager) GC() {
	now := time.Now()
	sm.mu.Lock()
	for up, e := range sm.m {
		if e == nil || e.sess == nil {
			delete(sm.m, up)
			continue
		}
		if now.Sub(e.lastUsed) > sm.ttl {
			e.sess.Close()
			delete(sm.m, up)
		}
	}
	sm.mu.Unlock()
}
