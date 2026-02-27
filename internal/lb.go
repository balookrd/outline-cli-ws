package internal

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

type hcState struct {
	healthy      bool
	failCount    int
	successCount int

	// inFlight prevents the scheduler from launching multiple concurrent
	// health checks for the same upstream+protocol when a check takes longer
	// than the scheduler tick.
	inFlight bool

	lastError     error
	lastCheckTime time.Time

	lastRTT time.Duration
	rttEWMA time.Duration

	nextHC  time.Time
	hcEvery time.Duration
}

type UpstreamState struct {
	cfg UpstreamConfig
	mu  sync.Mutex

	// separate HC
	tcp hcState
	udp hcState

	// cooldown (separate, optional but useful)
	tcpCooldownUntil time.Time
	udpCooldownUntil time.Time

	// warm-standby TCP
	standbyMu  sync.Mutex
	standbyTCP WSConn
}

type LoadBalancer struct {
	hc     HealthcheckConfig
	sel    SelectionConfig
	probe  ProbeConfig
	fwmark uint32

	mu   sync.Mutex
	pool []*UpstreamState

	current     *UpstreamState
	stickyUntil time.Time

	dialSem chan struct{}
}

func NewLoadBalancer(ups []UpstreamConfig, hc HealthcheckConfig, sel SelectionConfig, probe ProbeConfig, fwmark uint32) *LoadBalancer {
	pool := make([]*UpstreamState, 0, len(ups))
	for _, u := range ups {
		s := &UpstreamState{cfg: u}
		s.tcp.healthy = false
		s.udp.healthy = false
		pool = append(pool, s)
	}
	lb := &LoadBalancer{hc: hc, sel: sel, probe: probe, fwmark: fwmark, pool: pool}
	lb.dialSem = make(chan struct{}, 32) // default parallel dials
	return lb
}

func (lb *LoadBalancer) PickTCP() (*UpstreamState, error) {
	return lb.pickByEndpoint(true)
}

func (lb *LoadBalancer) PickUDP() (*UpstreamState, error) {
	return lb.pickByEndpoint(false)
}

func (lb *LoadBalancer) pickByEndpoint(isTCP bool) (*UpstreamState, error) {
	now := time.Now()

	lb.mu.Lock()
	pool := append([]*UpstreamState(nil), lb.pool...)
	// sticky делаем только для TCP (как и warm-standby), для UDP можно тоже, но обычно не надо
	cur := lb.current
	stickyUntil := lb.stickyUntil
	lb.mu.Unlock()

	// sticky только TCP
	if isTCP && cur != nil && now.Before(stickyUntil) {
		cur.mu.Lock()
		ok := cur.tcp.healthy && now.After(cur.tcpCooldownUntil)
		cur.mu.Unlock()
		if ok {
			return cur, nil
		}
	}

	best, bestRTT, err := lb.pickBestCandidateByEndpoint(pool, now, isTCP)
	if err != nil {
		return nil, err
	}

	// hysteresis + sticky тоже только TCP
	if isTCP && cur != nil {
		cur.mu.Lock()
		curOK := cur.tcp.healthy && now.After(cur.tcpCooldownUntil)
		curRTT := cur.tcp.rttEWMA
		cur.mu.Unlock()

		if curOK && curRTT > 0 && bestRTT > 0 {
			if curRTT-bestRTT < lb.sel.MinSwitch {
				lb.mu.Lock()
				lb.current = cur
				lb.stickyUntil = now.Add(lb.sel.StickyTTL)
				lb.mu.Unlock()
				return cur, nil
			}
		}
	}

	if isTCP {
		lb.mu.Lock()
		lb.current = best
		lb.stickyUntil = now.Add(lb.sel.StickyTTL)
		lb.mu.Unlock()
	}

	return best, nil
}

func (lb *LoadBalancer) pickBestCandidateByEndpoint(pool []*UpstreamState, now time.Time, isTCP bool) (*UpstreamState, time.Duration, error) {
	var best *UpstreamState
	bestScore := float64(1e18)
	bestRTT := time.Duration(0)

	for _, s := range pool {
		s.mu.Lock()
		var h hcState
		var cooldownUntil time.Time
		if isTCP {
			h = s.tcp
			cooldownUntil = s.tcpCooldownUntil
		} else {
			h = s.udp
			cooldownUntil = s.udpCooldownUntil
		}
		w := s.cfg.Weight
		s.mu.Unlock()

		if !h.healthy || now.Before(cooldownUntil) {
			continue
		}

		base := float64(h.rttEWMA.Milliseconds())
		if base <= 0 {
			base = 1000
		}

		staleness := now.Sub(h.lastCheckTime)
		stalePenalty := 0.0
		if staleness > 2*lb.hc.Interval {
			stalePenalty = float64(staleness.Milliseconds()) * 0.2
		}

		failPenalty := float64(h.failCount) * 500
		errPenalty := 0.0
		if h.lastError != nil {
			errPenalty = 500
		}

		if w <= 0 {
			w = 1
		}
		score := (base + stalePenalty + failPenalty + errPenalty) * (1.0 / float64(w))

		if score < bestScore {
			bestScore = score
			best = s
			bestRTT = h.rttEWMA
		}
	}

	if best == nil {
		return nil, 0, errors.New("no healthy upstreams")
	}
	return best, bestRTT, nil
}

func (lb *LoadBalancer) RunHealthChecks(ctx context.Context) {
	// init: сразу запланируем всем "прямо сейчас"
	lb.mu.Lock()
	pool := append([]*UpstreamState(nil), lb.pool...)
	lb.mu.Unlock()

	now := time.Now()
	for _, s := range pool {
		s.mu.Lock()

		s.tcp.nextHC = now
		s.udp.nextHC = now

		if s.tcp.hcEvery == 0 {
			s.tcp.hcEvery = lb.hc.Interval
		}
		if s.udp.hcEvery == 0 {
			s.udp.hcEvery = lb.hc.Interval
		}

		s.mu.Unlock()
	}

	// scheduler loop
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lb.runDueChecks(ctx)
		}
	}
}

func (lb *LoadBalancer) runDueChecks(ctx context.Context) {
	lb.mu.Lock()
	pool := append([]*UpstreamState(nil), lb.pool...)
	lb.mu.Unlock()

	now := time.Now()

	for _, st := range pool {
		var launchTCP, launchUDP bool
		st.mu.Lock()
		// Mark as in-flight under the lock to avoid duplicate goroutines.
		if !st.tcp.inFlight && !st.tcp.nextHC.After(now) {
			st.tcp.inFlight = true
			launchTCP = true
		}
		if !st.udp.inFlight && !st.udp.nextHC.After(now) {
			st.udp.inFlight = true
			launchUDP = true
		}
		st.mu.Unlock()

		if launchTCP {
			go lb.checkOneTCP(ctx, st)
		}
		if launchUDP {
			go lb.checkOneUDP(ctx, st)
		}
	}
}

func (lb *LoadBalancer) ReportTCPFailure(s *UpstreamState, err error) {
	if s == nil {
		return
	}
	now := time.Now()

	s.mu.Lock()
	s.tcp.lastError = err
	s.tcp.failCount++
	s.tcp.successCount = 0
	s.tcp.healthy = false
	s.tcpCooldownUntil = now.Add(lb.sel.Cooldown)

	// ускоряем TCP HC
	s.tcp.hcEvery = lb.hc.MinInterval
	s.tcp.nextHC = now.Add(applyJitter(lb.hc.MinInterval, lb.hc.Jitter))
	s.mu.Unlock()

	// сбрасываем sticky
	lb.mu.Lock()
	if lb.current == s {
		lb.stickyUntil = time.Time{}
	}
	lb.mu.Unlock()
}

func (lb *LoadBalancer) ReportUDPFailure(s *UpstreamState, err error) {
	if s == nil {
		return
	}
	now := time.Now()

	s.mu.Lock()
	s.udp.lastError = err
	s.udp.failCount++
	s.udp.successCount = 0
	s.udp.healthy = false
	s.udpCooldownUntil = now.Add(lb.sel.Cooldown)

	// ускоряем UDP HC
	s.udp.hcEvery = lb.hc.MinInterval
	s.udp.nextHC = now.Add(applyJitter(lb.hc.MinInterval, lb.hc.Jitter))
	s.mu.Unlock()
}

func (lb *LoadBalancer) pickTopN(now time.Time, n int) []*UpstreamState {
	lb.mu.Lock()
	pool := append([]*UpstreamState(nil), lb.pool...)
	lb.mu.Unlock()

	out := make([]*UpstreamState, 0, n)
	used := map[*UpstreamState]bool{}

	for len(out) < n {
		var best *UpstreamState
		bestScore := float64(1e18)

		for _, s := range pool {
			if used[s] {
				continue
			}
			s.mu.Lock()
			healthy := s.tcp.healthy
			rtt := s.tcp.rttEWMA
			cooldownUntil := s.tcpCooldownUntil
			fail := s.tcp.failCount
			lastErr := s.tcp.lastError
			lastCheck := s.tcp.lastCheckTime
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
			pool := append([]*UpstreamState(nil), lb.pool...)
			lb.mu.Unlock()
			for _, u := range pool {
				u.standbyMu.Lock()
				if u.standbyTCP != nil {
					_ = u.standbyTCP.Close(WSStatusNormalClosure, "shutdown")
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

func (lb *LoadBalancer) checkOneTCP(parent context.Context, st *UpstreamState) {
	cctx, cancel := context.WithTimeout(parent, lb.hc.Timeout)
	defer cancel()

	rtt, err := ProbeWSS(cctx, st.cfg.TCPWSS, lb.fwmark)
	if err == nil && lb.probe.EnableTCP {
		pctx, pcancel := context.WithTimeout(parent, lb.probe.Timeout)
		prtt, perr := ProbeTCPQuality(pctx, st.cfg, lb.probe.TCPTarget, lb.fwmark)
		pcancel()
		if perr != nil {
			err = perr
		} else {
			rtt = prtt
		}
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	defer func() { st.tcp.inFlight = false }()

	lb.applyHCResult(&st.tcp, err, rtt, st.cfg.Name, "tcp")

	// если TCP поднялся — можно снять TCP cooldown
	if st.tcp.healthy {
		st.tcpCooldownUntil = time.Time{}
	}
}

func (lb *LoadBalancer) checkOneUDP(parent context.Context, st *UpstreamState) {
	cctx, cancel := context.WithTimeout(parent, lb.hc.Timeout)
	defer cancel()

	rtt, err := ProbeWSS(cctx, st.cfg.UDPWSS, lb.fwmark)
	if err == nil && lb.probe.EnableUDP {
		pctx, pcancel := context.WithTimeout(parent, lb.probe.Timeout)
		prtt, perr := ProbeUDPQuality(pctx, st.cfg, lb.probe.UDPTarget, lb.probe.DNSName, lb.probe.DNSType, lb.fwmark)
		pcancel()
		if perr != nil {
			err = perr
		} else {
			rtt = prtt
		}
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	defer func() { st.udp.inFlight = false }()

	lb.applyHCResult(&st.udp, err, rtt, st.cfg.Name, "udp")

	// если UDP поднялся — можно снять UDP cooldown
	if st.udp.healthy {
		st.udpCooldownUntil = time.Time{}
	}
}

func (lb *LoadBalancer) applyHCResult(h *hcState, err error, rtt time.Duration,
	name string, proto string) {
	h.lastCheckTime = time.Now()

	if err != nil {
		h.lastError = err
		h.successCount = 0
		h.failCount++

		if h.failCount >= lb.hc.FailThreshold {
			if h.healthy {
				log.Printf("[HC|%s] %s DOWN: %v", proto, name, err)
			}
			h.healthy = false
		}

		h.hcEvery = lb.nextIntervalOnFailure(*h)
		h.nextHC = time.Now().Add(applyJitter(h.hcEvery, lb.hc.Jitter))
		return
	}

	// success
	h.lastError = nil
	h.failCount = 0
	h.successCount++

	h.lastRTT = rtt
	if h.rttEWMA == 0 {
		h.rttEWMA = rtt
	} else {
		h.rttEWMA = time.Duration(float64(h.rttEWMA)*0.8 + float64(rtt)*0.2)
	}

	if h.successCount >= lb.hc.SuccessThreshold {
		if !h.healthy {
			log.Printf("[HC|%s] %s UP (rtt=%s)", proto, name, h.rttEWMA)
		}
		h.healthy = true
	}

	h.hcEvery = lb.nextIntervalOnSuccess(*h)
	h.nextHC = time.Now().Add(applyJitter(h.hcEvery, lb.hc.Jitter))
}

func (lb *LoadBalancer) nextIntervalOnFailure(h hcState) time.Duration {
	base := lb.hc.MinInterval
	if h.hcEvery > 0 {
		base = h.hcEvery
	}
	if h.healthy {
		base = lb.hc.MinInterval
	}
	next := time.Duration(float64(base) * lb.hc.BackoffFactor)
	if next < lb.hc.MinInterval {
		next = lb.hc.MinInterval
	}
	if next > lb.hc.MaxInterval {
		next = lb.hc.MaxInterval
	}
	return next
}

func (lb *LoadBalancer) nextIntervalOnSuccess(h hcState) time.Duration {
	base := h.hcEvery
	if base == 0 {
		base = lb.hc.Interval
	}
	if h.successCount < 3 {
		base = minDur(base, lb.hc.Interval)
	}
	add := time.Duration(float64(h.rttEWMA) * lb.hc.RTTScale)
	next := time.Duration(float64(base)*1.2) + add

	if next < lb.hc.MinInterval {
		next = lb.hc.MinInterval
	}
	if next > lb.hc.MaxInterval {
		next = lb.hc.MaxInterval
	}
	return next
}

func (lb *LoadBalancer) acquireDialSlot(ctx context.Context) error {
	select {
	case lb.dialSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (lb *LoadBalancer) releaseDialSlot() {
	select {
	case <-lb.dialSem:
	default:
	}
}

func (lb *LoadBalancer) DialWSStreamLimited(ctx context.Context, url string) (WSConn, error) {
	if err := lb.acquireDialSlot(ctx); err != nil {
		return nil, err
	}
	defer lb.releaseDialSlot()
	return DialWSStream(ctx, url, lb.fwmark)
}
