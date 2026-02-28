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
	flag.StringVar(&cfgPath, "c", "config.yaml", "config path")
	flag.StringVar(&metricsAddr, "metrics", "", "prometheus metrics listen address, e.g. :9100")
	flag.Parse()

	cfg, err := outlinews.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
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

	// Health-check loop
	go lb.RunHealthChecks(ctx)
	go lb.RunWarmStandby(ctx)

	// SOCKS5 server
	addr := cfg.Listen.SOCKS5
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen socks5 %s: %v", addr, err)
	}
	log.Printf("SOCKS5 listening on %s", addr)

	srv := &outlinews.Socks5Server{LB: lb}

	if cfg.Tun.Enable {
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
		_ = ln.Close()
	}()

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
