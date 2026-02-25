package internal

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

type upstreamState struct {
	cfg UpstreamConfig

	mu            sync.Mutex
	healthy       bool
	failCount     int
	successCount  int
	lastError     error
	lastCheckTime time.Time

	lastRTT time.Duration
	rttEWMA time.Duration

	cooldownUntil time.Time

	standbyMu  sync.Mutex
	standbyTCP *websocket.Conn
}

type LoadBalancer struct {
	hc  HealthcheckConfig
	sel SelectionConfig

	mu   sync.Mutex
	pool []*upstreamState

	current     *upstreamState
	stickyUntil time.Time
}

func NewLoadBalancer(ups []UpstreamConfig, hc HealthcheckConfig, sel SelectionConfig) *LoadBalancer {
	pool := make([]*upstreamState, 0, len(ups))
	for _, u := range ups {
		s := &upstreamState{cfg: u, healthy: false}
		pool = append(pool, s)
	}
	return &LoadBalancer{hc: hc, sel: sel, pool: pool}
}

func (lb *LoadBalancer) Pick() (*upstreamState, error) {
	now := time.Now()

	lb.mu.Lock()
	cur := lb.current
	stickyUntil := lb.stickyUntil
	pool := append([]*upstreamState(nil), lb.pool...)
	lb.mu.Unlock()

	// 1) Если есть текущий и он ещё "липкий" — используем его, пока он здоров и не в cooldown
	if cur != nil && now.Before(stickyUntil) {
		cur.mu.Lock()
		ok := cur.healthy && now.After(cur.cooldownUntil)
		cur.mu.Unlock()
		if ok {
			return cur, nil
		}
		// иначе failover немедленно
	}

	// 2) Выбираем лучшего кандидата
	best, bestRTT, err := lb.pickBestCandidate(pool, now)
	if err != nil {
		return nil, err
	}

	// 3) Hysteresis: если текущий здоров, не переключаемся без заметного выигрыша RTT
	if cur != nil {
		cur.mu.Lock()
		curOK := cur.healthy && now.After(cur.cooldownUntil)
		curRTT := cur.rttEWMA
		cur.mu.Unlock()

		if curOK && curRTT > 0 && bestRTT > 0 {
			// переключаемся только если новый лучше минимум на MinSwitch
			if curRTT-bestRTT < lb.sel.MinSwitch {
				// продлеваем sticky на текущем
				lb.mu.Lock()
				lb.current = cur
				lb.stickyUntil = now.Add(lb.sel.StickyTTL)
				lb.mu.Unlock()
				return cur, nil
			}
		}
	}

	// 4) Фиксируем нового fastest и делаем его sticky
	lb.mu.Lock()
	lb.current = best
	lb.stickyUntil = now.Add(lb.sel.StickyTTL)
	lb.mu.Unlock()

	return best, nil
}

func (lb *LoadBalancer) pickBestCandidate(pool []*upstreamState, now time.Time) (*upstreamState, time.Duration, error) {
	var best *upstreamState
	bestScore := float64(1e18)
	bestRTT := time.Duration(0)

	for _, s := range pool {
		s.mu.Lock()
		healthy := s.healthy
		rtt := s.rttEWMA
		cooldownUntil := s.cooldownUntil
		fail := s.failCount
		lastErr := s.lastError
		lastCheck := s.lastCheckTime
		w := s.cfg.Weight
		s.mu.Unlock()

		if !healthy {
			continue
		}
		if now.Before(cooldownUntil) {
			continue
		}

		// base RTT
		base := float64(rtt.Milliseconds())
		if base <= 0 {
			base = 1000 // если не меряли — не даём стать best
		}

		// penalties
		staleness := now.Sub(lastCheck)
		stalePenalty := 0.0
		if staleness > 2*lb.hc.Interval {
			stalePenalty = float64(staleness.Milliseconds()) * 0.2
		}
		failPenalty := float64(fail) * 500
		errPenalty := 0.0
		if lastErr != nil {
			errPenalty = 500
		}

		if w <= 0 {
			w = 1
		}
		weightFactor := 1.0 / float64(w)

		score := (base + stalePenalty + failPenalty + errPenalty) * weightFactor

		if score < bestScore {
			bestScore = score
			best = s
			bestRTT = rtt
		}
	}

	if best == nil {
		return nil, 0, errors.New("no healthy upstreams")
	}
	return best, bestRTT, nil
}

func (lb *LoadBalancer) RunHealthChecks(ctx context.Context) {
	// initial check immediately
	lb.checkAllOnce(ctx)

	t := time.NewTicker(lb.hc.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lb.checkAllOnce(ctx)
		}
	}
}

func (lb *LoadBalancer) checkAllOnce(ctx context.Context) {
	lb.mu.Lock()
	pool := append([]*upstreamState(nil), lb.pool...)
	lb.mu.Unlock()

	for _, s := range pool {
		go func(st *upstreamState) {
			cctx, cancel := context.WithTimeout(ctx, lb.hc.Timeout)
			defer cancel()

			rtt, err := ProbeWSS(cctx, st.cfg.TCPWSS)
			st.mu.Lock()
			defer st.mu.Unlock()

			st.lastCheckTime = time.Now()

			if err != nil {
				st.lastError = err
				st.successCount = 0
				st.failCount++
				// можно слегка "портить" rttEWMA на фейлах:
				// st.rttEWMA = st.rttEWMA + 200*time.Millisecond
				if st.failCount >= lb.hc.FailThreshold {
					if st.healthy {
						log.Printf("[HC] %s DOWN: %v", st.cfg.Name, err)
					}
					st.healthy = false
				}
				return
			}

			// success
			st.lastError = nil
			st.failCount = 0
			st.successCount++

			st.lastRTT = rtt
			if st.rttEWMA == 0 {
				st.rttEWMA = rtt
			} else {
				// EWMA: 80% старое, 20% новое
				st.rttEWMA = time.Duration(float64(st.rttEWMA)*0.8 + float64(rtt)*0.2)
			}

			if st.successCount >= lb.hc.SuccessThreshold {
				if !st.healthy {
					log.Printf("[HC] %s UP (rtt=%s)", st.cfg.Name, st.rttEWMA)
				}
				st.healthy = true
				st.failCount = 0
				st.lastError = nil
				st.cooldownUntil = time.Time{}
			}
		}(s)
	}
}

func (lb *LoadBalancer) ReportFailure(s *upstreamState, err error) {
	if s == nil {
		return
	}
	now := time.Now()

	s.mu.Lock()
	s.lastError = err
	s.failCount++
	// немедленный cooldown, чтобы не выбирать его снова прямо сейчас
	s.cooldownUntil = now.Add(lb.sel.Cooldown)

	// если накопили фейлы — считаем down (до следующего успешного HC)
	if s.failCount >= lb.hc.FailThreshold {
		s.healthy = false
	}
	s.mu.Unlock()

	// если это был текущий sticky — сбрасываем sticky, чтобы следующий Pick() сделал failover
	lb.mu.Lock()
	if lb.current == s {
		lb.stickyUntil = time.Time{}
	}
	lb.mu.Unlock()
}

func (lb *LoadBalancer) pickTopN(now time.Time, n int) []*upstreamState {
	lb.mu.Lock()
	pool := append([]*upstreamState(nil), lb.pool...)
	lb.mu.Unlock()

	out := make([]*upstreamState, 0, n)
	used := map[*upstreamState]bool{}

	for len(out) < n {
		var best *upstreamState
		bestScore := float64(1e18)

		for _, s := range pool {
			if used[s] {
				continue
			}
			s.mu.Lock()
			healthy := s.healthy
			rtt := s.rttEWMA
			cooldownUntil := s.cooldownUntil
			fail := s.failCount
			lastErr := s.lastError
			lastCheck := s.lastCheckTime
			w := s.cfg.Weight
			s.mu.Unlock()

			if !healthy || now.Before(cooldownUntil) {
				continue
			}

			base := float64(rtt.Milliseconds())
			if base <= 0 {
				base = 1000
			}
			staleness := now.Sub(lastCheck)
			stalePenalty := 0.0
			if staleness > 2*lb.hc.Interval {
				stalePenalty = float64(staleness.Milliseconds()) * 0.2
			}
			failPenalty := float64(fail) * 500
			errPenalty := 0.0
			if lastErr != nil {
				errPenalty = 500
			}
			if w <= 0 {
				w = 1
			}
			score := (base + stalePenalty + failPenalty + errPenalty) * (1.0 / float64(w))

			if score < bestScore {
				bestScore = score
				best = s
			}
		}

		if best == nil {
			break
		}
		used[best] = true
		out = append(out, best)
	}

	return out
}

func (lb *LoadBalancer) RunWarmStandby(ctx context.Context) {
	t := time.NewTicker(lb.sel.WarmStandbyInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			// закрыть все standby
			lb.mu.Lock()
			pool := append([]*upstreamState(nil), lb.pool...)
			lb.mu.Unlock()
			for _, u := range pool {
				u.standbyMu.Lock()
				if u.standbyTCP != nil {
					_ = u.standbyTCP.Close(websocket.StatusNormalClosure, "shutdown")
					u.standbyTCP = nil
				}
				u.standbyMu.Unlock()
			}
			return
		case <-t.C:
			now := time.Now()
			n := lb.sel.WarmStandbyN
			if n <= 0 {
				continue
			}
			top := lb.pickTopN(now, n)
			for _, u := range top {
				// прогреваем параллельно
				go lb.EnsureStandbyTCP(ctx, u)
			}
		}
	}
}
