package internal

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SetWebSocketDebug(true)
	os.Exit(m.Run())
}
