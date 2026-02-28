package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type telemetry struct {
	enabled bool
	mu      sync.RWMutex

	selectedTotal map[string]uint64
	failuresTotal map[string]uint64
	healthy       map[string]float64
	wsPackets     map[string]uint64
	wsBytes       map[string]uint64
	wsDialSum     map[string]float64
	wsDialCount   map[string]uint64
}

var (
	metricsMu sync.RWMutex
	metrics   = telemetry{}
)

func EnablePrometheusMetrics() {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	if metrics.enabled {
		return
	}
	metrics.selectedTotal = make(map[string]uint64)
	metrics.failuresTotal = make(map[string]uint64)
	metrics.healthy = make(map[string]float64)
	metrics.wsPackets = make(map[string]uint64)
	metrics.wsBytes = make(map[string]uint64)
	metrics.wsDialSum = make(map[string]float64)
	metrics.wsDialCount = make(map[string]uint64)
	metrics.enabled = true
}

func StartMetricsServer(ctx context.Context, addr string) error {
	if strings.TrimSpace(addr) == "" {
		return errors.New("empty metrics address")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", metricsHandler)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("metrics server: %w", err)
	}
	return nil
}

func observeSelection(upstream, proto string) {
	metricsMu.RLock()
	if !metrics.enabled {
		metricsMu.RUnlock()
		return
	}
	metrics.mu.Lock()
	metricsMu.RUnlock()
	defer metrics.mu.Unlock()
	metrics.selectedTotal[fmt.Sprintf("upstream=%s,proto=%s", upstream, proto)]++
}

func observeFailure(upstream, proto string, err error) {
	metricsMu.RLock()
	if !metrics.enabled {
		metricsMu.RUnlock()
		return
	}
	metrics.mu.Lock()
	metricsMu.RUnlock()
	defer metrics.mu.Unlock()
	reason := failureReason(err)
	metrics.failuresTotal[fmt.Sprintf("upstream=%s,proto=%s,reason=%s", upstream, proto, reason)]++
}

func setHealthy(upstream, proto string, healthy bool) {
	metricsMu.RLock()
	if !metrics.enabled {
		metricsMu.RUnlock()
		return
	}
	metrics.mu.Lock()
	metricsMu.RUnlock()
	defer metrics.mu.Unlock()
	v := 0.0
	if healthy {
		v = 1
	}
	metrics.healthy[fmt.Sprintf("upstream=%s,proto=%s", upstream, proto)] = v
}

func observeWSFrame(direction string, bytes int) {
	metricsMu.RLock()
	if !metrics.enabled {
		metricsMu.RUnlock()
		return
	}
	metrics.mu.Lock()
	metricsMu.RUnlock()
	defer metrics.mu.Unlock()
	metrics.wsPackets[fmt.Sprintf("dir=%s", direction)]++
	metrics.wsBytes[fmt.Sprintf("dir=%s", direction)] += uint64(bytes)
}

func observeDial(upstream, proto string, d time.Duration) {
	metricsMu.RLock()
	if !metrics.enabled {
		metricsMu.RUnlock()
		return
	}
	metrics.mu.Lock()
	metricsMu.RUnlock()
	defer metrics.mu.Unlock()
	k := fmt.Sprintf("upstream=%s,proto=%s", upstream, proto)
	metrics.wsDialCount[k]++
	metrics.wsDialSum[k] += d.Seconds()
}

func failureReason(err error) string {
	if err == nil {
		return "unknown"
	}
	e := strings.ToLower(err.Error())
	switch {
	case strings.Contains(e, "timeout") || strings.Contains(e, "deadline"):
		return "timeout"
	case strings.Contains(e, "tls") || strings.Contains(e, "x509") || strings.Contains(e, "certificate"):
		return "tls"
	case strings.Contains(e, "dns") || strings.Contains(e, "no such host"):
		return "dns"
	case strings.Contains(e, "refused"):
		return "refused"
	default:
		return "other"
	}
}

func metricsHandler(w http.ResponseWriter, _ *http.Request) {
	metricsMu.RLock()
	enabled := metrics.enabled
	metricsMu.RUnlock()
	if !enabled {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("# metrics disabled\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	metrics.mu.RLock()
	defer metrics.mu.RUnlock()

	writeCounterVec(w, "outlinews_upstream_selected_total", metrics.selectedTotal)
	writeCounterVec(w, "outlinews_upstream_failures_total", metrics.failuresTotal)
	writeGaugeVec(w, "outlinews_upstream_healthy", metrics.healthy)
	writeCounterVec(w, "outlinews_ws_packets_total", metrics.wsPackets)
	writeCounterVec(w, "outlinews_ws_bytes_total", metrics.wsBytes)
	writeSummaryAsCountAndSum(w, "outlinews_ws_dial_duration_seconds", metrics.wsDialCount, metrics.wsDialSum)
}

func writeCounterVec(w http.ResponseWriter, name string, data map[string]uint64) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s{%s} %d\n", name, toPromLabels(k), data[k])
	}
}

func writeGaugeVec(w http.ResponseWriter, name string, data map[string]float64) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s{%s} %.0f\n", name, toPromLabels(k), data[k])
	}
}

func writeSummaryAsCountAndSum(w http.ResponseWriter, name string, counts map[string]uint64, sums map[string]float64) {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		labels := toPromLabels(k)
		fmt.Fprintf(w, "%s_count{%s} %d\n", name, labels, counts[k])
		fmt.Fprintf(w, "%s_sum{%s} %f\n", name, labels, sums[k])
	}
}

func toPromLabels(s string) string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		parts[i] = fmt.Sprintf("%s=\"%s\"", kv[0], strings.ReplaceAll(kv[1], "\"", "\\\""))
	}
	return strings.Join(parts, ",")
}
