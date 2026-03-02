package internal

import (
	"log"
	"sync/atomic"
)

var wsDebugEnabled atomic.Bool

func SetWebSocketDebug(enabled bool) {
	wsDebugEnabled.Store(enabled)
}

func wsDebugf(format string, args ...any) {
	if wsDebugEnabled.Load() {
		log.Printf("[WSDEBUG] "+format, args...)
	}
}
