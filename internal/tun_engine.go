package internal

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/engine"
)

func StartTun2SocksEngine(ctx context.Context, tun TunConfig, socksListen string, fwmark uint32) (func(), error) {
	if !tun.Enable {
		log.Printf("TUN mode disabled")
		return func() {}, nil
	}

	if tun.Device == "" {
		return nil, fmt.Errorf("tun.enable=true but tun.device is empty")
	}

	if !tun.Auto {
		log.Printf("TUN mode enabled (auto=false), expecting existing interface %q", tun.Device)

		if err := checkTunExists(tun.Device); err != nil {
			return nil, fmt.Errorf("tun.auto=false and %w", err)
		}
	} else {
		log.Printf("TUN mode enabled (auto=true), attempting to manage interface %q", tun.Device)

		if _, err := os.Stat("/dev/net/tun"); err != nil {
			return nil, fmt.Errorf("tun.auto=true but /dev/net/tun not available: %w", err)
		}
	}

	proxyURL := "socks5://" + socksListen

	k := &engine.Key{
		Device:     tun.Device,
		MTU:        tun.MTU,
		Interface:  tun.OutIface,
		Proxy:      proxyURL,
		Mark:       int(fwmark),
		LogLevel:   tun.LogLevel,
		UDPTimeout: 60 * time.Second,
	}

	engine.Insert(k)
	engine.Start()

	log.Printf("TUN engine started (device=%s, mtu=%d, out-if=%s, fwmark=%d)",
		tun.Device, tun.MTU, tun.OutIface, fwmark)

	stopFn := func() {
		log.Printf("Stopping TUN engine")
		engine.Stop()
	}

	go func() {
		<-ctx.Done()
		engine.Stop()
	}()

	return stopFn, nil
}

func checkTunExists(name string) error {
	if name == "" {
		return fmt.Errorf("tun.device is empty")
	}
	_, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("tun interface %q not found", name)
	}
	return nil
}
