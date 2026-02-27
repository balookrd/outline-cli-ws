package internal

import (
	"math/rand"
	"testing"
	"time"
)

func withDeterministicRNG(t *testing.T, seed int64, fn func()) {
	t.Helper()
	rngMu.Lock()
	old := rng
	rng = rand.New(rand.NewSource(seed))
	rngMu.Unlock()
	t.Cleanup(func() {
		rngMu.Lock()
		rng = old
		rngMu.Unlock()
	})
	fn()
}

func TestMinDur(t *testing.T) {
	if got := minDur(5*time.Second, 3*time.Second); got != 3*time.Second {
		t.Fatalf("minDur: got %v", got)
	}
	if got := minDur(1*time.Nanosecond, 1*time.Nanosecond); got != 1*time.Nanosecond {
		t.Fatalf("minDur equal: got %v", got)
	}
}

func TestApplyJitter_NoJitter(t *testing.T) {
	base := 1500 * time.Millisecond
	if got := applyJitter(base, 0); got != base {
		t.Fatalf("expected %v, got %v", base, got)
	}
	if got := applyJitter(base, -10*time.Millisecond); got != base {
		t.Fatalf("expected %v, got %v", base, got)
	}
}

func TestApplyJitter_RangeAndNonNegative(t *testing.T) {
	withDeterministicRNG(t, 1, func() {
		base := 200 * time.Millisecond
		jitter := 50 * time.Millisecond
		for i := 0; i < 200; i++ {
			got := applyJitter(base, jitter)
			if got < 0 {
				t.Fatalf("negative duration: %v", got)
			}
			if got < base-jitter || got > base+jitter {
				t.Fatalf("out of range: got %v, base=%v jitter=%v", got, base, jitter)
			}
		}
	})
}

func TestApplyJitter_DoesNotGoNegative(t *testing.T) {
	withDeterministicRNG(t, 2, func() {
		base := 1 * time.Millisecond
		jitter := 10 * time.Millisecond
		for i := 0; i < 200; i++ {
			got := applyJitter(base, jitter)
			if got < 0 {
				t.Fatalf("negative duration: %v", got)
			}
		}
	})
}
