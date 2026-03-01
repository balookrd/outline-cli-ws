package internal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockWSConn struct {
	mu    sync.Mutex
	reads []struct {
		typ  WSMessageType
		data []byte
		err  error
	}
	writes []struct {
		typ  WSMessageType
		data []byte
	}
	closed bool
}

func (m *mockWSConn) enqueueRead(typ WSMessageType, data []byte, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, struct {
		typ  WSMessageType
		data []byte
		err  error
	}{typ, data, err})
}

func (m *mockWSConn) Read(ctx context.Context) (WSMessageType, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.reads) == 0 {
		return 0, nil, errors.New("no reads queued")
	}
	r := m.reads[0]
	m.reads = m.reads[1:]
	return r.typ, r.data, r.err
}

func (m *mockWSConn) Write(ctx context.Context, typ WSMessageType, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, struct {
		typ  WSMessageType
		data []byte
	}{typ, append([]byte(nil), data...)})
	return nil
}

func (m *mockWSConn) Close(code WSStatusCode, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func TestWSPacketConn_ReadFrom_SkipsNonBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	m := &mockWSConn{}
	m.enqueueRead(WSMessagePing, []byte("x"), nil)
	m.enqueueRead(WSMessageText, []byte("hi"), nil)
	m.enqueueRead(WSMessageBinary, []byte{1, 2, 3}, nil)

	pc := NewWSPacketConn(ctx, m, "test-upstream", "udp")
	buf := make([]byte, 10)

	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected n=3, got %d", n)
	}
	if got := buf[:n]; string(got) != string([]byte{1, 2, 3}) {
		t.Fatalf("unexpected payload: %v", got)
	}
}

func TestWSPacketConn_WriteTo_WritesBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &mockWSConn{}
	pc := NewWSPacketConn(ctx, m, "test-upstream", "udp")

	payload := []byte("hello")
	n, err := pc.WriteTo(payload, nil)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("expected %d, got %d", len(payload), n)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(m.writes))
	}
	if m.writes[0].typ != WSMessageBinary {
		t.Fatalf("expected binary, got %v", m.writes[0].typ)
	}
	if string(m.writes[0].data) != "hello" {
		t.Fatalf("unexpected data: %q", m.writes[0].data)
	}
}

func TestWSPacketConn_Close(t *testing.T) {
	ctx := context.Background()
	m := &mockWSConn{}
	pc := NewWSPacketConn(ctx, m, "test-upstream", "udp")
	_ = pc.Close()

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		t.Fatalf("expected closed")
	}
}

func TestWSStreamConn_ObservesTrafficMetrics(t *testing.T) {
	metricsMu.Lock()
	metrics = telemetry{}
	metricsMu.Unlock()
	EnablePrometheusMetrics()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	m := &mockWSConn{}
	m.enqueueRead(WSMessageBinary, []byte("hello"), nil)

	sc := NewWSStreamConn(ctx, m, "edge-1", "tcp")

	buf := make([]byte, 16)
	n, err := sc.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected n=5, got %d", n)
	}

	if _, err := sc.Write([]byte("world!")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	metrics.mu.RLock()
	defer metrics.mu.RUnlock()

	if got := metrics.wsBytes["dir=in"]; got != 5 {
		t.Fatalf("ws in bytes=%d want 5", got)
	}
	if got := metrics.wsBytes["dir=out"]; got != 6 {
		t.Fatalf("ws out bytes=%d want 6", got)
	}
	if got := metrics.upstreamBytes["upstream=edge-1,proto=tcp,dir=in"]; got != 5 {
		t.Fatalf("upstream in bytes=%d want 5", got)
	}
	if got := metrics.upstreamBytes["upstream=edge-1,proto=tcp,dir=out"]; got != 6 {
		t.Fatalf("upstream out bytes=%d want 6", got)
	}

	m.mu.Lock()
	writes := m.writes
	m.mu.Unlock()
	if len(writes) != 1 || writes[0].typ != WSMessageBinary || !strings.Contains(string(writes[0].data), "world") {
		t.Fatalf("unexpected writes: %+v", writes)
	}
}
