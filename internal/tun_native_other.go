//go:build !unit && !linux

package internal

import (
	"context"
	"fmt"
)

func RunTunNative(ctx context.Context, cfg TunConfig, lb *LoadBalancer) error {
	_ = ctx
	_ = lb
	return fmt.Errorf("tun mode is supported only on linux")
}
