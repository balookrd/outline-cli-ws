//go:build !unit

package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"outline-cli-ws/pkg/outlinews"
	"syscall"
	"time"
)

func main() {
	var cfgPath string
	var metricsAddr string
	var noProbes bool
	flag.StringVar(&cfgPath, "c", "config.yaml", "config path")
	flag.StringVar(&metricsAddr, "metrics", "", "prometheus metrics listen address, e.g. :9100")
	flag.BoolVar(&noProbes, "no-probes", false, "disable background health checks/probes/warm-standby for clean per-request logs")
	flag.Parse()

	cfg, err := outlinews.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	outlinews.SetWebSocketDebug(cfg.WebSocket.Debug)
	if cfg.WebSocket.Debug {
		log.Printf("WebSocket debug logging is enabled")
	}

	lb := outlinews.NewLoadBalancer(cfg.Upstreams, cfg.Healthcheck, cfg.Selection, cfg.Probe, cfg.Fwmark)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if metricsAddr != "" {
		outlinews.EnablePrometheusMetrics()
		go func() {
			if err := outlinews.StartMetricsServer(ctx, metricsAddr); err != nil {
				log.Printf("metrics server stopped: %v", err)
			}
		}()
		log.Printf("Prometheus metrics listening on %s", metricsAddr)
	}

	disableProbes := cfg.DisableProbes || noProbes
	if disableProbes {
		lb.DisableBackgroundProbes()
		log.Printf("background probes are disabled (disable_probes=%v, -no-probes=%v)", cfg.DisableProbes, noProbes)
	} else {
		// Health-check loop
		go lb.RunHealthChecks(ctx)
		go lb.RunWarmStandby(ctx)
	}

	socksAddr := cfg.Listen.SOCKS5
	socksEnabled := socksAddr != ""
	tunEnabled := cfg.Tun.Device != ""

	if !socksEnabled && !tunEnabled {
		log.Fatal("nothing to run: neither listen.socks5 nor tun.device is configured")
	}
	if tunEnabled && !socksEnabled && cfg.Tun.NetNS == "" {
		if _, err := net.InterfaceByName(cfg.Tun.Device); err != nil {
			log.Fatalf("tun-only mode requires existing interface %q: %v", cfg.Tun.Device, err)
		}
	}

	var ln net.Listener
	var srv *outlinews.Socks5Server
	if socksEnabled {
		ln, err = net.Listen("tcp", socksAddr)
		if err != nil {
			log.Fatalf("listen socks5 %s: %v", socksAddr, err)
		}
		log.Printf("SOCKS5 listening on %s", socksAddr)
		srv = &outlinews.Socks5Server{LB: lb}
	} else {
		log.Printf("SOCKS5 disabled: listen.socks5 is empty")
	}

	if tunEnabled {
		log.Printf("TUN mode enabled (native), expecting existing interface %q", cfg.Tun.Device)

		go func() {
			if err := outlinews.RunTunNative(ctx, cfg.Tun, lb); err != nil {
				log.Printf("tun native stopped: %v", err)
				cancel()
			}
		}()
	}

	// Graceful shutdown
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigc
		log.Printf("shutting down...")
		cancel()
		if ln != nil {
			_ = ln.Close()
		}
	}()

	if !socksEnabled {
		<-ctx.Done()
		return
	}

	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			log.Printf("accept: %v", err)
			continue
		}
		go func() {
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			srv.HandleConn(ctx, c)
		}()
	}
}
