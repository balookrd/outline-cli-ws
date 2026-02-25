package internal

import "time"

func applyJitter(d, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return d
	}
	// равномерно в диапазоне [-jitter, +jitter]
	// важно: нужен math/rand, но лучше rand.New(rand.NewSource(...)) глобально
	j := time.Duration((randInt63n(int64(2*jitter)+1) - int64(jitter)))
	if d+j < 0 {
		return d
	}
	return d + j
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
