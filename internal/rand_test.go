package internal

import (
	"math/rand"
	"testing"
)

func TestRandInt63n_EdgeCases(t *testing.T) {
	if got := randInt63n(0); got != 0 {
		t.Fatalf("n=0: expected 0, got %d", got)
	}
	if got := randInt63n(-1); got != 0 {
		t.Fatalf("n<0: expected 0, got %d", got)
	}
}

func TestRandInt63n_Range(t *testing.T) {
	rngMu.Lock()
	old := rng
	rng = rand.New(rand.NewSource(123))
	rngMu.Unlock()
	t.Cleanup(func() {
		rngMu.Lock()
		rng = old
		rngMu.Unlock()
	})

	n := int64(7)
	for i := 0; i < 1000; i++ {
		v := randInt63n(n)
		if v < 0 || v >= n {
			t.Fatalf("out of range: %d (n=%d)", v, n)
		}
	}
}
