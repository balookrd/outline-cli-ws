//go:build !linux

package internal

import (
	"context"
	"fmt"
)

func RunTunNative(ctx context.Context, cfg TunConfig, lb *LoadBalancer) error {
	if !cfg.Enable {
		return nil
	}
	if cfg.Device == "" {
		return fmt.Errorf("tun.device is empty")
	}
	return fmt.Errorf("TUN mode supported only on linux")
}
