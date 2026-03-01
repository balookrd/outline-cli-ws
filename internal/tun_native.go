//go:build !unit && linux

package internal

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"

	"github.com/songgao/water"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// ----- TUN open (existing interface created by script) -----

func openExistingTun(name string, debug bool) (*water.Interface, int, error) {
	if name == "" {
		return nil, 0, fmt.Errorf("tun.device is empty")
	}
	if _, err := net.InterfaceByName(name); err != nil {
		return nil, 0, fmt.Errorf("tun interface %q not found (create it in start script): %w", name, err)
	}

	cfg := water.Config{DeviceType: water.TUN}
	cfg.Name = name
	ifce, err := water.New(cfg)
	if err != nil {
		return nil, 0, fmt.Errorf("open tun %q: %w", name, err)
	}

	if err := ensureTunPersistent(ifce, name, debug); err != nil {
		_ = ifce.Close()
		return nil, 0, err
	}

	ifi, err := net.InterfaceByName(name)
	if err != nil {
		_ = ifce.Close()
		return nil, 0, fmt.Errorf("InterfaceByName(%q): %w", name, err)
	}
	mtu := ifi.MTU
	if mtu <= 0 {
		mtu = 1500
	}
	return ifce, mtu, nil
}

func ensureTunPersistent(ifce *water.Interface, name string, debug bool) error {
	f, ok := ifce.ReadWriteCloser.(*os.File)
	if !ok {
		return fmt.Errorf("tun %q: unexpected handle type %T", name, ifce.ReadWriteCloser)
	}

	if err := unix.IoctlSetInt(int(f.Fd()), unix.TUNSETPERSIST, 1); err != nil {
		if err == unix.EPERM {
			tunDebugf(debug, "failed to set TUNSETPERSIST for %q (continuing): %v", name, err)
			return nil
		}
		return fmt.Errorf("set TUNSETPERSIST for %q: %w", name, err)
	}

	tunDebugf(debug, "TUN interface %q marked persistent", name)
	return nil
}

func withNetNS(path string, fn func() error) error {
	if path == "" {
		return fn()
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return fmt.Errorf("open current netns: %w", err)
	}
	defer orig.Close()

	target, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open tun.netns %q: %w", path, err)
	}
	defer target.Close()

	if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("setns(%q): %w", path, err)
	}
	defer func() {
		if err := unix.Setns(int(orig.Fd()), unix.CLONE_NEWNET); err != nil {
			log.Printf("WARN: failed to restore original netns: %v", err)
		}
	}()

	return fn()
}

func tunDebugf(enabled bool, format string, args ...any) {
	if enabled {
		log.Printf("tun debug: "+format, args...)
	}
}

func isDNSDestination(dst string) bool {
	_, port, err := net.SplitHostPort(dst)
	return err == nil && port == "53"
}

// ----- Main -----

func RunTunNative(ctx context.Context, cfg TunConfig, lb *LoadBalancer) error {
	if !cfg.Enable {
		return nil
	}
	if cfg.Device == "" {
		return fmt.Errorf("tun.device is empty")
	}

	// defaults
	if cfg.UDPMaxFlows == 0 {
		cfg.UDPMaxFlows = 4096
	}
	if cfg.UDPIdleTimeout == 0 {
		cfg.UDPIdleTimeout = 60 * time.Second
	}
	if cfg.UDPGCInterval == 0 {
		cfg.UDPGCInterval = 10 * time.Second
	}

	log.Printf("TUN mode enabled (native), expecting existing interface %q", cfg.Device)
	if cfg.NetNS != "" {
		log.Printf("TUN netns enabled: opening %q inside %q", cfg.Device, cfg.NetNS)
	}
	if cfg.Debug {
		log.Printf("TUN debug logging is enabled")
	}

	var (
		ifce *water.Interface
		mtu  int
	)
	err := withNetNS(cfg.NetNS, func() error {
		var openErr error
		ifce, mtu, openErr = openExistingTun(cfg.Device, cfg.Debug)
		return openErr
	})
	if err != nil {
		return err
	}
	defer ifce.Close()

	log.Printf("TUN opened: %s (mtu=%d)", cfg.Device, mtu)

	st := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	ep := channel.New(4096, uint32(mtu), "")
	const nicID tcpip.NICID = 1
	if err := st.CreateNIC(nicID, ep); err != nil {
		// gVisor returns tcpip.Error here (not the built-in error interface),
		// so we can't wrap it with %w.
		return fmt.Errorf("CreateNIC: %v", err)
	}

	// Important tuning for TUN:
	_ = st.SetPromiscuousMode(nicID, true)
	_ = st.SetSpoofing(nicID, true)

	st.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	portTable := newUDPPortTable(lb, cfg)

	go func() {
		t := time.NewTicker(cfg.UDPGCInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				portTable.gcOnce()
			}
		}
	}()

	// TCP forwarder
	tcpFwd := tcp.NewForwarder(st, 0, 65535, func(r *tcp.ForwarderRequest) {
		id := r.ID()

		var wq waiter.Queue
		epTCP, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)

		go tunHandleTCP(ctx, lb, epTCP, id, &wq, cfg.Debug)
	})
	st.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// UDP forwarder
	udpFwd := udp.NewForwarder(st, func(r *udp.ForwarderRequest) {
		id := r.ID()

		var wq waiter.Queue
		epUDP, err := r.CreateEndpoint(&wq)
		if err != nil {
			return
		}
		go tunHandleUDP(ctx, lb, portTable, epUDP, id, &wq, cfg.Debug)
	})
	st.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	// Pumps
	errCh := make(chan error, 2)
	go func() { errCh <- tunToStack(ctx, ifce, ep, cfg.Debug) }()
	go func() { errCh <- stackToTun(ctx, ifce, ep, cfg.Debug) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func tunToStack(ctx context.Context, ifce *water.Interface, ep *channel.Endpoint, debug bool) error {
	buf := make([]byte, 65535)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		n, err := ifce.Read(buf)
		if err != nil {
			tunDebugf(debug, "read from tun failed: %v", err)
			return err
		}
		pkt := buf[:n]

		var proto tcpip.NetworkProtocolNumber
		switch pkt[0] >> 4 {
		case 4:
			proto = ipv4.ProtocolNumber
		case 6:
			proto = ipv6.ProtocolNumber
		default:
			tunDebugf(debug, "drop packet with unknown L3 version (len=%d)", len(pkt))
			continue
		}

		pb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(append([]byte(nil), pkt...)),
		})
		ep.InjectInbound(proto, pb)
		pb.DecRef()
	}
}

func stackToTun(ctx context.Context, ifce *water.Interface, ep *channel.Endpoint, debug bool) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		pb := ep.Read()
		if pb == nil {
			time.Sleep(1 * time.Millisecond)
			continue
		}
		v := pb.ToView()
		b := append([]byte(nil), v.AsSlice()...)
		pb.DecRef()

		if _, err := ifce.Write(b); err != nil {
			tunDebugf(debug, "write to tun failed: %v", err)
			return err
		}
	}
}

func tunHandleTCP(ctx context.Context, lb *LoadBalancer, epTCP tcpip.Endpoint, id stack.TransportEndpointID, wq *waiter.Queue, debug bool) {
	defer epTCP.Close()

	nsConn := gonet.NewTCPConn(wq, epTCP)
	defer nsConn.Close()

	// gVisor forwarder IDs are oriented as local=packet destination, remote=packet source.
	// For outbound packets from namespace workload: remote=workload, local=real destination.
	dst := net.JoinHostPort(net.IP(id.LocalAddress.AsSlice()).String(), fmt.Sprintf("%d", id.LocalPort))
	tunDebugf(debug, "tcp flow: %s:%d -> %s", net.IP(id.RemoteAddress.AsSlice()).String(), id.RemotePort, dst)

	up, err := lb.PickTCP()
	if err != nil {
		tunDebugf(debug, "PickTCP failed for dst=%s: %v", dst, err)
		return
	}
	out, err := DialOutlineTCP(ctx, lb, up, dst)
	if err != nil {
		tunDebugf(debug, "DialOutlineTCP failed dst=%s via upstream=%s: %v", dst, up.cfg.Name, err)
		lb.ReportTCPFailure(up, err)
		return
	}
	defer out.Close()

	go func() {
		if _, err := io.Copy(out, nsConn); err != nil {
			// минимум: лог, иначе errcheck будет ругаться
			log.Printf("tun: io.Copy nsConn->out: %v", err)
		}
	}()
	_, _ = io.Copy(nsConn, out)
}

func tunHandleUDP(ctx context.Context, lb *LoadBalancer, pt *udpPortTable, epUDP tcpip.Endpoint, id stack.TransportEndpointID, wq *waiter.Queue, debug bool) {
	defer epUDP.Close()

	nsUDP := gonet.NewUDPConn(wq, epUDP)
	defer nsUDP.Close()

	var srcIP netip.Addr
	if id.RemoteAddress.Len() == 4 {
		srcIP = netip.AddrFrom4([4]byte(id.RemoteAddress.AsSlice()))
	} else {
		srcIP = netip.AddrFrom16([16]byte(id.RemoteAddress.AsSlice()))
	}
	var dstAddr netip.Addr
	if id.LocalAddress.Len() == 4 {
		dstAddr = netip.AddrFrom4([4]byte(id.LocalAddress.AsSlice()))
	} else {
		dstAddr = netip.AddrFrom16([16]byte(id.LocalAddress.AsSlice()))
	}
	dst := net.JoinHostPort(dstAddr.String(), fmt.Sprintf("%d", id.LocalPort))
	if cfgDNS := isDNSDestination(dst); cfgDNS || debug {
		tunDebugf(true, "udp flow: %s:%d -> %s", srcIP.String(), id.RemotePort, dst)
	}

	pk := udpPortKey{
		netProto: 4,
		srcIP:    srcIP,
		srcPort:  id.RemotePort,
	}
	if srcIP.Is6() {
		pk.netProto = 6
	}

	ps, err := pt.getOrCreate(ctx, pk)
	if err != nil {
		tunDebugf(true, "udp session create failed for %s:%d -> %s: %v", srcIP.String(), id.LocalPort, dst, err)
		return
	}

	// subscribe once per dst inside this port-session
	ps.mu.Lock()

	if _, ok := ps.flows[dst]; !ok {
		maxDst := pt.cfg.UDPMaxDstPerPort
		if maxDst <= 0 {
			maxDst = 512
		}
		if len(ps.flows) >= maxDst {
			tunDebugf(debug, "udp drop flow %s: max dst per port reached (%d)", dst, maxDst)
			ps.mu.Unlock()
			return
		}
		ps.flows[dst] = time.Now()

		dstLocal := dst
		replyCh := ps.sess.Subscribe(dstLocal)
		go func(dst string) {
			defer ps.sess.Unsubscribe(dst)
			for {
				select {
				case <-ctx.Done():
					return
				case p, ok := <-replyCh:
					if !ok {
						tunDebugf(debug, "udp reply channel closed for dst=%s", dst)
						return
					}
					if _, err := nsUDP.Write(p.B); err != nil {
						tunDebugf(true, "udp write back to stack failed dst=%s: %v", dst, err)
						p.Release()
						return
					}
					p.Release()

					now := time.Now()
					ps.mu.Lock()
					ps.flows[dst] = now
					ps.mu.Unlock()

					pt.mu.Lock()
					if cur := pt.ports[pk]; cur != nil {
						cur.lastSeen = now
					}
					pt.mu.Unlock()
				}
			}
		}(dstLocal)
	}
	ps.mu.Unlock()

	// stack -> outline
	buf := make([]byte, 65535)
	for {
		n, _, err := nsUDP.ReadFrom(buf)
		if err != nil {
			tunDebugf(debug, "udp read from stack failed dst=%s: %v", dst, err)
			break
		}
		if n == 0 {
			continue
		}

		if err := ps.sess.Send(dst, buf[:n]); err != nil {
			tunDebugf(true, "udp send to outline failed dst=%s len=%d upstream=%s: %v", dst, n, ps.up.cfg.Name, err)
			lb.ReportUDPFailure(ps.up, err)
			break
		}
		if isDNSDestination(dst) && (debug || n > 0) {
			tunDebugf(true, "udp dns query forwarded dst=%s len=%d", dst, n)
		}

		now := time.Now()
		ps.mu.Lock()
		ps.flows[dst] = now
		ps.mu.Unlock()

		pt.mu.Lock()
		if cur := pt.ports[pk]; cur != nil {
			cur.lastSeen = now
		}
		pt.mu.Unlock()
	}

	// optional: remove dst mapping early (GC и так вычистит)
	ps.mu.Lock()
	delete(ps.flows, dst)
	ps.mu.Unlock()
	ps.sess.Unsubscribe(dst)
	tunDebugf(debug, "udp flow closed: %s", dst)
}
