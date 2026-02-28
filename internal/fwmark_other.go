//go:build !linux

package internal

import "fmt"

func setSocketMark(fd uintptr, mark uint32) error {
	if mark == 0 {
		return nil
	}
	return fmt.Errorf("fwmark is supported only on linux")
}
