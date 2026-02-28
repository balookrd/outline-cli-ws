package internal

import (
	"math/rand"
	"sync"
	"time"
)

var (
	rngMu sync.Mutex
	rng   = rand.New(rand.NewSource(time.Now().UnixNano()))
)

func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	rngMu.Lock()
	v := rng.Int63n(n)
	rngMu.Unlock()
	return v
}
