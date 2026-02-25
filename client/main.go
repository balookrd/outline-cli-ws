package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"outline-cli-ws/internal"
	"syscall"
	"time"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "c", "config.yaml", "config path")
	flag.Parse()

	cfg, err := internal.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	lb := internal.NewLoadBalancer(cfg.Upstreams, cfg.Healthcheck, cfg.Selection, cfg.Probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	srv := &internal.Socks5Server{LB: lb}

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
