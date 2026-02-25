//go:build linux

package internal

import (
	"fmt"
	"syscall"
)

func setSocketMark(fd uintptr, mark uint32) error {
	if mark == 0 {
		return nil
	}
	// SO_MARK = 36 на Linux
	if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, int(mark)); err != nil {
		return fmt.Errorf("setsockopt SO_MARK=%d: %w", mark, err)
	}
	return nil
}
